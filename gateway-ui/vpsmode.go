package main

import (
	"net/http"
	"os"
	"strings"
)

// Режим работы: "VPS+zapret" (обычный) или "только zapret" (без VPS-туннеля —
// для тех, у кого нет VPS, или кто хочет временно отключить его). Скрипт —
// iptables/vps-mode.sh, снимает/возвращает REDIRECT LAN 80/443 -> xray и
// авто-обход -> xray. brain-apply.sh's has_vps() читает тот же файл состояния,
// чтобы не создавать автообход в никуда, пока режим off.
const vpsModeStateFile = "/etc/gateway/vps-mode.conf"

var vpsModeValid = map[string]bool{"on": true, "off": true}

func readVPSMode() string {
	b, err := os.ReadFile(vpsModeStateFile)
	if err != nil {
		return "on"
	}
	m := strings.TrimSpace(string(b))
	if !vpsModeValid[m] {
		return "on"
	}
	return m
}

func (s *server) handleVPSMode(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"mode": readVPSMode()})

	case http.MethodPost:
		mode := r.FormValue("mode")
		if !vpsModeValid[mode] {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mode: on|off"})
			return
		}
		if out, err := runCmd("/opt/gateway/vps-mode.sh", mode); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "output": out})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": readVPSMode()})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "GET|POST"})
	}
}
