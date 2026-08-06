package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Монитор zapret (read-only): демоны nfqws (профиль/стратегия), счётчики NFQUEUE
// (пакеты/дропы), статус сервисов из zapret-services.json. Не трогает боевой путь.

// nfqProfile — один профиль nfqws (один сервис на очереди). У демона их может
// быть много (мультипрофиль через --new) — раскладываем по строкам.
type nfqProfile struct {
	PID      int    `json:"pid"`
	Qnum     int    `json:"qnum"`
	Service  string `json:"service"` // из hostlist (basename) или "весь трафик"
	Proto    string `json:"proto"`   // tcp | udp
	Ports    string `json:"ports"`
	L7       string `json:"l7,omitempty"`
	Strategy string `json:"strategy"` // --dpi-desync… очищено
}

type queueStat struct {
	Qnum     int `json:"qnum"`
	Queued   int `json:"queued"`  // сейчас в очереди
	Dropped  int `json:"dropped"` // дропнуто (очередь полна)
	UserDrop int `json:"user_dropped"`
	Packets  int `json:"packets"` // всего прошло (из iptables)
}

type svcInfo struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Mode     string   `json:"mode"` // zapret | vps
	Domains  int      `json:"domains"`
	Channels []string `json:"channels"` // напр. ["tcp:443","udp:50000-50100 (discord,stun)"]
}

var (
	reQnum     = regexp.MustCompile(`--qnum=(\d+)`)
	reFilterT  = regexp.MustCompile(`--filter-tcp=(\S+)`)
	reFilterU  = regexp.MustCompile(`--filter-udp=(\S+)`)
	reL7       = regexp.MustCompile(`--filter-l7=(\S+)`)
	reHost     = regexp.MustCompile(`--hostlist=(\S+)`)
	reFakePath = regexp.MustCompile(`\s--dpi-desync-fake-\S+=\S+`)
)

func (s *server) handleMonitor(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"profiles":        nfqProfiles(),
		"queues":          queueStats(),
		"services":        svcList(s),
		"accept_only":     acceptOnlyEntities(), // T57: UDP-сущности без десинхронизации — нет процесса, invisible в profiles
		"brain_groups":    brainGroupSummaries(),
		"brain_totals":    brainTotals(),
		"ciadpi_profiles": ciadpiProfiles(),
		"reeval_schedule": s.reevalSchedule(), // T-are: confidence/next_reeval_at по домену (gwdb services-list)
		"ver":             s.ver,              // авто-обновление вкладки после деплоя (checkVer в JS) — раньше это делал /api/scan
	})
}

// reevalScheduleEntry — одна строка confidence-гистерезиса (T-are): домен, его
// текущая стратегия (движок+имя), confidence (0/30/70/100) и когда его в
// следующий раз проверит ночной перебор (или "" — ещё ни разу не тронут).
type reevalScheduleEntry struct {
	Domain       string `json:"domain"`
	Engine       string `json:"engine"`
	StrategyName string `json:"strategy_name"`
	Confidence   int    `json:"confidence"`
	LastReevalAt string `json:"last_reeval_at"`
	NextReevalAt string `json:"next_reeval_at"`
}

func (s *server) reevalSchedule() []reevalScheduleEntry {
	out := []reevalScheduleEntry{}
	raw, err := s.gwdb("services-list")
	if err != nil {
		return out
	}
	for _, ln := range strings.Split(raw, "\n") {
		if ln == "" {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) != 7 {
			continue
		}
		conf, _ := strconv.Atoi(f[4])
		out = append(out, reevalScheduleEntry{
			Domain: f[0], Engine: f[2], StrategyName: f[3],
			Confidence: conf, LastReevalAt: f[5], NextReevalAt: f[6],
		})
	}
	return out
}

// ciadpiProfile — один живой ciadpi-процесс (T-ciadpi): в отличие от nfqws у него
// нет очереди/дропов (REDIRECT, не NFQUEUE) — метрики другие, поэтому отдельная
// структура/таблица, не натягиваем на nfqProfile.
type ciadpiProfile struct {
	PID      int    `json:"pid"`
	Port     int    `json:"port"`
	Domains  string `json:"domains"`
	Strategy string `json:"strategy"`
	RSSKB    int    `json:"rss_kb"`
}

var reCiadpiPort = regexp.MustCompile(`-p\s+(\d+)`)

