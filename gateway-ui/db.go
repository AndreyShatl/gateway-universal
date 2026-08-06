package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// whitelist + presets (T48). НЕ используем cgo-sqlite драйвер из Go: modernc.org/sqlite
// тянет свой cc/ccgo-тулчейн (C->Go транспайлер) и на сборке разово съедает больше
// гигабайта — стенд/боевая это неттопы с 6-10ГБ ДИСКА ЦЕЛИКОМ, не хватает места
// (проверено: "no space left on device" на реальной сборке). Вместо этого gateway-ui,
// как и bash-скрипты мозга, зовёт единственную точку схемы — scripts/gwdb.py (stdlib
// sqlite3 в python3, уже используется в brain-nightly.sh) — ноль новых зависимостей.
func (s *server) gwdb(args ...string) (string, error) {
	cmd := exec.Command("python3", append([]string{filepath.Join(s.repoDir, "scripts", "gwdb.py")}, args...)...)
	if s.dbPath != "" {
		cmd.Env = append(os.Environ(), "GWDB_PATH="+s.dbPath)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// defaultWhitelistSeed — T49: .ru/.su/.рф (+ punycode xn--p1ai) целиком не
// анализируются детектором. Вызывается при каждом старте UI (INSERT OR IGNORE,
// идемпотентно) — если пользователь удалит запись через UI, а потом
// перезапустит gateway-ui, сид восстановится (осознанный компромисс v1).
var defaultWhitelistSeed = [][2]string{
	{"ru", "suffix"}, {"su", "suffix"}, {"рф", "suffix"}, {"xn--p1ai", "suffix"},
}

// initGWDB создаёт схему, засеивает стандартные пресеты и дефолтный whitelist
// (идемпотентно — safe перезапускать при каждом старте UI).
func (s *server) initGWDB() error {
	if _, err := s.gwdb("init", "--strategies-file", filepath.Join(s.repoDir, "gateway-ui", "strategies.json")); err != nil {
		return err
	}
	for _, wl := range defaultWhitelistSeed {
		s.gwdb("whitelist-add", wl[0], wl[1], "seed: .ru/.su/.рф не анализируются")
	}
	return nil
}

type whitelistEntry struct {
	ID      int64  `json:"id"`
	Pattern string `json:"pattern"`
	Kind    string `json:"kind"`
	Note    string `json:"note"`
	Source  string `json:"source"`
	AddedAt string `json:"added_at"`
}

// hiddenWhitelistNote — записи, чья заметка помечена как техническая (2026-08-06):
// принудительный VPS-роутинг adult-доменов (pornhub и т.п., см. запись от
// 2026-08-01 — zapret ломал авторизацию/редиректил на VK) реально нужен
// детектору, но неловко показывать в списке панели. INSERT/whitelisted-запрос
// в БД не трогаем — фильтруем только вывод в UI.
func hiddenWhitelistNote(note string) bool {
	return strings.Contains(strings.ToLower(note), "pornhub")
}

func (s *server) handleWhitelist(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := s.gwdb("whitelist-list")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "output": out})
			return
		}
		list := []whitelistEntry{}
		for _, ln := range strings.Split(out, "\n") {
			if ln == "" {
				continue
			}
			f := strings.Split(ln, "\t")
			if len(f) != 6 {
				continue
			}
			id, _ := strconv.ParseInt(f[0], 10, 64)
			entry := whitelistEntry{ID: id, Pattern: f[1], Kind: f[2], Note: f[3], Source: f[4], AddedAt: f[5]}
			if hiddenWhitelistNote(entry.Note) {
				// Технические записи вроде принудительного VPS-роутинга adult-
				// доменов (см. hiddenWhitelistNote) реально нужны детектору —
				// не удаляем из БД, только не показываем в UI, чтобы не смущать
				// пользователей панели.
				continue
			}
			list = append(list, entry)
		}
		writeJSON(w, http.StatusOK, map[string]any{"whitelist": list})

	case http.MethodPost:
		switch r.FormValue("action") {
		case "add":
			pattern := strings.ToLower(strings.TrimSpace(r.FormValue("pattern")))
			kind := r.FormValue("kind")
			if pattern == "" || (kind != "suffix" && kind != "exact") {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pattern + kind=suffix|exact обязательны"})
				return
			}
			if out, err := s.gwdb("whitelist-add", pattern, kind, r.FormValue("note")); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "output": out})
				return
			}
			s.timeline.Record("config.updated", "Whitelist updated")
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case "remove":
			if out, err := s.gwdb("whitelist-remove", r.FormValue("id")); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "output": out})
				return
			}
			s.timeline.Record("config.updated", "Whitelist updated")
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action: add|remove"})
		}

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type presetRow struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Proto        string  `json:"proto"`
	Args         string  `json:"args"`
	Source       string  `json:"source"`
	Trusted      bool    `json:"trusted"`
	SuccessCount int     `json:"success_count"`
	Engine       string  `json:"engine"`
	Score        float64 `json:"score"`
	Confidence   int     `json:"confidence"`
}

func (s *server) handlePresets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		args := []string{"strategies-list"}
		if tier := r.URL.Query().Get("tier"); tier != "" {
			args = append(args, "--tier", tier)
		}
		if proto := r.URL.Query().Get("proto"); proto != "" {
			args = append(args, "--proto", proto)
		}
		if engine := r.URL.Query().Get("engine"); engine != "" {
			args = append(args, "--engine", engine)
		}
		out, err := s.gwdb(args...)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "output": out})
			return
		}
		list := []presetRow{}
		for _, ln := range strings.Split(out, "\n") {
			if ln == "" {
				continue
			}
			f := strings.Split(ln, "\t")
			if len(f) != 10 {
				continue
			}
			id, _ := strconv.ParseInt(f[0], 10, 64)
			sc, _ := strconv.Atoi(f[6])
			score, _ := strconv.ParseFloat(f[8], 64)
			confidence, _ := strconv.Atoi(f[9])
			list = append(list, presetRow{
				ID: id, Name: f[1], Proto: f[2], Args: f[3], Source: f[4],
				Trusted: f[5] == "1", SuccessCount: sc, Engine: f[7],
				Score: score, Confidence: confidence,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"presets": list})

	case http.MethodPost:
		if r.FormValue("action") != "add" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action: add"})
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		proto := r.FormValue("proto")
		presetArgs := strings.TrimSpace(r.FormValue("args"))
		engine := r.FormValue("engine")
		if engine == "" {
			engine = "zapret"
		}
		if name == "" || presetArgs == "" || (proto != "tcp" && proto != "udp") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name + proto=tcp|udp + args обязательны"})
			return
		}
		if out, err := s.gwdb("strategy-add", name, proto, presetArgs, "--engine", engine); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "output": out})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
