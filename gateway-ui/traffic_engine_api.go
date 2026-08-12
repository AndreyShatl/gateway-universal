package main

import (
	"encoding/json"
	"net/http"
)

// traffic_engine_api.go — API-слой из ТЗ "Traffic Engine Orchestrator" п.15,
// честно смэппленный на реальность: ShattlBypass — единственный adapter,
// всегда установлен (часть install.sh), переключать не на что. install/
// uninstall/switch отвечают явным "не применимо", а не изображают
// функциональность, которой нет — ТЗ п.20 ("не создавать дублирующую
// систему управления") важнее формального покрытия списка эндпоинтов.

type trafficEngineSummary struct {
	Name   string       `json:"name"`
	Active bool         `json:"active"`
	Status HealthStatus `json:"status"`
}

// GET /api/traffic-engine — список движков (сейчас всегда один).
func (s *server) handleTrafficEngineList(w http.ResponseWriter, r *http.Request) {
	st := shattlBypassStatus()
	writeJSON(w, http.StatusOK, []trafficEngineSummary{
		{Name: "shattl-bypass", Active: true, Status: st.Status},
	})
}

// GET /api/traffic-engine/status — то же самое, что /api/engine/status
// (эндпоинт для Overview появился раньше API-слоя из ТЗ) — оставлены оба
// пути намеренно: старый уже используется фронтендом, этот — для
// соответствия ТЗ и будущих внешних интеграций.
func (s *server) handleTrafficEngineStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, shattlBypassStatus())
}

// GET /api/traffic-engine/available — единственный доступный adapter.
// PodkopAdapter/SSClashAdapter сознательно не реализованы (см. engine.go).
func (s *server) handleTrafficEngineAvailable(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"available": []string{"shattl-bypass"},
		"note":      "Podkop/SSClash не реализованы — платформа Debian, не OpenWrt, конкретной задачи для них нет",
	})
}

// notApplicable — единый ответ для install/uninstall/switch: честно, а не
// молча 404 и не притворная имитация действия.
func notApplicable(w http.ResponseWriter, reason string) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"error": "не применимо",
		"why":   reason,
	})
}

func (s *server) handleTrafficEngineInstall(w http.ResponseWriter, r *http.Request) {
	notApplicable(w, "shattl-bypass ставится как часть install.sh, отдельного install-шага нет")
}

func (s *server) handleTrafficEngineUninstall(w http.ResponseWriter, r *http.Request) {
	notApplicable(w, "shattl-bypass — не съёмный компонент; удаление шлюза целиком делает uninstall.sh")
}

func (s *server) handleTrafficEngineSwitch(w http.ResponseWriter, r *http.Request) {
	notApplicable(w, "сейчас доступен только один adapter (shattl-bypass) — переключаться не на что")
}

// POST /api/traffic-engine/start|stop|restart — обёрнуты в оркестратор
// (backup/apply/health-check/откат, см. controlCore в engine.go).
func (s *server) handleTrafficEngineControl(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := s.orchestrator.controlCore(action)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": res.Detail})
			return
		}
		if !res.ok() {
			writeJSON(w, http.StatusConflict, map[string]any{"error": res.Detail, "status": res.Status})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// POST /api/traffic-engine/health-check — форсированная проверка "сейчас",
// без изменения состояния (ТЗ п.10) — то же, что status, но по семантике
// POST-действие, а не GET-чтение (некоторые клиенты ТЗ ожидают именно так).
func (s *server) handleTrafficEngineHealthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, shattlBypassStatus())
}

// GET /api/traffic-engine/config — текущий список сервисов/доменов и их
// режимов (zapret/vps/auto) — та же модель, что уже отдаёт /api/zapret/services,
// просто под путём из ТЗ.
func (s *server) handleTrafficEngineConfigGet(w http.ResponseWriter, r *http.Request) {
	s.handleServices(w, r)
}

// PUT /api/traffic-engine/config — запись; /api/zapret/services принимает
// POST для мутаций (см. handleServices) — ТЗ говорит PUT, реальный формат
// тела не меняется, просто иначе называется метод снаружи.
func (s *server) handleTrafficEngineConfigPut(w http.ResponseWriter, r *http.Request) {
	r2 := r.Clone(r.Context())
	r2.Method = http.MethodPost
	s.handleServices(w, r2)
}

// GET /api/traffic-engine/history — последние события engine.* из Mission
// Timeline (не отдельный журнал — тот же источник, что уже видно на Overview).
func (s *server) handleTrafficEngineHistory(w http.ResponseWriter, r *http.Request) {
	all := s.timeline.Recent(200)
	out := make([]timelineEvent, 0, len(all))
	for _, ev := range all {
		if len(ev.Kind) > 7 && ev.Kind[:7] == "engine." {
			out = append(out, ev)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/traffic-engine/backups — снапшоты оркестратора (ТЗ п.9), то же,
// что /api/engine/snapshots — alias под путём из ТЗ.
func (s *server) handleTrafficEngineBackups(w http.ResponseWriter, r *http.Request) {
	s.handleEngineSnapshots(w, r)
}

type rollbackRequest struct {
	SnapshotID string `json:"snapshot_id"`
}

// POST /api/traffic-engine/rollback — откат к конкретному снапшоту по ID.
// Сейчас снапшоты создаются только для vps-mode (см. applyVPSMode) и
// traffic-engine-core (см. controlCore) — оба уже откатываются
// автоматически при провале health-check; этот эндпоинт — для РУЧНОГО
// отката (например, пользователь передумал уже ПОСЛЕ успешного apply).
func (s *server) handleTrafficEngineRollback(w http.ResponseWriter, r *http.Request) {
	var req rollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SnapshotID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "snapshot_id обязателен"})
		return
	}

	s.orchestrator.mu.Lock()
	var target *snapshot
	for i := range s.orchestrator.snapshots {
		if s.orchestrator.snapshots[i].ID == req.SnapshotID {
			target = &s.orchestrator.snapshots[i]
			break
		}
	}
	s.orchestrator.mu.Unlock()

	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "снапшот не найден (буфер отката хранит последние 20 операций)"})
		return
	}

	switch target.Component {
	case "vps-mode":
		res, err := s.orchestrator.applyVPSMode(target.Data["mode"])
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": res.Detail})
			return
		}
		writeJSON(w, http.StatusOK, res)
	default:
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "ручной откат для компонента " + target.Component + " не поддержан"})
	}
}
