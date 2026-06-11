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

// normalizeEntry приводит запись к каноническому виду (lower, без префикса domain:).
func normalizeEntry(raw string) string {
	e := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(e, "geosite:") {
		return e
	}
	return strings.TrimPrefix(e, "domain:")
}

// validateEntry нормализует и проверяет запись (домен или geosite:тег).
func validateEntry(raw string) (string, bool) {
	e := normalizeEntry(raw)
	if strings.HasPrefix(e, "geosite:") {
		return e, reGeosite.MatchString(e)
	}
	return e, reDomain.MatchString(e)
}

// curatedSet — записи из курируемых списков репо (xray/domains/*.txt).
// Их незачем дублировать в local.txt: build-domains всё равно дедуплицирует.
func (s *server) curatedSet() map[string]bool {
	set := map[string]bool{}
	dir := filepath.Join(s.repoDir, "xray", "domains")
	entries, _ := os.ReadDir(dir)
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".txt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			set[normalizeEntry(t)] = true
		}
	}
	return set
}

// curatedList — курируемые записи (xray/domains/*.txt) отсортированные A-Z.
func (s *server) curatedList() []string {
	m := s.curatedSet()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// splitEntries разбивает ввод (несколько доменов) по переводам строк,
// запятым, точкам с запятой и пробелам.
func splitEntries(raw string) []string {
	f := func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == ';' || r == ' ' || r == '\t' }
	return strings.FieldsFunc(raw, f)
}

func (s *server) handleDomains(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"domains":  s.listDomains(),  // пользовательские (редактируемые), A-Z
			"defaults": s.curatedList(),  // курируемые по умолчанию (только чтение), A-Z
		})

	case http.MethodPost:
		switch r.FormValue("action") {
		case "add":
			s.handleDomainsAdd(w, r)
		case "remove":
			s.handleDomainsRemove(w, r)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action: add|remove"})
		}

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

// handleDomainsAdd — пакетное добавление: несколько доменов за раз, с разбором
// дублей (уже добавленные / уже в списках по умолчанию) и невалидных.
func (s *server) handleDomainsAdd(w http.ResponseWriter, r *http.Request) {
	raws := splitEntries(r.FormValue("domain"))
	if len(raws) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "пусто — впишите домен(ы)"})
		return
	}
	curated := s.curatedSet()
	local := map[string]bool{}
	for _, e := range s.listDomains() {
		local[e] = true
	}
	var add, dupPresent, dupDefault, invalid []string
	seen := map[string]bool{}
	for _, raw := range raws {
		e, ok := validateEntry(raw)
		if !ok {
			invalid = append(invalid, raw)
		} else if local[e] || seen[e] {
			dupPresent = append(dupPresent, e)
		} else if curated[e] {
			dupDefault = append(dupDefault, e)
		} else {
			add = append(add, e)
			seen[e] = true
		}
	}
	if len(add) > 0 {
		if err := s.appendDomains(add); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if out, err := s.applyXray(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "применение не удалось, откатано: " + err.Error(), "output": out})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "added": add, "skipped_present": dupPresent,
		"skipped_default": dupDefault, "invalid": invalid, "domains": s.listDomains(),
	})
}

func (s *server) handleDomainsRemove(w http.ResponseWriter, r *http.Request) {
	entry, ok := validateEntry(r.FormValue("domain"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "некорректная запись"})
		return
	}
	changed, err := s.removeDomain(entry)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if changed {
		if out, err := s.applyXray(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "применение не удалось, откатано: " + err.Error(), "output": out})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "domains": s.listDomains()})
}

// appendDomains дописывает записи в local.txt (вызывающий уже отфильтровал дубли).
func (s *server) appendDomains(entries []string) error {
	if err := os.MkdirAll(s.userDomainsDir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.localDomainsFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(entries, "\n") + "\n")
	return err
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
