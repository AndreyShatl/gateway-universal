package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Zapret: обновление движка (nfqws и т.д.) из апстрима bol-van/zapret.
// Ручное управление стратегиями/сервисами (featured youtube/discord/instagram,
// просмотр запущенных стратегий, поиск через blockcheck) убрано — этим теперь
// занимается "мозг" (T44-51, см. CANON), UI-переключалка стала не нужна.
// zapret-services.json как источник конфигурации для zapret.sh/render-config.sh
// не тронут — редактируется только руками (SSH), если вообще понадобится.

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
		// CANON #20: zapret.service флашит mangle POSTROUTING при каждом рестарте —
		// без restore все brain-группы (T-consolidate) осиротеют (демоны живы, правил нет).
		"sleep 2 && /opt/gateway-brain/brain-apply.sh restore\n" +
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

// shq — одинарное экранирование для вставки в bash-скрипт.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