func ciadpiProfiles() []ciadpiProfile {
	portDomains := map[int]string{}
	for _, g := range readCiadpiGroups() {
		if g.Port != nil {
			portDomains[*g.Port] = strings.Join(g.Domains, ", ")
		}
	}
	out, _ := exec.Command("pgrep", "-a", "ciadpi").Output()
	var ps []ciadpiProfile
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln == "" {
			continue
		}
		fields := strings.SplitN(ln, " ", 2)
		pid, _ := strconv.Atoi(fields[0])
		cmd := ""
		if len(fields) > 1 {
			cmd = fields[1]
		}
		port := 0
		if m := reCiadpiPort.FindStringSubmatch(cmd); m != nil {
			port, _ = strconv.Atoi(m[1])
		}
		strat := cmd
		if i := strings.Index(cmd, "-E"); i >= 0 {
			strat = strings.TrimSpace(cmd[i+2:])
		}
		rss := 0
		if out2, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output(); err == nil {
			rss, _ = strconv.Atoi(strings.TrimSpace(string(out2)))
		}
		ps = append(ps, ciadpiProfile{PID: pid, Port: port, Domains: portDomains[port], Strategy: strat, RSSKB: rss})
	}
	return ps
}

// brainGroupSummary — одна группа для карточки «Мозг» (T-consolidate, 2026-07-23):
// не расписываем 113 доменов в одну строку (было в profiles/service — нечитаемо),
// даём count + сам список отдельно (UI разворачивает по клику). Engine различает
// zapret (Queue — очередь nfqws) от ciadpi (Port — REDIRECT-порт, T-ciadpi) —
// раньше ciadpi-группы тут не показывались вообще (brain-services-ciadpi.json
// не читался), из-за чего в UI их не было видно нигде, только vps/zapret.
type brainGroupSummary struct {
	GroupID string   `json:"group_id"`
	Engine  string   `json:"engine"`
	Proto   string   `json:"proto"`
	Queue   *int     `json:"queue,omitempty"`
	Port    *int     `json:"port,omitempty"`
	Count   int      `json:"count"`
	Domains []string `json:"domains"`
}

func brainGroupSummaries() []brainGroupSummary {
	out := []brainGroupSummary{}
	for _, g := range readBrainGroups() {
		out = append(out, brainGroupSummary{GroupID: g.GroupID, Engine: "zapret", Proto: g.Proto, Queue: g.Queue, Count: len(g.Domains), Domains: g.Domains})
	}
	for _, g := range readCiadpiGroups() {
		out = append(out, brainGroupSummary{GroupID: g.GroupID, Engine: "ciadpi", Proto: g.Proto, Port: g.Port, Count: len(g.Domains), Domains: g.Domains})
	}
	for _, g := range readZapret2Groups() {
		out = append(out, brainGroupSummary{GroupID: g.GroupID, Engine: "zapret2", Proto: g.Proto, Queue: g.Queue, Count: len(g.Domains), Domains: g.Domains})
	}
	return out
}

// readZapret2Groups — группы zapret2-адаптера (T-zapret2, brain-apply.sh), тот же
// формат, что у zapret1 (queue, не port — NFQUEUE, не REDIRECT), файл отдельный.
func readZapret2Groups() []brainGroup {
	data, err := os.ReadFile("/etc/gateway/brain-services-zapret2.json")
	if err != nil {
		return nil
	}
	var raw []brainGroup
	json.Unmarshal(data, &raw)
	return raw
}

// ciadpiGroup — группа ciadpi-адаптера (T-ciadpi, brain-apply.sh), формат
// /etc/gateway/brain-services-ciadpi.json: {group_id, proto, strategy, port, domains}.
type ciadpiGroup struct {
	GroupID string   `json:"group_id"`
	Proto   string   `json:"proto"`
	Port    *int     `json:"port"`
	Domains []string `json:"domains"`
}

func readCiadpiGroups() []ciadpiGroup {
	data, err := os.ReadFile("/etc/gateway/brain-services-ciadpi.json")
	if err != nil {
		return nil
	}
	var raw []ciadpiGroup
	json.Unmarshal(data, &raw)
	return raw
}

type brainTotalsInfo struct {
	Groups   int     `json:"groups"`
	Domains  int     `json:"domains"`
	Daemons  int     `json:"daemons"`
	MemoryMB float64 `json:"memory_mb"`
}

