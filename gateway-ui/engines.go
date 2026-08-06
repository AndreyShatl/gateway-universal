// Package main: движки ciadpi (hufrea/byedpi) и zapret2 (bol-van/zapret2) —
// версия + ручное обновление из UI (T-engines-ui, 2026-08-03). Оба движка не
// имеют одного системного демона (динамические per-группа transient-юниты,
// см. brain-apply.sh), поэтому обновление — не "git+make+systemctl restart"
// напрямую в хендлере (как у zapret, см. zapret.go), а вызов уже существующих
// и более безопасных скриптов *-auto-update.sh (останавливают активные группы
// перед пересборкой, откатываются на предыдущий commit при неудачной сборке —
// у zapret-обновления в этом файле такой защиты нет, это осознанно не трогали:
// ниже риск, т.к. движки почти всегда неактивны в момент обновления по расписанию).
package main

import (
	"net/http"
	"strings"
)

const (
	ciadpiDir           = "/opt/byedpi"
	ciadpiUpdateScript  = "/opt/gateway-brain/ciadpi-auto-update.sh"
	ciadpiUpdateLog     = "/var/log/gateway-ciadpi-update.log"
	ciadpiUpdateUnit    = "gateway-ciadpi-update-manual"
	zapret2Dir          = "/opt/zapret2"
	zapret2UpdateScript = "/opt/gateway-brain/zapret2-auto-update.sh"
	zapret2UpdateLog    = "/var/log/gateway-zapret2-update.log"
	zapret2UpdateUnit   = "gateway-zapret2-update-manual"
)

func gitVersionInfo(dir string) (commit, desc string) {
	c, _ := runCmd("git", "-C", dir, "rev-parse", "--short", "HEAD")
	d, _ := runCmd("git", "-C", dir, "log", "-1", "--format=%cs %s", "HEAD")
	return strings.TrimSpace(c), strings.TrimSpace(d)
}

func (s *server) handleCiadpiVersion(w http.ResponseWriter, r *http.Request) {
	commit, desc := gitVersionInfo(ciadpiDir)
	updating := false
	if out, _ := runCmd("systemctl", "is-active", ciadpiUpdateUnit); strings.TrimSpace(out) == "active" {
		updating = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"commit": commit, "desc": desc, "updating": updating,
		"log_tail": tailFile(ciadpiUpdateLog, 40),
	})
}

// handleCiadpiUpdate — POST: запускает уже существующий scripts/ciadpi-auto-update.sh
// в фоне (тот же путь, что и еженедельный таймер) — останавливает активные
// ciadpi-группы, git fetch+reset, make, restore-ciadpi; откатывается на
// предыдущий commit, если сборка не удалась.
func (s *server) handleCiadpiUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if out, _ := runCmd("systemctl", "is-active", ciadpiUpdateUnit); strings.TrimSpace(out) == "active" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "обновление уже идёт"})
		return
	}
	runCmd("systemctl", "reset-failed", ciadpiUpdateUnit)
	if out, err := runCmd("systemd-run", "--unit="+ciadpiUpdateUnit, "--collect", "/bin/bash", ciadpiUpdateScript); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "systemd-run: " + err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleZapret2Version(w http.ResponseWriter, r *http.Request) {
	commit, desc := gitVersionInfo(zapret2Dir)
	updating := false
	if out, _ := runCmd("systemctl", "is-active", zapret2UpdateUnit); strings.TrimSpace(out) == "active" {
		updating = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"commit": commit, "desc": desc, "updating": updating,
		"log_tail": tailFile(zapret2UpdateLog, 40),
	})
}

// handleZapret2Update — POST, тот же принцип, что и handleCiadpiUpdate, только
// для zapret2 (scripts/zapret2-auto-update.sh).
func (s *server) handleZapret2Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if out, _ := runCmd("systemctl", "is-active", zapret2UpdateUnit); strings.TrimSpace(out) == "active" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "обновление уже идёт"})
		return
	}
	runCmd("systemctl", "reset-failed", zapret2UpdateUnit)
	if out, err := runCmd("systemd-run", "--unit="+zapret2UpdateUnit, "--collect", "/bin/bash", zapret2UpdateScript); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "systemd-run: " + err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
