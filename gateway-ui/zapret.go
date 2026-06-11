package main

import (
	"net/http"
	"os"
	"path/filepath"
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

// Блоки стратегий zapret.sh: id → переменная DESYNC_<VAR> + калиброванный дефолт.
// Оверрайды живут в /etc/gateway/zapret-overrides.env (DESYNC_<VAR>="...").
type zapBlock struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Varname string `json:"var"`
	Default string `json:"default"`
}

var zapBlocks = []zapBlock{
	{"tcp_instagram", "Instagram (TCP)", "DESYNC_TCP_INSTAGRAM", `--dpi-desync=fake,multidisorder --dpi-desync-split-pos=1,midsld --dpi-desync-repeats=11 --dpi-desync-fooling=md5sig,badseq --dpi-desync-autottl=2:2-12 $FAKE_TLS`},
	{"tcp_discord", "Discord (TCP)", "DESYNC_TCP_DISCORD", `--dpi-desync=fake,fakedsplit --dpi-desync-repeats=6 --dpi-desync-fooling=ts $FAKE_TLS`},
	{"tcp_general", "General (TCP)", "DESYNC_TCP_GENERAL", `--dpi-desync=fake,fakedsplit --dpi-desync-repeats=6 --dpi-desync-fooling=ts $FAKE_TLS`},
	{"tcp_youtube", "YouTube (TCP)", "DESYNC_TCP_YOUTUBE", `--dpi-desync=fake,fakedsplit --dpi-desync-repeats=6 --dpi-desync-fooling=ts`},
	{"udp_instagram", "Instagram (UDP/QUIC)", "DESYNC_UDP_INSTAGRAM", `--dpi-desync=fake --dpi-desync-repeats=6 $FAKE_QUIC`},
	{"udp_general", "General (UDP/QUIC)", "DESYNC_UDP_GENERAL", `--dpi-desync=fake --dpi-desync-repeats=6 $FAKE_QUIC`},
	{"udp_discord", "Discord voice (UDP)", "DESYNC_UDP_DISCORD", `--dpi-desync=fake --dpi-desync-repeats=6`},
}

func zapBlockByID(id string) *zapBlock {
	for i := range zapBlocks {
		if zapBlocks[i].ID == id {
			return &zapBlocks[i]
		}
	}
	return nil
}

// handleStrategies — GET: блоки с дефолтом и текущим оверрайдом (если есть).
func (s *server) handleStrategies(w http.ResponseWriter, r *http.Request) {
	type row struct {
		zapBlock
		Override string `json:"override"`
	}
	out := []row{}
	for _, b := range zapBlocks {
		ov, _ := readConfigVar(s.overrides, b.Varname)
		out = append(out, row{b, ov})
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocks": out})
}

// handleStrategySet — POST id + desync: записать оверрайд блока и рестартить zapret.
// action=reset убирает оверрайд (возврат к калиброванному дефолту).
func (s *server) handleStrategySet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	b := zapBlockByID(r.FormValue("id"))
	if b == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "неизвестный блок"})
		return
	}
	var err error
	if r.FormValue("action") == "reset" {
		err = removeConfigVar(s.overrides, b.Varname)
	} else {
		desync := strings.TrimSpace(r.FormValue("desync"))
		if desync == "" || !strings.Contains(desync, "--dpi-desync") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "пустая или некорректная стратегия (нужны --dpi-desync…)"})
			return
		}
		err = upsertConfigVar(s.overrides, b.Varname, desync)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if out, err := runCmd("systemctl", "restart", "zapret.service"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "рестарт zapret: " + err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

const zupdateUnit = "gateway-zupdate"

// handleZapretVersion — GET текущий коммит /opt/zapret + статус обновления.
func (s *server) handleZapretVersion(w http.ResponseWriter, r *http.Request) {
	zb := filepath.Dir(s.blockcheck)
	commit, _ := runCmd("git", "-C", zb, "rev-parse", "--short", "HEAD")
	desc, _ := runCmd("git", "-C", zb, "log", "-1", "--format=%cs %s", "HEAD")
	updating := false
	if out, _ := runCmd("systemctl", "is-active", zupdateUnit); strings.TrimSpace(out) == "active" {
		updating = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"commit":   strings.TrimSpace(commit),
		"desc":     strings.TrimSpace(desc),
		"updating": updating,
		"log_tail": tailFile(s.zupdateLog(), 40),
	})
}

func (s *server) zupdateLog() string { return filepath.Join(filepath.Dir(s.scanDir), "zupdate.log") }

// handleZapretUpdate — POST: git fetch+reset апстрима + пересборка + рестарт zapret, в фоне.
func (s *server) handleZapretUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if out, _ := runCmd("systemctl", "is-active", zupdateUnit); strings.TrimSpace(out) == "active" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "обновление уже идёт"})
		return
	}
	zb := filepath.Dir(s.blockcheck)
	sh := "#!/bin/bash\n" +
		"exec > " + shq(s.zupdateLog()) + " 2>&1\n" +
		"set -x\n" +
		"cd " + shq(zb) + " || exit 1\n" +
		"git fetch --depth 1 origin && git reset --hard FETCH_HEAD || exit 1\n" +
		"make -C nfq && make -C mdig && make -C tpws\n" +
		"systemctl restart zapret.service\n" +
		"echo __ZUPDATE_DONE__ rc=$?\n"
	scriptPath := filepath.Join(filepath.Dir(s.scanDir), "zupdate.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := os.WriteFile(scriptPath, []byte(sh), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	runCmd("systemctl", "reset-failed", zupdateUnit)
	if out, err := runCmd("systemd-run", "--unit="+zupdateUnit, "--collect", "/bin/bash", scriptPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "systemd-run: " + err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
