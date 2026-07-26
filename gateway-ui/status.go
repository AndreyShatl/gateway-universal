package main

import (
	"net/http"
	"strconv"
	"strings"
)

// Статус и управление (T13). Всё через exec существующих средств:
// systemctl, pgrep, curl, tests/smoke.sh, journalctl. UI ничего не дублирует.

// В обзоре — только значимые сервисы. fix-gateway/discord-tproxy — фоновая
// инфраструктура, их статус в обзоре не нужен.
var statusServices = []string{"xray", "zapret", "gateway-ui"}

// сервисы, которые разрешено рестартить/смотреть из UI
var manageable = map[string]bool{"xray": true, "zapret": true, "fix-gateway": true, "discord-tproxy": true,
	"gateway-detector": true, "gateway-brain-worker": true}

// gateway-brain-worker пишет подробный лог (per-domain ✅/🔵/⚠) не в stdout/journalctl,
// а в свой файл — journalctl этого юнита показал бы только "воркер запущен" и тишину.
const brainLogFile = "/var/log/gateway-brain.log"

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	svc := map[string]string{}
	for _, name := range statusServices {
		out, _ := runCmd("systemctl", "is-active", name+".service")
		svc[name] = out
	}
	nfqws, _ := runCmd("bash", "-c", "pgrep -x nfqws | wc -l")
	writeJSON(w, http.StatusOK, map[string]any{
		"services": svc,
		"nfqws":    strings.TrimSpace(nfqws),
	})
}

func (s *server) handleExitIP(w http.ResponseWriter, r *http.Request) {
	provider, _ := runCmd("curl", "-s", "--max-time", "6", "https://api.ipify.org")
	vps, _ := runCmd("bash", "-c",
		"curl -s --max-time 8 --socks5-hostname 127.0.0.1:1080 https://www.cloudflare.com/cdn-cgi/trace | sed -n 's/^ip=//p'")
	writeJSON(w, http.StatusOK, map[string]any{
		"provider": strings.TrimSpace(provider),
		"vps":      strings.TrimSpace(vps),
	})
}

func (s *server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	svc := r.FormValue("service")
	if !manageable[svc] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "сервис не разрешён"})
		return
	}
	out, err := runCmd("systemctl", "restart", svc+".service")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "output": out})
		return
	}
	if svc == "zapret" {
		// CANON #20: zapret.service флашит mangle POSTROUTING при каждом рестарте —
		// без restore все brain-группы (T-consolidate) осиротеют молча.
		runCmd("sleep", "2")
		runCmd("/opt/gateway-brain/brain-apply.sh", "restore")
	}
	state, _ := runCmd("systemctl", "is-active", svc+".service")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": svc, "state": strings.TrimSpace(state)})
}

func (s *server) handleSmoke(w http.ResponseWriter, r *http.Request) {
	out, err := s.runScript("tests/smoke.sh")
	writeJSON(w, http.StatusOK, map[string]any{"pass": err == nil, "output": out})
}

func (s *server) handleLogs(w http.ResponseWriter, r *http.Request) {
	svc := r.URL.Query().Get("service")
	if !manageable[svc] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "сервис не разрешён"})
		return
	}
	n := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("lines")); err == nil && v > 0 && v <= 200 {
		n = v
	}
	var out string
	if svc == "gateway-brain-worker" {
		out, _ = runCmd("tail", "-n", strconv.Itoa(n), brainLogFile)
	} else {
		out, _ = runCmd("journalctl", "-u", svc+".service", "-n", strconv.Itoa(n), "--no-pager")
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": out})
}
