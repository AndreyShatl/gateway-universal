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
	"strings"
	"sync"
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
	fmt.Fprintln(os.Stderr, "usage:\n  gateway-detector probe <target> [--port N] [--sni name] [--socks addr] [--no-tls]\n  gateway-detector watch --iface enp2s0 [--vps IP] [--apply]\n  gateway-detector recheck [--socks addr] [--workers N] [--apply]")
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
	log.Printf("detector: iface=%s vps=%s apply=%v", *iface, *vps, *apply)
	// watcher -> prober (подтверждение) -> [тень: лог | apply: применить]
	handler := func(c watcher.Candidate) {
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
					log.Printf("✅ добавлен в авто-обход: %s (dst=%s)", target, c.DstIP)
				} else {
					log.Printf("… %s подтверждён, но уже в списке / тумблер выкл", target)
				}
			} else {
				log.Printf("🟡 БЫ добавил: %-30s (блок подтверждён; direct=%s)", target, short(res.Direct))
			}
		default:
			log.Printf("⚪ %-30s кандидат, но prober=%s — НЕ добавляю (direct=%s vps=%s)", target, res.Verdict, short(res.Direct), short(res.ViaVPS))
		}
	}
	w := &watcher.Watcher{Iface: *iface, VPSIP: *vps, OnCandidate: handler}
	if err := w.Run(); err != nil {
		log.Fatalf("watch: %v", err)
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
		if s.Enabled {
			applier.Sync(kept) // ресинк ipset только когда авто-обход включён
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
