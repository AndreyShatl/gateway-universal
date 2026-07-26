package main

import (
	"net/http"
	"os"
	"strings"
)

// Game Mode — бланкет-ACCEPT для эфемерных портов игровых серверов, применяется
// через iptables/game-mode.sh (T-gamemode). Состояние — одно слово в
// /etc/gateway/game-mode.conf, читаемое и самим скриптом (restore на боевую) и UI.
const gameModeStateFile = "/etc/gateway/game-mode.conf"

var gameModeValid = map[string]bool{"off": true, "tcp": true, "udp": true, "both": true}

func readGameMode() string {
	b, err := os.ReadFile(gameModeStateFile)
	if err != nil {
		return "off"
	}
	m := strings.TrimSpace(string(b))
	if !gameModeValid[m] {
		return "off"
	}
	return m
}

func (s *server) handleGameMode(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"mode": readGameMode()})

	case http.MethodPost:
		mode := r.FormValue("mode")
		if !gameModeValid[mode] {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mode: off|tcp|udp|both"})
			return
		}
		if out, err := runCmd("/opt/gateway/game-mode.sh", mode); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "output": out})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": readGameMode()})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "GET|POST"})
	}
}
