// gateway-detector — компонент авто-обхода (T41).
//
// Сущности:
//   - prober  — проверка цели напрямую vs через VPS -> вердикт (готово);
//   - watcher — пассивный (pcap) детект провалов прямого TCP + SNI (позже);
//   - applier — применение подтверждённого блока (autoroute.json + xray API) (позже).
//
// Пока: CLI-обёртка над prober для проверки/отладки механизма.
//
//	gateway-detector probe <target> [--port 443] [--sni name] [--socks 127.0.0.1:1081] [--no-tls]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gateway-detector/applier"
	"gateway-detector/prober"
	"gateway-detector/watcher"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "probe":
		// ниже
	case "watch":
		runWatch()
		return
	case "recheck":
		runRecheck()
		return
	case "watch-ebpf":
		runWatchEBPF()
		return
	default:
		usage()
	}
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	port := fs.Int("port", 443, "порт цели")
	sni := fs.String("sni", "", "SNI для TLS (по умолчанию = target)")
	socks := fs.String("socks", "127.0.0.1:1081", "адрес VPS-socks5")
	noTLS := fs.Bool("no-tls", false, "не делать TLS-рукопожатие (просто TCP-connect)")
	timeout := fs.Duration("timeout", 5*time.Second, "таймаут на фазу")
	fs.Parse(os.Args[3:])

	target := os.Args[2]
	res := prober.Probe(target, *port, *sni, prober.Config{
		SocksAddr: *socks, Timeout: *timeout, TLS: !*noTLS,
	})
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:\n  gateway-detector probe <target> [--port N] [--sni name] [--socks addr] [--no-tls]\n  gateway-detector watch --iface enp2s0 [--vps IP] [--apply]\n  gateway-detector recheck [--socks addr] [--workers N] [--apply]\n  gateway-detector watch-ebpf --iface enp2s0 [--vps IP] [--apply]  (T59, тень/сравнение с watch)")
	os.Exit(2)
}

func runWatch() {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	iface := fs.String("iface", "", "WAN-интерфейс (авто, если пусто)")
	vps := fs.String("vps", "", "IP VPS — исключить туннель (авто из --config-env, если пусто)")
	configEnv := fs.String("config-env", "/root/gateway-universal/config.env", "config.env для VPS_ADDR")
	socks := fs.String("socks", "127.0.0.1:1081", "VPS-socks5 для перепроверки")
	apply := fs.Bool("apply", false, "применять (иначе тень: только лог, что БЫ добавил)")
	fs.Parse(os.Args[2:])
	if *iface == "" {
		*iface = detectIface()
	}
	if *vps == "" {
		*vps = resolveHost(configVar(*configEnv, "VPS_ADDR"))
	}
	if *iface == "" {
		log.Fatal("не удалось определить WAN-интерфейс, задайте --iface")
	}
	gwdbScript = filepath.Join(filepath.Dir(*configEnv), "scripts", "gwdb.py")
	log.Printf("detector: iface=%s vps=%s apply=%v", *iface, *vps, *apply)
	// watcher -> prober (подтверждение) -> [тень: лог | apply: применить]
	handler := buildCandidateHandler(apply, socks)
	w := &watcher.Watcher{Iface: *iface, VPSIP: *vps, OnCandidate: handler}
	if err := w.Run(); err != nil {
		log.Fatalf("watch: %v", err)
	}
}

