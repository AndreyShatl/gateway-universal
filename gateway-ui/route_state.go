package main

// route_state.go (Этап 5 / STAGE 4 OBSERVE, 2026-09-07) — отдаёт снапшот
// состояния маршрутов, который пишет gateway-detector brain-observe в
// /etc/gateway/observe/route-state.json. UI не дублирует логику сборки —
// один источник истины, детектор владеет форматом (см. detector/route_state.go
// и Obsidian «Shattl-Brain»).

import (
	"encoding/json"
	"net/http"
	"os"
)

const routeStateFile = "/etc/gateway/observe/route-state.json"

func (s *server) handleRouteState(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(routeStateFile)
	if err != nil {
		// снапшота ещё нет (первый прогон таймера не случился) — честный
		// пустой ответ с пояснением, фронт покажет заглушку
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{
			"generated":    nil,
			"destinations": []any{},
			"totals":       nil,
			"hint":         "снапшот ещё не создан — ждём первый прогон gateway-brain-observe (таймер каждые 30 мин)",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
