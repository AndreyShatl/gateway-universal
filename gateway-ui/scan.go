package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Поиск рабочих стратегий (T22): обёртка над /opt/zapret/blockcheck.sh.
// Долгий фон через systemd-run (unit gateway-scan), переживает рестарт UI.
// Состояние в <scanDir>/job.json, вывод в scan.log. Рабочие стратегии
// парсятся из лога по требованию. Изоляция: blockcheck в OUTPUT + QNUM 59780,
// живой zapret на POSTROUTING 200/201 — не пересекаются.

const scanUnit = "gateway-scan"
const scanQNUM = "59780"

type scanParams struct {
	Owner     string   `json:"owner"` // кто запустил: id сервиса или "zapret"
	Domains   []string `json:"domains"`
	HTTP      bool     `json:"http"`
	TLS12     bool     `json:"tls12"`
	TLS13     bool     `json:"tls13"`
	QUIC      bool     `json:"quic"`
	ScanLevel string   `json:"scanlevel"` // quick|standard|force
	Parallel  bool     `json:"parallel"`
}

type scanJob struct {
	Status  string     `json:"status"` // running|done|stopped|interrupted|failed
	Started string     `json:"started"`
	Params  scanParams `json:"params"`
	Unit    string     `json:"unit"`
}

func (s *server) jobPath() string { return filepath.Join(s.scanDir, "job.json") }
func (s *server) logPath() string { return filepath.Join(s.scanDir, "scan.log") }
func (s *server) runPath() string { return filepath.Join(s.scanDir, "run.sh") }

func (s *server) readJob() (scanJob, bool) {
	var j scanJob
	data, err := os.ReadFile(s.jobPath())
	if err != nil {
		return j, false
	}
	json.Unmarshal(data, &j)
	return j, true
}

func (s *server) writeJob(j scanJob) error {
	if err := os.MkdirAll(s.scanDir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(j, "", "  ")
	return os.WriteFile(s.jobPath(), b, 0o644)
}

func unitActive() bool {
	out, _ := runCmd("systemctl", "is-active", scanUnit)
	return strings.TrimSpace(out) == "active" || strings.TrimSpace(out) == "activating"
}

// --- preconditions: ip_forward + zapret active ------------------------

func (s *server) scanPreconditions() (bool, string) {
	fwd, _ := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if strings.TrimSpace(string(fwd)) != "1" {
		return false, "ip_forward выключен — машина не работает как шлюз"
	}
	if st, _ := runCmd("systemctl", "is-active", "zapret.service"); strings.TrimSpace(st) != "active" {
		return false, "zapret не активен"
	}
	return true, ""
}

// --- handlers ----------------------------------------------------------

func (s *server) handleScan(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.scanStatus(w)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) scanStatus(w http.ResponseWriter) {
	j, ok := s.readJob()
	active := unitActive()
	// сверка: job=running, а юнита нет → done/interrupted
	if ok && j.Status == "running" && !active {
		if logHas(s.logPath(), "__SCAN_DONE__") {
			j.Status = "done"
		} else {
			j.Status = "interrupted"
		}
		s.writeJob(j)
	}
	pre, preMsg := s.scanPreconditions()
	resp := map[string]any{
		"exists":          ok,
		"running":         active,
		"can_start":       pre && !active,
		"precondition_ok": pre,
		"precondition":    preMsg,
		"working":         parseWorking(s.logPath()),
		"log_tail":        tailFile(s.logPath(), 40),
	}
	if ok {
		resp["status"] = j.Status
		resp["started"] = j.Started
		resp["params"] = j.Params
		resp["owner"] = j.Params.Owner
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleScanStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ok, msg := s.scanPreconditions(); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "предусловия не выполнены: " + msg})
		return
	}
	if unitActive() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "поиск уже идёт"})
		return
	}
	var p scanParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "плохой JSON параметров"})
		return
	}
	if len(p.Domains) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "укажите хотя бы один домен"})
		return
	}
	if p.ScanLevel == "" {
		p.ScanLevel = "standard"
	}
	if err := s.writeRunScript(p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// старый юнит мог остаться в failed/inactive — сбросить, чтобы имя освободилось
	runCmd("systemctl", "reset-failed", scanUnit)
	if out, err := runCmd("systemd-run", "--unit="+scanUnit, "--collect", "/bin/bash", s.runPath()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "systemd-run: " + err.Error(), "output": out})
		return
	}
	s.writeJob(scanJob{Status: "running", Started: time.Now().Format(time.RFC3339), Params: p, Unit: scanUnit})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleScanStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runCmd("systemctl", "stop", scanUnit)
	if j, ok := s.readJob(); ok {
		j.Status = "stopped"
		s.writeJob(j)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// writeRunScript пишет run.sh, который выставляет env и запускает blockcheck.
func (s *server) writeRunScript(p scanParams) error {
	if err := os.MkdirAll(s.scanDir, 0o755); err != nil {
		return err
	}
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	level := p.ScanLevel
	if level != "quick" && level != "standard" && level != "force" {
		level = "standard"
	}
	zb := filepath.Dir(s.blockcheck) // база zapret (для prereq-бинарников)
	sh := "#!/bin/bash\n" +
		"exec > " + shq(s.logPath()) + " 2>&1\n" +
		// blockcheck требует mdig и tpws — собираем при отсутствии (install ставит только nfqws)
		"[ -x " + shq(zb+"/mdig/mdig") + " ] || make -C " + shq(zb+"/mdig") + "\n" +
		"[ -x " + shq(zb+"/tpws/tpws") + " ] || make -C " + shq(zb+"/tpws") + "\n" +
		"export DOMAINS=" + shq(strings.Join(p.Domains, " ")) + "\n" +
		"export IPVS=4\n" +
		"export ENABLE_HTTP=" + b(p.HTTP) + " ENABLE_HTTPS_TLS12=" + b(p.TLS12) +
		" ENABLE_HTTPS_TLS13=" + b(p.TLS13) + " ENABLE_HTTP3=" + b(p.QUIC) + "\n" +
		"export PARALLEL=" + b(p.Parallel) + " SCANLEVEL=" + shq(level) + " QNUM=" + scanQNUM + "\n" +
		"yes '' | " + shq(s.blockcheck) + "\n" +
		"echo \"__SCAN_DONE__ rc=$?\"\n"
	return os.WriteFile(s.runPath(), []byte(sh), 0o755)
}

// shq — простое одинарное квотирование для shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// --- парсинг лога ------------------------------------------------------

var reWorking = regexp.MustCompile(`working strategy found for ipv\d+ (\S+)\s*:\s*(.+?)\s*!!!!!`)

type workingStrategy struct {
	Domain   string `json:"domain"`
	Strategy string `json:"strategy"`
}

func parseWorking(path string) []workingStrategy {
	data, err := os.ReadFile(path)
	if err != nil {
		return []workingStrategy{}
	}
	seen := map[string]bool{}
	out := []workingStrategy{}
	for _, m := range reWorking.FindAllStringSubmatch(string(data), -1) {
		ws := workingStrategy{Domain: m[1], Strategy: strings.TrimSpace(m[2])}
		key := ws.Domain + "|" + ws.Strategy
		if !seen[key] {
			seen[key] = true
			out = append(out, ws)
		}
	}
	return out
}

func logHas(path, needle string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), needle)
}

func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