// buildCandidateHandler — общая логика применения кандидата (whitelist/
// curated/isBrainEntity/autoroute/prober/applier/enqueueBrain), одна на ОБА
// источника событий — pcap (runWatch) и eBPF (runWatchEBPF, T59). Меняется
// только то, ЧТО поставляет Candidate, не то, что с ним делают.
func buildCandidateHandler(apply *bool, socks *string) func(watcher.Candidate) {
	return func(c watcher.Candidate) {
		// UDP-«без ответа»: НЕ авто-добавляем. Молчание UDP неоднозначно — многие
		// сервисы (напр. latency-пинги AWS GameLift) не отвечают by design, а не
		// из-за блока, и маршрут через VPS их не оживляет. Поэтому только логируем
		// кандидата — можно добавить вручную, если это реально нужный адрес.
		if c.Signal == "udp-no-reply" {
			log.Printf("🔵 UDP без ответа: %s:%d (кандидат, не добавляю — UDP-молчание неоднозначно)", c.DstIP, c.Port)
			return
		}
		// whitelist (T49): не анализируем вообще (даже тенью), если SNI попадает
		// под правило whitelist (.ru/.рф/.su, см. gwdb.py) — КРОМЕ доменов, явно
		// прописанных на VPS в xray/domains/*.txt (курируемый список приоритетнее).
		if c.SNI != "" && isWhitelisted(c.SNI) && !inCuratedRouting(c.SNI) {
			return
		}
		// adguard-filter (T-junk-filter, 2026-08-16): реклама/трекеры/фишинг-
		// типосквоттинг из уже загруженных блок-листов AdGuardHome — те же
		// правила, что и так режут DNS-запрос до этого момента в норме, но
		// пассивный детектор слушает сырой трафик (pcap/eBPF) и иногда ловит
		// то, что резолвилось раньше/через другой DNS. См. adguard_filter.go.
		if c.SNI != "" && !inCuratedRouting(c.SNI) && isAdGuardBlocked(c.SNI) {
			log.Printf("🚫 мусор (AdGuardHome-блоклист): %s — не анализирую", c.SNI)
			return
		}
		// уже обрабатывается — не пере-обрабатываем (иначе петля: свой трафик
		// стенда zapret не десинхронизирует, прямая проба всегда «блок»). НО держим
		// ipset свежим наблюдаемым IP клиента: Cloudflare/DoH отдают клиенту другие
		// IP, чем резолвит стенд — без этого сущность/VPS не ловят реальный трафик.
		if c.SNI != "" {
			ip := c.DstIP
			if isBrainEntity(c.SNI) {
				addToSet("brain_"+sanitizeDomain(c.SNI), ip) // сущность: и NFQUEUE, и RETURN по этому ipset
				return
			}
			if inAutoroute(c.SNI) {
				addToSet(applier.IPSet, ip) // VPS
				return
			}
			if inZapretHostlist(c.SNI) {
				return // hostlist-сервис фильтрует по SNI — IP не важен
			}
		}
		// T57: QUIC-сигнал — prober.Probe ниже всегда бьёт по TCP, для чисто UDP-блока
		// (у нас самих же глобальный DROP UDP/443 кроме Meta, см. zapret/zapret.sh) TCP
		// почти всегда открыт => verdict=ok => кандидат тихо теряется, до мозга не доходит.
		// Отдельная активная проверка через curl --http3-only (напрямую, без VPS-сравнения —
		// прогон QUIC через VPS-socks не поддержан) перед обычным TCP-путём.
		if c.Signal == "quic-no-response" && c.SNI != "" {
			if quicBlocked(c.SNI) {
				if *apply {
					if applier.Apply(c.SNI, c.Signal, 443) {
						log.Printf("✅ мгновенно в VPS (QUIC): %s (dst=%s)", c.SNI, c.DstIP)
					}
					addToSet(applier.IPSet, c.DstIP)
					if enqueueBrain(c.SNI, c.Signal) {
						log.Printf("→ %s в очередь мозга (проверка zapret UDP)", c.SNI)
					}
				} else {
					log.Printf("🟡 БЫ добавил (QUIC): %s", c.SNI)
				}
			} else {
				log.Printf("⚪ %s — QUIC напрямую снова работает, не добавляю", c.SNI)
			}
			return
		}
		target := c.SNI
		if target == "" {
			target = c.DstIP // без SNI (syn-timeout) — проверяем по IP
		}
		port := c.Port
		if port == 0 {
			port = 443
		}
		// с SNI — TLS-рукопожатие (DPI режет по имени); без SNI — только TCP-коннект
		// (блок по IP: SYN дропается, до TLS дело не доходит) на реальном порту.
		res := prober.Probe(target, port, c.SNI, prober.Config{SocksAddr: *socks, Timeout: 6 * time.Second, TLS: c.SNI != ""})
		switch res.Verdict {
		case prober.Blocked:
			if *apply {
				if applier.Apply(target, c.Signal, port) {
					log.Printf("✅ мгновенно в VPS: %s (dst=%s)", target, c.DstIP)
				} else {
					log.Printf("… %s подтверждён, но уже в списке / тумблер выкл", target)
				}
				addToSet(applier.IPSet, c.DstIP) // + реальный IP клиента (мультиIP CDN)
				// домен (по SNI) — в очередь «мозга»: воркер попробует перевести на
				// zapret (низкий пинг). Блоки по чистому IP zapret не пробьёт — остаются VPS.
				if c.SNI != "" && enqueueBrain(c.SNI, c.Signal) {
					log.Printf("→ %s в очередь мозга (проверка zapret)", c.SNI)
				}
			} else {
				log.Printf("🟡 БЫ добавил: %-30s (блок подтверждён; direct=%s)", target, short(res.Direct))
			}
		default:
			log.Printf("⚪ %-30s кандидат, но prober=%s — НЕ добавляю (direct=%s vps=%s)", target, res.Verdict, short(res.Direct), short(res.ViaVPS))
		}
	}
}