func brainTotals() brainTotalsInfo {
	groups := readBrainGroups()
	cgroups := readCiadpiGroups()
	zgroups := readZapret2Groups()
	domains := 0
	for _, g := range groups {
		domains += len(g.Domains)
	}
	for _, g := range cgroups {
		domains += len(g.Domains)
	}
	for _, g := range zgroups {
		domains += len(g.Domains)
	}
	// -x (точное совпадение имени процесса) обязателен: без него "nfqws" по
	// подстроке матчит и nfqws2, задваивая счётчик демонов zapret1.
	daemons, _ := runCmd("bash", "-c", "pgrep -c -x nfqws")
	cdaemons, _ := runCmd("bash", "-c", "pgrep -c ciadpi")
	zdaemons, _ := runCmd("bash", "-c", "pgrep -c -x nfqws2")
	mem, _ := runCmd("bash", "-c", `ps -o rss= -C nfqws | awk '{s+=$1} END{printf "%.1f", s/1024}'`)
	cmem, _ := runCmd("bash", "-c", `ps -o rss= -C ciadpi | awk '{s+=$1} END{printf "%.1f", s/1024}'`)
	zmem, _ := runCmd("bash", "-c", `ps -o rss= -C nfqws2 | awk '{s+=$1} END{printf "%.1f", s/1024}'`)
	n, _ := strconv.Atoi(strings.TrimSpace(daemons))
	cn, _ := strconv.Atoi(strings.TrimSpace(cdaemons))
	zn, _ := strconv.Atoi(strings.TrimSpace(zdaemons))
	m, _ := strconv.ParseFloat(strings.TrimSpace(mem), 64)
	cm, _ := strconv.ParseFloat(strings.TrimSpace(cmem), 64)
	zm, _ := strconv.ParseFloat(strings.TrimSpace(zmem), 64)
	return brainTotalsInfo{Groups: len(groups) + len(cgroups) + len(zgroups), Domains: domains, Daemons: n + cn + zn, MemoryMB: m + cm + zm}
}

// acceptOnlyEntities — сущности мозга без очереди/nfqws (T57: UDP-домен, которому
// хватило снять наш собственный DROP, десинхронизация не нужна). nfqProfiles их не
// видит (нет процесса) — отдельная секция в Мониторе.
type acceptOnlyEntity struct {
	Domain string `json:"domain"`
	Proto  string `json:"proto"`
}

// brainGroup — одна ГРУППА доменов с общей стратегией (T-consolidate, 2026-07-23:
// схема сменилась с "сущность на домен" на "сущность на группу", см. CANON).
type brainGroup struct {
	GroupID string   `json:"group_id"`
	Proto   string   `json:"proto"`
	Queue   *int     `json:"queue"`
	Domains []string `json:"domains"`
}

func readBrainGroups() []brainGroup {
	data, err := os.ReadFile("/etc/gateway/brain-services.json")
	if err != nil {
		return nil
	}
	var raw []brainGroup
	json.Unmarshal(data, &raw)
	return raw
}

func acceptOnlyEntities() []acceptOnlyEntity {
	out := []acceptOnlyEntity{}
	for _, g := range readBrainGroups() {
		if g.Queue != nil {
			continue
		}
		for _, d := range g.Domains {
			out = append(out, acceptOnlyEntity{Domain: d, Proto: g.Proto})
		}
	}
	return out
}

// brainQueues — карта очередь->домены (через запятую) из состояния мозга группами
// (сущности без hostlist скоупятся ipset'ом, их имя монитору иначе не видно).
func brainQueues() map[int]string {
	m := map[int]string{}
	for _, g := range readBrainGroups() {
		if g.Queue == nil || *g.Queue <= 0 {
			continue // accept-only (T57, queue=null в JSON) — нет nfqws-процесса, нечего подписывать
		}
		m[*g.Queue] = strings.Join(g.Domains, ", ")
	}
	return m
}

