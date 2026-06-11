package main

import (
	"net/http"
	"os"
	"strings"
)

// Zapret (T20): показать реально работающие стратегии DPI-обхода и домены под них.
// Источник истины — то, что РЕАЛЬНО запущено: командные строки nfqws (pgrep -a).
// Внутри одного процесса стратегии разделены флагом --new. Только чтение.

type zStrategy struct {
	Queue   string   `json:"queue"`
	Proto   string   `json:"proto"`   // tcp / udp
	Ports   string   `json:"ports"`   // из --filter-tcp/udp
	L7      string   `json:"l7"`      // из --filter-l7 (discord,stun)
	Desync  string   `json:"desync"`  // --dpi-desync=...
	Repeats string   `json:"repeats"` // --dpi-desync-repeats
	Fooling string   `json:"fooling"` // --dpi-desync-fooling
	List    string   `json:"list"`    // имя hostlist-файла
	Domains []string `json:"domains"` // домены из hostlist
}

func (s *server) handleZapret(w http.ResponseWriter, r *http.Request) {
	out, _ := runCmd("bash", "-c", "pgrep -a nfqws")
	if strings.TrimSpace(out) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"running": false, "strategies": []zStrategy{}})
		return
	}
	var strategies []zStrategy
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// fields[0] = PID, дальше команда; найдём общий --qnum
		queue := flagVal(fields, "--qnum")
		// разбить по --new на сегменты-стратегии
		for _, seg := range splitByNew(fields) {
			st := zStrategy{Queue: queue}
			if v := flagVal(seg, "--filter-tcp"); v != "" {
				st.Proto, st.Ports = "tcp", v
			} else if v := flagVal(seg, "--filter-udp"); v != "" {
				st.Proto, st.Ports = "udp", v
			} else {
				continue // не сегмент стратегии (напр. начало с --daemon без фильтра)
			}
			st.L7 = flagVal(seg, "--filter-l7")
			st.Desync = flagVal(seg, "--dpi-desync")
			st.Repeats = flagVal(seg, "--dpi-desync-repeats")
			st.Fooling = flagVal(seg, "--dpi-desync-fooling")
			if hl := flagVal(seg, "--hostlist"); hl != "" {
				st.List = baseName(hl)
				st.Domains = readLines(hl)
			}
			strategies = append(strategies, st)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"running": true, "strategies": strategies})
}

// flagVal ищет токен вида --key=value и возвращает value.
func flagVal(tokens []string, key string) string {
	p := key + "="
	for _, t := range tokens {
		if strings.HasPrefix(t, p) {
			return strings.TrimPrefix(t, p)
		}
	}
	return ""
}

// splitByNew режет список токенов на сегменты по разделителю --new.
func splitByNew(tokens []string) [][]string {
	var segs [][]string
	cur := []string{}
	for _, t := range tokens {
		if t == "--new" {
			segs = append(segs, cur)
			cur = []string{}
			continue
		}
		cur = append(cur, t)
	}
	segs = append(segs, cur)
	return segs
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func readLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}
