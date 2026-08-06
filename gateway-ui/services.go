package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Services-карточки (ТЗ Shattl Gateway UI Specification, 2026-08-05): для
// каждого управляемого сервиса — состояние, аптайм, CPU%, RAM, кнопки
// Restart/Stop/Logs. Область НАМЕРЕННО ограничена статическими systemd-
// юнитами из manageable{} (xray/zapret/fix-gateway/discord-tproxy) — ciadpi/
// zapret2 управляются per-домен динамическими группами ("мозг", см.
// monitor.go), у них нет одного стабильного unit'а для карточки такого вида.

type serviceDetail struct {
	Name      string  `json:"name"`
	State     string  `json:"state"`
	UptimeS   int64   `json:"uptime_s"`
	MemoryMB  float64 `json:"memory_mb"`
	CPUPct    float64 `json:"cpu_pct"`
	Loggable  bool    `json:"loggable"`
	Stoppable bool    `json:"stoppable"`
}

// serviceOrder — фиксированный порядок карточек (map не гарантирует порядок).
var serviceOrder = []string{"xray", "zapret", "fix-gateway", "discord-tproxy"}

func (s *server) handleServicesDetail(w http.ResponseWriter, r *http.Request) {
	type sample struct {
		cpuNsec uint64
		ok      bool
	}
	first := make(map[string]sample, len(serviceOrder))
	for _, name := range serviceOrder {
		first[name] = sample{cpuNsec: readCPUUsageNsec(name)}
	}
	time.Sleep(cpuSampleDelay)

	out := make([]serviceDetail, 0, len(serviceOrder))
	for _, name := range serviceOrder {
		state := strings.TrimSpace(mustRunCmd("systemctl", "is-active", name+".service"))
		enter := mustRunCmd("systemctl", "show", name+".service", "-p", "ActiveEnterTimestamp", "--value")
		memRaw := mustRunCmd("systemctl", "show", name+".service", "-p", "MemoryCurrent", "--value")
		secondCPU := readCPUUsageNsec(name)

		memBytes, _ := strconv.ParseUint(strings.TrimSpace(memRaw), 10, 64)
		var cpuPct float64
		if firstCPU := first[name].cpuNsec; secondCPU >= firstCPU {
			cpuPct = float64(secondCPU-firstCPU) / float64(cpuSampleDelay.Nanoseconds()) * 100
		}

		out = append(out, serviceDetail{
			Name:      name,
			State:     state,
			UptimeS:   activeUptimeSeconds(enter),
			MemoryMB:  float64(memBytes) / 1024 / 1024,
			CPUPct:    cpuPct,
			Loggable:  loggable[name],
			Stoppable: manageable[name],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func readCPUUsageNsec(name string) uint64 {
	raw := mustRunCmd("systemctl", "show", name+".service", "-p", "CPUUsageNSec", "--value")
	v, _ := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	return v
}

// activeUptimeSeconds разбирает ActiveEnterTimestamp ("Wed 2026-08-05 20:30:15
// MSK") — формат systemd, локальное время демона. Пустая строка/"n/a" (сервис
// никогда не запускался) — 0, не ошибка.
func activeUptimeSeconds(ts string) int64 {
	ts = strings.TrimSpace(ts)
	if ts == "" || ts == "n/a" {
		return 0
	}
	t, err := time.ParseInLocation("Mon 2006-01-02 15:04:05 MST", ts, time.Local)
	if err != nil {
		return 0
	}
	d := time.Since(t)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}

// mustRunCmd — обёртка над runCmd, где ошибка ожидаема и незначима (сервис
// ещё не существует / systemctl show для несуществующего свойства) —
// возвращает то, что есть, ошибку осознанно отбрасывает.
func mustRunCmd(name string, args ...string) string {
	out, _ := runCmd(name, args...)
	return out
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	svc := r.FormValue("service")
	if !manageable[svc] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "сервис не разрешён"})
		return
	}
	out, err := runCmd("systemctl", "stop", svc+".service")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "output": out})
		return
	}
	s.timeline.Record("service.stop", svc+" stopped")
	state, _ := runCmd("systemctl", "is-active", svc+".service")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": svc, "state": strings.TrimSpace(state)})
}
