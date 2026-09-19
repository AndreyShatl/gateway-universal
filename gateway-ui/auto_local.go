package main

// auto_local.go (T-auto-local, 2026-09-17, идея владельца) — кнопка «auto»
// больше не делает blockcheck+majORITY-vote по сервису. Новый контракт:
// все домены сервиса ставятся в очередь мозга → фоновый параллельный поиск
// (WORKERS=4) в изолированном netns → домен переключается на DPI ТОЛЬКО при
// подтверждённом обходе, с сохранением VPS-подложки и без разрыва соединений.
// Ничего не применяется «молча»: пользователь видит прогресс воркера
// (brain-progress / панель FullCheck) и журнал переключений.
//
// Очередь — тот же файл brainQueueFile (переиспользуем константу pinvps.go, строка "domain\tauto"),
// дедуп — чтением файла перед записью (appends < PIPE_BUF атомарны; гонка с
// воркером безопасна: лишний дубль просто отработает вторым проходом).

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func inQueue(domain string) bool {
	data, err := os.ReadFile(brainQueueFile)
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.SplitN(ln, "\t", 2)[0] == domain {
			return true
		}
	}
	return false
}

// handleAutoLocal — POST /api/services/{id}/auto-local: поставить все домены
// сервиса в очередь фонового поиска локального обхода (без смены режима).
func (s *server) handleAutoLocal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST only"})
		return
	}
	id := r.PathValue("id")
	svc, err := s.readServices()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	var domains []string
	for _, sv := range svc {
		if sv.ID == id {
			domains = sv.Domains
			break
		}
	}
	if len(domains) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "сервис не найден или без доменов"})
		return
	}
	f, err := os.OpenFile(brainQueueFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer f.Close()
	enq := 0
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || inQueue(d) {
			continue
		}
		if _, err := f.WriteString(d + "\tauto\n"); err == nil {
			enq++
		}
	}
	// Готовые стратегии из ночного кэша — применить сразу в фоне (без проб);
	// в очередь идут только те, у кого готовности нет.
	go runCmd("bash", "/opt/gateway-brain/brain-apply-ready.sh", id)
	s.timeline.Record("service.auto-local", id+": "+itoa(enq)+" доменов в фоновый поиск LOCAL + мгновенное применение готовых из кэша")
	writeJSON(w, http.StatusOK, map[string]any{
		"enqueued": enq,
		"message":  "Домены поставлены в фоновый поиск локального обхода (4 воркера). Переключение — только при подтверждённом обходе, соединения не рвутся, VPS-режим сервиса не меняется.",
	})
}

func itoa(n int) string { return strconv.Itoa(n) }

// handleDPIReadiness — GET /api/dpi-readiness: ночной кэш готовности DPI
// (domain -> ready/engine/strategy/verified_at). Фронт считает N/M по сервису.
func (s *server) handleDPIReadiness(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("/etc/gateway/observe/dpi-readiness.json")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"entries": []any{}, "hint": "ночная проверка ещё не сформировала кэш готовности"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