// runRecheck — ночная перепроверка списка авто-обхода: пробим каждую запись
// напрямую vs через VPS. Если напрямую снова работает (блок снят) ДВА прогона
// подряд — убираем из списка. Пул воркеров: большой список — минуты, не часы.
func runRecheck() {
	fs := flag.NewFlagSet("recheck", flag.ExitOnError)
	socks := fs.String("socks", "127.0.0.1:1081", "VPS-socks5 для перепроверки")
	workers := fs.Int("workers", 30, "число параллельных проб")
	timeout := fs.Duration("timeout", 6*time.Second, "таймаут на фазу пробы")
	apply := fs.Bool("apply", false, "реально удалять (иначе тень: только лог)")
	fs.Parse(os.Args[2:])

	cfg := readRecheckCfg()
	if !cfg.Enabled {
		log.Printf("recheck: перепроверка выключена в настройках — пропускаю")
		return
	}
	if cfg.Workers > 0 {
		*workers = cfg.Workers
	}
	s, err := applier.Load()
	if err != nil {
		log.Fatalf("recheck: чтение списка: %v", err)
	}
	// работаем независимо от тумблера авто-обхода: список чистим всегда,
	// ipset пересобираем только если авто-обход включён (иначе он и так пуст).
	// пробуем только то, что можно проверить: домен или одиночный IP (не geosite/CIDR)
	var todo []applier.Entry
	for _, e := range s.Entries {
		if strings.HasPrefix(e.Addr, "geosite:") || strings.Contains(e.Addr, "/") {
			continue
		}
		todo = append(todo, e)
	}
	log.Printf("recheck: записей всего=%d, проверяю=%d, воркеров=%d, apply=%v", len(s.Entries), len(todo), *workers, *apply)
	start := time.Now()

	type res struct {
		addr string
		ok   bool // прямая проба прошла (блок снят)
	}
	jobs := make(chan applier.Entry)
	out := make(chan res)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				port := e.DPort
				if port == 0 {
					port = 443
				}
				sni := ""
				if net.ParseIP(e.Addr) == nil {
					sni = e.Addr // домен -> TLS по имени
				}
				r := prober.Probe(e.Addr, port, sni, prober.Config{
					SocksAddr: *socks, Timeout: *timeout, TLS: sni != "" || port == 443,
				})
				out <- res{addr: e.Addr, ok: r.Verdict == prober.OK}
			}
		}()
	}
	go func() {
		for _, e := range todo {
			jobs <- e
		}
		close(jobs)
	}()
	go func() { wg.Wait(); close(out) }()

	// текущие счётчики clean по addr
	cur := map[string]int{}
	for _, e := range s.Entries {
		cur[e.Addr] = e.Clean
	}
	remove := map[string]bool{}
	clean := map[string]int{}
	var okCnt, stillCnt int
	for r := range out {
		if r.ok {
			okCnt++
			c := cur[r.addr] + 1
			if c >= 2 {
				remove[r.addr] = true
				log.Printf("🗑  %s — разблокирован (чисто 2 раза подряд)%s", r.addr, dryTag(*apply))
			} else {
				clean[r.addr] = c
				log.Printf("🟢 %s — сейчас работает напрямую (1/2, оставляю)", r.addr)
			}
		} else {
			stillCnt++
			if cur[r.addr] > 0 {
				clean[r.addr] = 0 // блок вернулся — сброс серии
			}
		}
	}

	if *apply {
		kept := applier.UpdateClean(remove, clean)
		if s.RouteOn() {
			applier.Sync(kept) // ресинк ipset только когда применение включено
		}
	}
	dur := time.Since(start)
	log.Printf("recheck: готово за %s — снято=%d, ещё блок=%d, помечено-к-снятию=%d",
		dur.Truncate(time.Second), len(remove), stillCnt, okCnt-len(remove))
	if *apply {
		writeRecheckStats(len(todo), len(remove), okCnt-len(remove), int(dur.Seconds()))
	}
}

