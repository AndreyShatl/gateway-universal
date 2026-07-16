package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Перепроверка авто-обхода (T42): отдельная вкладка с настройками расписания.
// Работает независимо от тумблера авто-обхода — список чистится, даже если
// авто-обход выключен. Конфиг — /etc/gateway/recheck.json; расписание задаётся
// через systemd-override таймера gateway-recheck.timer.

const recheckTimerOverride = "/etc/systemd/system/gateway-recheck.timer.d/schedule.conf"

type recheckCfg struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time"`    // HH:MM
	Days    string `json:"days"`    // "daily" или "Mon,Wed,Fri"
	Workers int    `json:"workers"` // параллельных проб
	// статистика последнего прогона (пишет детектор):
	LastRun      string `json:"last_run,omitempty"`
	LastChecked  int    `json:"last_checked,omitempty"`
	LastRemoved  int    `json:"last_removed,omitempty"`
	LastPending  int    `json:"last_pending,omitempty"`
	LastDuration int    `json:"last_duration_sec,omitempty"`
}

var (
	timeRe = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)
	dayset = map[string]bool{"Mon": true, "Tue": true, "Wed": true, "Thu": true, "Fri": true, "Sat": true, "Sun": true}
)

func (s *server) readRecheck() recheckCfg {
	c := recheckCfg{Enabled: true, Time: "04:00", Days: "daily", Workers: 30}
	if data, err := os.ReadFile(s.recheckFile); err == nil {
		json.Unmarshal(data, &c)
	}
	if c.Workers <= 0 {
		c.Workers = 30
	}
	if !timeRe.MatchString(c.Time) {
		c.Time = "04:00"
	}
	if c.Days == "" {
		c.Days = "daily"
	}
	return c
}

func (s *server) writeRecheck(c recheckCfg) error {
	if err := os.MkdirAll(filepath.Dir(s.recheckFile), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := s.recheckFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.recheckFile)
}

// onCalendar — systemd OnCalendar из time+days.
func onCalendar(c recheckCfg) string {
	if c.Days == "daily" || c.Days == "" {
		return "*-*-* " + c.Time + ":00"
	}
	return c.Days + " *-*-* " + c.Time + ":00"
}

// applyRecheckSchedule — переписать override таймера и (пере)включить/выключить его.
func (s *server) applyRecheckSchedule(c recheckCfg) {
	if !c.Enabled {
		runCmd("systemctl", "disable", "--now", "gateway-recheck.timer")
		return
	}
	os.MkdirAll(filepath.Dir(recheckTimerOverride), 0o755)
	// пустой OnCalendar= сбрасывает значение из unit, затем задаём своё
	body := "[Timer]\nOnCalendar=\nOnCalendar=" + onCalendar(c) + "\n"
	os.WriteFile(recheckTimerOverride, []byte(body), 0o644)
	runCmd("systemctl", "daemon-reload")
	runCmd("systemctl", "enable", "--now", "gateway-recheck.timer")
	runCmd("systemctl", "restart", "gateway-recheck.timer")
}

// nextRun — время следующего запуска таймера (из systemctl), для отображения.
func nextRun() string {
	out, err := runCmd("systemctl", "show", "gateway-recheck.timer", "-p", "NextElapseUSecRealtime", "--value")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (s *server) handleRecheck(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c := s.readRecheck()
		writeJSON(w, http.StatusOK, map[string]any{"cfg": c, "next": nextRun()})

	case http.MethodPost:
		c := s.readRecheck()
		switch r.FormValue("action") {
		case "save":
			c.Enabled = r.FormValue("enabled") == "true"
			if t := r.FormValue("time"); timeRe.MatchString(t) {
				c.Time = t
			}
			if d := r.FormValue("days"); d != "" {
				if d == "daily" || validDays(d) {
					c.Days = d
				}
			}
			if wk, err := strconv.Atoi(r.FormValue("workers")); err == nil && wk >= 1 && wk <= 200 {
				c.Workers = wk
			}
			if err := s.writeRecheck(c); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			s.applyRecheckSchedule(c)
		case "run":
			// запустить перепроверку прямо сейчас (oneshot, не блокируя ответ)
			runCmd("systemctl", "start", "--no-block", "gateway-recheck.service")
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action: save|run"})
			return
		}
		c = s.readRecheck()
		writeJSON(w, http.StatusOK, map[string]any{"cfg": c, "next": nextRun()})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func validDays(d string) bool {
	parts := strings.Split(d, ",")
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if !dayset[strings.TrimSpace(p)] {
			return false
		}
	}
	return true
}
