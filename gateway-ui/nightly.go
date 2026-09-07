package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// Прогресс ночной проверки (T-progress-ui): brain-nightly.sh/brain-static-reeval.sh
// пишут накопительный total в /etc/gateway/brain-progress.json при постановке в
// очередь; brain-worker.sh сбрасывает его в 0, когда очередь опустела. done
// считаем здесь же (total - текущая длина /etc/gateway/brain-queue), не храним —
// проще и не может разъехаться с реальной очередью.
type nightlyProgress struct {
	Total     int      `json:"total"`
	Done      int      `json:"done"`
	Remaining int      `json:"remaining"`
	StartedAt string   `json:"started_at"`
	Running   bool     `json:"running"`
	Feed      []string `json:"feed"`
}

func (s *server) handleNightlyProgress(w http.ResponseWriter, r *http.Request) {
	total, startedAt := 0, ""
	if data, err := os.ReadFile("/etc/gateway/brain-progress.json"); err == nil {
		var p struct {
			Total     int    `json:"total"`
			StartedAt string `json:"started_at"`
		}
		if json.Unmarshal(data, &p) == nil {
			total, startedAt = p.Total, p.StartedAt
		}
	}
	remaining := 0
	if f, err := os.Open("/etc/gateway/brain-queue"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) != "" {
				remaining++
			}
		}
		f.Close()
	}
	done := total - remaining
	if done < 0 {
		done = 0
	}
	writeJSON(w, http.StatusOK, nightlyProgress{
		Total: total, Done: done, Remaining: remaining,
		StartedAt: startedAt, Running: total > 0 && remaining > 0,
		Feed: tailBrainLog(25),
	})
}

// handleNightlyTrigger — п.5 ТЗ: ручной запуск "Полная проверка", тот же
// systemd-юнит, что и ночной таймер (gateway-brain-nightly.service), просто
// по кнопке, а не по расписанию 04:00. Не дублирует логику постановки в
// очередь — доверяем существующему brain-nightly.sh целиком.
func (s *server) handleNightlyTrigger(w http.ResponseWriter, r *http.Request) {
	if data, err := os.ReadFile("/etc/gateway/brain-progress.json"); err == nil {
		var p struct {
			Total int `json:"total"`
		}
		if json.Unmarshal(data, &p) == nil && p.Total > 0 {
			remaining := 0
			if f, err := os.Open("/etc/gateway/brain-queue"); err == nil {
				sc := bufio.NewScanner(f)
				for sc.Scan() {
					if strings.TrimSpace(sc.Text()) != "" {
						remaining++
					}
				}
				f.Close()
			}
			if remaining > 0 {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "проверка уже выполняется"})
				return
			}
		}
	}
	if out, err := exec.Command("systemctl", "start", "gateway-brain-nightly.service").CombinedOutput(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": strings.TrimSpace(string(out))})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// tailBrainLog — последние N строк лога мозга (для живой ленты в UI). Читает весь
// файл — на домашнем шлюзе он в разумных пределах (ротация logrotate), простое и
// надёжное решение без сложного seek-с-конца.
func tailBrainLog(n int) []string {
	data, err := os.ReadFile("/var/log/gateway-brain.log")
	if err != nil {
		return []string{}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	// новые сверху — удобнее читать "живую ленту"
	out := make([]string, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] != "" {
			out = append(out, lines[i])
		}
	}
	return out
}