const recheckFile = "/etc/gateway/recheck.json"

type recheckCfg struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time"`
	Days    string `json:"days"`
	Workers int    `json:"workers"`
	// stats:
	LastRun      string `json:"last_run,omitempty"`
	LastChecked  int    `json:"last_checked,omitempty"`
	LastRemoved  int    `json:"last_removed,omitempty"`
	LastPending  int    `json:"last_pending,omitempty"`
	LastDuration int    `json:"last_duration_sec,omitempty"`
}

// readRecheckCfg — конфиг перепроверки (дефолт: включена). Отсутствие файла =
// включена (обратная совместимость с уже стоящим таймером).
func readRecheckCfg() recheckCfg {
	c := recheckCfg{Enabled: true, Workers: 30}
	if data, err := os.ReadFile(recheckFile); err == nil {
		json.Unmarshal(data, &c)
	}
	return c
}

// writeRecheckStats — обновить поля статистики, сохранив конфиг (read-modify-write).
func writeRecheckStats(checked, removed, pending, durSec int) {
	c := readRecheckCfg()
	c.LastRun = time.Now().UTC().Format(time.RFC3339)
	c.LastChecked, c.LastRemoved, c.LastPending, c.LastDuration = checked, removed, pending, durSec
	if b, err := json.MarshalIndent(c, "", "  "); err == nil {
		tmp := recheckFile + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			os.Rename(tmp, recheckFile)
		}
	}
}

func dryTag(apply bool) string {
	if apply {
		return ""
	}
	return " [тень]"
}

// gwdbScript — путь к scripts/gwdb.py (whitelist + presets, T48/T49). Задаётся
// в runWatch() из --config-env (тот же репозиторий).
var gwdbScript = "/root/gateway-universal/scripts/gwdb.py"

// isWhitelisted — проверка через gwdb.py (единая точка схемы с bash-стороной
// мозга и gateway-ui, см. scripts/gwdb.py). Не найден python3/скрипт — считаем
// НЕ whitelisted (fail-open к анализу, не fail-open к пропуску блокировок).
func isWhitelisted(domain string) bool {
	out, err := exec.Command("python3", gwdbScript, "whitelisted", domain).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

// quicBlocked — T57: активная проверка QUIC (curl --http3-only, напрямую, без
// VPS — SOCKS5 не тащит QUIC/UDP полноценно). Пустой/ошибочный код ответа = блок.
func quicBlocked(domain string) bool {
	out, err := exec.Command("curl", "--http3-only", "-s", "-o", "/dev/null",
		"-w", "%{http_code}", "--max-time", "6", "https://"+domain+"/").Output()
	if err != nil {
		return true
	}
	code := strings.TrimSpace(string(out))
	return code == "" || code == "000"
}

// inCuratedRouting — домен (или его родитель) явно прописан в xray/domains/*.txt
// (курируемый список ручного VPS-роутинга) — приоритетнее правила whitelist.
// Каталог берём рядом с gwdbScript (тот же репозиторий: <repo>/scripts/gwdb.py
// и <repo>/xray/domains/ — общий родитель <repo>).
func inCuratedRouting(domain string) bool {
	dir := filepath.Join(filepath.Dir(filepath.Dir(gwdbScript)), "xray", "domains")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	domain = strings.ToLower(domain)
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".txt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(data), "\n") {
			t := strings.ToLower(strings.TrimSpace(ln))
			if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "geosite:") {
				continue
			}
			if domain == t || strings.HasSuffix(domain, "."+t) {
				return true
			}
		}
	}
	return false
}

const brainQueueFile = "/etc/gateway/brain-queue"
const brainQueueLock = "/etc/gateway/brain-queue.lock"