// nfqProfiles — распарсить `pgrep -a nfqws`, разложив мультипрофиль (--new) по
// сервисам: одна строка = один профиль (сервис на очереди со своей стратегией).
func nfqProfiles() []nfqProfile {
	brain := brainQueues()
	out, _ := exec.Command("pgrep", "-a", "nfqws").Output()
	var ps []nfqProfile
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln == "" {
			continue
		}
		fields := strings.SplitN(ln, " ", 2)
		pid, _ := strconv.Atoi(fields[0])
		cmd := ""
		if len(fields) > 1 {
			cmd = fields[1]
		}
		qnum := 0
		if m := reQnum.FindStringSubmatch(cmd); m != nil {
			qnum, _ = strconv.Atoi(m[1])
		}
		// профили разделены " --new "
		for _, seg := range strings.Split(cmd, "--new") {
			p := nfqProfile{PID: pid, Qnum: qnum, Service: "весь трафик"}
			if m := reFilterT.FindStringSubmatch(seg); m != nil {
				p.Proto, p.Ports = "tcp", m[1]
			}
			if m := reFilterU.FindStringSubmatch(seg); m != nil {
				p.Proto, p.Ports = "udp", m[1]
			}
			if p.Proto == "" {
				continue // не профиль (напр. хвост)
			}
			if m := reL7.FindStringSubmatch(seg); m != nil {
				p.L7 = m[1]
			}
			if m := reHost.FindStringSubmatch(seg); m != nil {
				parts := strings.Split(m[1], "/")
				p.Service = strings.TrimSuffix(parts[len(parts)-1], ".txt")
			}
			if i := strings.Index(seg, "--dpi-desync="); i >= 0 {
				st := reFakePath.ReplaceAllString(seg[i:], "")
				st = strings.ReplaceAll(st, "--dpi-desync=", "")
				st = strings.ReplaceAll(st, "--dpi-desync-", "")
				p.Strategy = strings.Join(strings.Fields(st), " ")
			}
			// сущность мозга (скоуп по ipset, без hostlist) — имя из состояния
			if d, ok := brain[qnum]; ok {
				p.Service = d + " ⚙"
			}
			ps = append(ps, p)
		}
	}
	return ps
}

// queueStats — /proc/net/netfilter/nfnetlink_queue + счётчики пакетов из iptables.
func queueStats() []queueStat {
	stats := map[int]*queueStat{}
	if f, err := os.Open("/proc/net/netfilter/nfnetlink_queue"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			c := strings.Fields(sc.Text())
			if len(c) < 8 {
				continue
			}
			q, _ := strconv.Atoi(c[0])
			queued, _ := strconv.Atoi(c[2])
			drop, _ := strconv.Atoi(c[5])
			udrop, _ := strconv.Atoi(c[6])
			stats[q] = &queueStat{Qnum: q, Queued: queued, Dropped: drop, UserDrop: udrop}
		}
	}
	// пакеты из iptables (mangle POSTROUTING + PREROUTING)
	pkts := iptablesQueuePackets()
	for q, p := range pkts {
		if stats[q] == nil {
			stats[q] = &queueStat{Qnum: q}
		}
		stats[q].Packets = p
	}
	var out []queueStat
	for _, v := range stats {
		out = append(out, *v)
	}
	return out
}

func iptablesQueuePackets() map[int]int {
	res := map[int]int{}
	for _, chain := range []string{"POSTROUTING", "PREROUTING"} {
		out, _ := exec.Command("iptables", "-t", "mangle", "-L", chain, "-n", "-v", "-x").Output()
		for _, ln := range strings.Split(string(out), "\n") {
			if !strings.Contains(ln, "NFQUEUE num") {
				continue
			}
			f := strings.Fields(ln)
			if len(f) < 2 {
				continue
			}
			pk, _ := strconv.Atoi(f[0])
			if m := regexp.MustCompile(`NFQUEUE num (\d+)`).FindStringSubmatch(ln); m != nil {
				q, _ := strconv.Atoi(m[1])
				res[q] += pk
			}
		}
	}
	return res
}

func svcList(s *server) []svcInfo {
	data, err := os.ReadFile(s.servicesFile)
	if err != nil {
		return nil
	}
	var raw []struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Mode     string   `json:"mode"`
		Domains  []string `json:"domains"`
		Channels []struct {
			Proto string `json:"proto"`
			Ports string `json:"ports"`
			L7    string `json:"l7"`
		} `json:"channels"`
	}
	json.Unmarshal(data, &raw)
	var out []svcInfo
	for _, r := range raw {
		mode := r.Mode
		if mode == "" {
			mode = "zapret"
		}
		var chans []string
		for _, c := range r.Channels {
			s := c.Proto + ":" + c.Ports
			if c.L7 != "" {
				s += " (" + c.L7 + ")"
			}
			chans = append(chans, s)
		}
		out = append(out, svcInfo{ID: r.ID, Name: r.Name, Mode: mode, Domains: len(r.Domains), Channels: chans})
	}
	return out
}
