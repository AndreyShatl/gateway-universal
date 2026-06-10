package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Пользовательские домены живут в <userDomainsDir>/local.txt (вне репо,
// переживают передеплой). Применение: render-config.sh пересобирает
// config.json из всех .txt и UI рестартит xray.

var (
	reDomain  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	reGeosite = regexp.MustCompile(`^geosite:[a-z0-9_-]+$`)
)

func (s *server) localDomainsFile() string { return filepath.Join(s.userDomainsDir, "local.txt") }

// validateEntry нормализует и проверяет запись (домен или geosite:тег).
func validateEntry(raw string) (string, bool) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(e, "geosite:") {
		return e, reGeosite.MatchString(e)
	}
	e = strings.TrimPrefix(e, "domain:") // если ввели с префиксом — убрать
	return e, reDomain.MatchString(e)
}

func (s *server) handleDomains(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"domains": s.listDomains()})

	case http.MethodPost:
		action := r.FormValue("action")
		entry, ok := validateEntry(r.FormValue("domain"))
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "некорректная запись (домен или geosite:тег)"})
			return
		}
		var changed bool
		var err error
		switch action {
		case "add":
			changed, err = s.addDomain(entry)
		case "remove":
			changed, err = s.removeDomain(entry)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action: add|remove"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if !changed {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "domains": s.listDomains(), "note": "без изменений"})
			return
		}
		// Применить: render-config + restart xray; при ошибке — откат файла
		if out, err := s.applyXray(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "применение не удалось, изменение откатано: " + err.Error(), "output": out})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "domains": s.listDomains()})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) listDomains() []string {
	data, err := os.ReadFile(s.localDomainsFile())
	if err != nil {
		return []string{}
	}
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func (s *server) addDomain(entry string) (bool, error) {
	for _, e := range s.listDomains() {
		if e == entry {
			return false, nil // уже есть
		}
	}
	if err := os.MkdirAll(s.userDomainsDir, 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(s.localDomainsFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.WriteString(entry + "\n"); err != nil {
		return false, err
	}
	return true, nil
}

func (s *server) removeDomain(entry string) (bool, error) {
	path := s.localDomainsFile()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var kept []string
	removed := false
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) == entry {
			removed = true
			continue
		}
		kept = append(kept, ln)
	}
	if !removed {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644)
}

// applyXray пересобирает config.json из всех .txt и рестартит xray.
// При ошибке восстанавливает local.txt из снимка (без битого состояния).
func (s *server) applyXray() (string, error) {
	snapshot, _ := os.ReadFile(s.localDomainsFile())
	rollback := func() {
		if snapshot != nil {
			os.WriteFile(s.localDomainsFile(), snapshot, 0o644)
		} else {
			os.Remove(s.localDomainsFile())
		}
	}
	out, err := s.runScript("xray/render-config.sh",
		"--template", filepath.Join(s.repoDir, "xray", "config.template.json"),
		"--out", s.xrayConfig,
		"--config", s.configEnv,
		"--xray", s.xrayBin,
		"--user-domains-dir", s.userDomainsDir,
	)
	if err != nil {
		rollback()
		return out, fmt.Errorf("render-config: %v", err)
	}
	if rout, rerr := runCmd("systemctl", "restart", "xray.service"); rerr != nil {
		rollback()
		// перерендер вернёт рабочий конфиг к прежнему списку
		s.runScript("xray/render-config.sh", "--template", filepath.Join(s.repoDir, "xray", "config.template.json"),
			"--out", s.xrayConfig, "--config", s.configEnv, "--user-domains-dir", s.userDomainsDir)
		runCmd("systemctl", "restart", "xray.service")
		return out + "\n" + rout, fmt.Errorf("restart xray: %v", rerr)
	}
	return out, nil
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
