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
		"profiles":    nfqProfiles(),
		"queues":      queueStats(),
		"services":    svcList(s),
		"accept_only": acceptOnlyEntities(), // T57: UDP-сущности без десинхронизации — нет процесса, invisible в profiles
		"ver":         s.ver,                // авто-обновление вкладки после деплоя (checkVer в JS) — раньше это делал /api/scan
	})
}

// acceptOnlyEntities — сущности мозга без очереди/nfqws (T57: UDP-домен, которому
// хватило снять наш собственный DROP, десинхронизация не нужна). nfqProfiles их не
// видит (нет процесса) — отдельная секция в Мониторе.
type acceptOnlyEntity struct {
	Domain string `json:"domain"`
	Proto  string `json:"proto"`
}

func acceptOnlyEntities() []acceptOnlyEntity {
	data, err := os.ReadFile("/etc/gateway/brain-services.json")
	if err != nil {
		return nil
	}
	var raw []struct {
		Domain string `json:"domain"`
		Proto  string `json:"proto"`
		Queue  *int   `json:"queue"`
	}
	json.Unmarshal(data, &raw)
	out := []acceptOnlyEntity{}
	for _, x := range raw {
		if x.Queue == nil {
			out = append(out, acceptOnlyEntity{Domain: x.Domain, Proto: x.Proto})
		}
	}
	return out
}

// brainQueues — карта очередь->домен из состояния мозга (сущности без hostlist
// скоупятся ipset'ом, их имя монитору иначе не видно).
func brainQueues() map[int]string {
	m := map[int]string{}
	data, err := os.ReadFile("/etc/gateway/brain-services.json")
	if err != nil {
		return m
	}
	var raw []struct {
		Domain string `json:"domain"`
		Queue  int    `json:"queue"`
	}
	json.Unmarshal(data, &raw)
	for _, x := range raw {
		if x.Queue <= 0 {
			continue // accept-only (T57, queue=null в JSON) — нет nfqws-процесса, нечего подписывать
		}
		m[x.Queue] = x.Domain
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
