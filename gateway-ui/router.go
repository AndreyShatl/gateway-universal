package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// handleRouterIP — GET текущий ROUTER_IP, POST — задать новый (T11).
// Источник истины — config.env; применение — systemd/apply-fix-gateway.sh
// (тот же скрипт, что зовёт install.sh — логика не дублируется).
func (s *server) handleRouterIP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ip, _ := readConfigVar(s.configEnv, "ROUTER_IP")
		writeJSON(w, http.StatusOK, map[string]any{"router_ip": ip})

	case http.MethodPost:
		ip := strings.TrimSpace(r.FormValue("router_ip"))
		if p := net.ParseIP(ip); p == nil || p.To4() == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "нужен корректный IPv4"})
			return
		}
		if err := writeConfigVar(s.configEnv, "ROUTER_IP", ip); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "запись config.env: " + err.Error()})
			return
		}
		out, err := s.runScript("systemd/apply-fix-gateway.sh", ip)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "output": out})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "router_ip": ip, "output": out})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// runScript запускает bash <repo>/<rel> с аргументами, возвращает вывод.
func (s *server) runScript(rel string, args ...string) (string, error) {
	cmd := exec.Command("bash", append([]string{filepath.Join(s.repoDir, rel)}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// ---- config.env (KEY="value" с возможным inline-комментарием) ---------

func readConfigVar(path, key string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, ln := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, key+"=") {
			continue
		}
		rhs := strings.TrimSpace(t[len(key)+1:])
		if strings.HasPrefix(rhs, "\"") {
			if j := strings.Index(rhs[1:], "\""); j >= 0 {
				return rhs[1 : 1+j], nil
			}
		}
		return strings.Fields(rhs)[0], nil // unquoted: первый токен (отрезаем комментарий)
	}
	return "", nil
}

// writeConfigVar заменяет (или добавляет) строку KEY="value", сохраняя
// остальные строки и права файла. Атомарно (temp + rename).
func writeConfigVar(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	info, _ := os.Stat(path)
	mode := os.FileMode(0o600)
	if info != nil {
		mode = info.Mode()
	}
	lines := strings.Split(string(data), "\n")
	newline := fmt.Sprintf("%s=%q", key, value)
	found := false
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), key+"=") {
			lines[i] = newline
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, newline)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
