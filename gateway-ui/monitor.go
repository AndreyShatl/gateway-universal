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

type nfqDaemon struct {
	PID      int    `json:"pid"`
	Qnum     int    `json:"qnum"`
	Proto    string `json:"proto"`    // tcp | udp
	Filter   string `json:"filter"`   // порты/фильтр
	Strategy string `json:"strategy"` // --dpi-desync… кратко
	Hostlist string `json:"hostlist"`
}

type queueStat struct {
	Qnum       int `json:"qnum"`
	Queued     int `json:"queued"`      // сейчас в очереди
	Dropped    int `json:"dropped"`     // дропнуто (очередь полна)
	UserDrop   int `json:"user_dropped"`
	Packets    int `json:"packets"`     // всего прошло (из iptables)
}

type svcInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Mode    string `json:"mode"` // zapret | vps
	Domains int    `json:"domains"`
}

var (
	reQnum   = regexp.MustCompile(`--qnum=(\d+)`)
	reFilterT = regexp.MustCompile(`--filter-tcp=(\S+)`)
	reFilterU = regexp.MustCompile(`--filter-udp=(\S+)`)
	reDesync = regexp.MustCompile(`--dpi-desync=(\S+)`)
	reHost   = regexp.MustCompile(`--hostlist=(\S+)`)
)

func (s *server) handleMonitor(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"daemons":  nfqDaemons(),
		"queues":   queueStats(),
		"services": svcList(s),
	})
}

// nfqDaemons — распарсить `pgrep -a nfqws`.
func nfqDaemons() []nfqDaemon {
	out, _ := exec.Command("pgrep", "-a", "nfqws").Output()
	var ds []nfqDaemon
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
		d := nfqDaemon{PID: pid}
		if m := reQnum.FindStringSubmatch(cmd); m != nil {
			d.Qnum, _ = strconv.Atoi(m[1])
		}
		if m := reFilterT.FindStringSubmatch(cmd); m != nil {
			d.Proto, d.Filter = "tcp", m[1]
		}
		if m := reFilterU.FindStringSubmatch(cmd); m != nil {
			d.Proto, d.Filter = "udp", m[1]
		}
		// стратегия = всё от --dpi-desync до конца (кратко), без путей fake-файлов
		if i := strings.Index(cmd, "--dpi-desync="); i >= 0 {
			st := cmd[i:]
			st = regexp.MustCompile(`\s--dpi-desync-fake-\S+=\S+`).ReplaceAllString(st, "")
			st = strings.ReplaceAll(st, "--dpi-desync=", "")
			st = strings.ReplaceAll(st, "--dpi-desync-", "")
			d.Strategy = strings.TrimSpace(st)
		}
		if m := reDesync.FindStringSubmatch(cmd); m != nil && d.Strategy == "" {
			d.Strategy = m[1]
		}
		if m := reHost.FindStringSubmatch(cmd); m != nil {
			parts := strings.Split(m[1], "/")
			d.Hostlist = parts[len(parts)-1]
		}
		ds = append(ds, d)
	}
	return ds
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
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Mode    string   `json:"mode"`
		Domains []string `json:"domains"`
	}
	json.Unmarshal(data, &raw)
	var out []svcInfo
	for _, r := range raw {
		mode := r.Mode
		if mode == "" {
			mode = "zapret"
		}
		out = append(out, svcInfo{ID: r.ID, Name: r.Name, Mode: mode, Domains: len(r.Domains)})
	}
	return out
}