// enqueueBrain — положить домен в очередь воркера «мозга» (dedup: не в очереди и
// не уже zapret-сущность). Под тем же flock, что worker.pop, чтобы не гонки.
// Строка очереди — "domain\tsource" (T50): source = сигнатура детектора
// (syn-timeout/rst-after-clienthello/...), solve.sh использует её для
// классификации (пропуск перебора пресетов при похожей на IP-блокировку).
func enqueueBrain(domain, source string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || strings.HasPrefix(domain, "geosite:") || isBrainEntity(domain) || inBrainQueue(domain) {
		return false
	}
	lf, err := os.OpenFile(brainQueueLock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer lf.Close()
	syscall.Flock(int(lf.Fd()), syscall.LOCK_EX)
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	if inBrainQueue(domain) { // повторная проверка под локом
		return false
	}
	f, err := os.OpenFile(brainQueueFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	if source == "" {
		source = "unknown"
	}
	f.WriteString(domain + "\t" + source + "\n")
	return true
}

// inBrainQueue сравнивает только домен (первое поле "domain\tsource") — старые
// строки без табуляции (до T50) тоже матчатся целиком, обратная совместимость.
func inBrainQueue(domain string) bool {
	data, err := os.ReadFile(brainQueueFile)
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(data), "\n") {
		d := strings.SplitN(strings.TrimSpace(ln), "\t", 2)[0]
		if d == domain {
			return true
		}
	}
	return false
}

// addToSet — добавить IP в ipset (идемпотентно). Держим наборы свежими реальным
// IP клиента (Cloudflare/DoH отдают ему другие IP, чем резолвит стенд).
func addToSet(set, ip string) {
	if net.ParseIP(ip) != nil && !(set == applier.IPSet && applier.IsProtected(ip)) {
		exec.Command("ipset", "add", set, ip, "-exist").Run()
	}
}

// sanitizeDomain — как san() в brain-apply.sh: не-[a-z0-9] -> _, срезать хвост _.
func sanitizeDomain(d string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(d) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return strings.TrimRight(b.String(), "_")
}

func inAutoroute(domain string) bool {
	data, err := os.ReadFile("/etc/gateway/autoroute.json")
	if err != nil {
		return false
	}
	var ar struct {
		Entries []struct {
			Addr string `json:"addr"`
		} `json:"entries"`
	}
	json.Unmarshal(data, &ar)
	for _, e := range ar.Entries {
		if e.Addr == domain {
			return true
		}
	}
	return false
}

// inZapretHostlist — домен (или его родитель) в hostlist какого-либо zapret-сервиса.
func inZapretHostlist(domain string) bool {
	files, _ := filepath.Glob("/opt/zapret-config/domains.gen/*.txt")
	set := map[string]bool{}
	for _, f := range files {
		data, _ := os.ReadFile(f)
		for _, ln := range strings.Split(string(data), "\n") {
			ln = strings.TrimSpace(ln)
			if ln != "" && !strings.HasPrefix(ln, "#") {
				set[ln] = true
			}
		}
	}
	parts := strings.Split(domain, ".")
	for i := 0; i+1 < len(parts); i++ {
		if set[strings.Join(parts[i:], ".")] {
			return true
		}
	}
	return false
}

func isBrainEntity(domain string) bool {
	data, err := os.ReadFile("/etc/gateway/brain-services.json")
	if err != nil {
		return false
	}
	var svc []struct {
		Domain string `json:"domain"`
	}
	json.Unmarshal(data, &svc)
	for _, s := range svc {
		if s.Domain == domain {
			return true
		}
	}
	return false
}

func short(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}

func detectIface() string {
	out, err := exec.Command("ip", "route", "get", "1.1.1.1").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	for i, w := range f {
		if w == "dev" && i+1 < len(f) {
			return f[i+1]
		}
	}
	return ""
}

func configVar(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, key+"=") {
			v := strings.TrimPrefix(ln, key+"=")
			v = strings.Trim(strings.Fields(v)[0], `"`)
			return v
		}
	}
	return ""
}

func resolveHost(h string) string {
	if h == "" {
		return ""
	}
	if net.ParseIP(h) != nil {
		return h
	}
	if ips, err := net.LookupHost(h); err == nil {
		for _, ip := range ips {
			if p := net.ParseIP(ip); p != nil && p.To4() != nil {
				return ip
			}
		}
	}
	return ""
}
