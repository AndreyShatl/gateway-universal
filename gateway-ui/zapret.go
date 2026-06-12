package main

import (
	"encoding/json"
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
// Сервис zapret: имя, домены (inline) и каналы (tcp/udp с портами+desync).
type zChannel struct {
	Proto  string `json:"proto"`
	Ports  string `json:"ports"`
	L7     string `json:"l7,omitempty"`
	Desync string `json:"desync"`
}
type zService struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Domains  []string   `json:"domains"`
	Channels []zChannel `json:"channels"`
}

func (s *server) readServices() ([]zService, error) {
	data, err := os.ReadFile(s.servicesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []zService{}, nil
		}
		return nil, err
	}
	var svc []zService
	if err := json.Unmarshal(data, &svc); err != nil {
		return nil, err
	}
	return svc, nil
}

// handleServices — GET список сервисов; POST — заменить весь список и рестартить zapret.
func (s *server) handleServices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		svc, err := s.readServices()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"services": svc})

	case http.MethodPost:
		var svc []zService
		if err := json.NewDecoder(r.Body).Decode(&svc); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "плохой JSON"})
			return
		}
		if msg := validateServices(svc); msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg})
			return
		}
		b, _ := json.MarshalIndent(svc, "", "  ")
		if err := os.MkdirAll(filepath.Dir(s.servicesFile), 0o755); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		tmp := s.servicesFile + ".tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		os.Rename(tmp, s.servicesFile)
		// Перегенерировать xray-конфиг: домены, отданные в zapret, исключаются из
		// VPS-роутинга (build-domains вычитает их). Затем рестарт xray и zapret.
		if out, err := s.runScript("xray/render-config.sh",
			"--template", filepath.Join(s.repoDir, "xray", "config.template.json"),
			"--out", s.xrayConfig, "--config", s.configEnv, "--xray", s.xrayBin,
			"--user-domains-dir", s.userDomainsDir); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "render xray: " + err.Error(), "output": out})
			return
		}
		runCmd("systemctl", "restart", "xray.service")
		if out, err := runCmd("systemctl", "restart", "zapret.service"); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "рестарт zapret: " + err.Error(), "output": out})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func validateServices(svc []zService) string {
	seen := map[string]bool{}
	for _, s := range svc {
		if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Name) == "" {
			return "у сервиса пустой id или имя"
		}
		if seen[s.ID] {
			return "повторяющийся id: " + s.ID
		}
		seen[s.ID] = true
		if len(s.Channels) == 0 {
			return s.Name + ": нет каналов (tcp/udp)"
		}
		for _, c := range s.Channels {
			if c.Proto != "tcp" && c.Proto != "udp" {
				return s.Name + ": proto должен быть tcp или udp"
			}
			if strings.TrimSpace(c.Ports) == "" {
				return s.Name + ": пустые порты"
			}
			if !strings.Contains(c.Desync, "--dpi-desync") {
				return s.Name + ": стратегия без --dpi-desync"
			}
		}
	}
	return ""
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
