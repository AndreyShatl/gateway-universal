package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// engine.go — первый срез Traffic Engine Orchestrator (T-shattl-orchestrator,
// 2026-08-12): backup -> apply -> health-check -> rollback вокруг
// потенциально опасных операций над обходом. Не переписывает существующую
// логику (zapret/zapret2/CIADPI/xray/мозг остаются как есть) — оборачивает
// её общей safety-прослойкой. Обкатывается на первом реальном сценарии:
// переключение VPS-режима (см. handleVPSMode) — именно эта операция реально
// ломала Telegram в этой же сессии (2026-08-09), когда применялась напрямую
// без health-check/отката.
//
// Из ТЗ "Traffic Engine Orchestrator" сознательно взято только это ядро
// (backup/validate/apply/health-check/rollback + событийный журнал в
// существующий Mission Timeline). PodkopAdapter/SSClashAdapter НЕ
// реализуются — платформа Debian, не OpenWrt, конкретной задачи для них
// пока нет (см. обсуждение 2026-08-12). ShattlBypassAdapter — единственный
// adapter на данный момент.

// HealthStatus — упрощённый набор из ТЗ (healthy/degraded/failed), без
// промежуточных состояний типа Installing/Updating — они не нужны для
// операций над уже установленным ShattlBypass.
type HealthStatus string

const (
	HealthHealthy  HealthStatus = "healthy"
	HealthDegraded HealthStatus = "degraded"
	HealthFailed   HealthStatus = "failed"
)

// HealthResult — структурированный результат healthCheck() (см. ТЗ п.10):
// статус + человекочитаемая диагностика, без stack trace наружу.
type HealthResult struct {
	Status HealthStatus `json:"status"`
	Detail string       `json:"detail"`
}

func (h HealthResult) ok() bool { return h.Status == HealthHealthy }

// snapshot — минимальный backup (ТЗ п.9): что меняем и чем можем откатить.
// Data — плоский набор ключ/значение (путь конфига -> его содержимое на
// момент snapshot), этого достаточно для файловых состояний вроде
// vps-mode.conf; более сложные операции расширят Data по мере необходимости.
type snapshot struct {
	ID        string            `json:"id"`
	At        time.Time         `json:"at"`
	Component string            `json:"component"`
	Reason    string            `json:"reason"`
	Data      map[string]string `json:"data"`
}

// orchestrator — держит последние snapshot'ы в памяти (не переживает
// рестарт gateway-ui — это осознанно: снапшоты нужны только для отката
// ТЕКУЩЕЙ операции, не как персистентная история конфигов).
type orchestrator struct {
	mu        sync.Mutex
	snapshots []snapshot
	timeline  *timelineLog
	configEnv string
}

func newOrchestrator(timeline *timelineLog, configEnv string) *orchestrator {
	return &orchestrator{timeline: timeline, configEnv: configEnv}
}

func (o *orchestrator) saveSnapshot(component, reason string, data map[string]string) snapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := snapshot{
		ID:        fmt.Sprintf("%s-%d", component, time.Now().UnixNano()),
		At:        time.Now(),
		Component: component,
		Reason:    reason,
		Data:      data,
	}
	o.snapshots = append(o.snapshots, s)
	// последние 20 снапшотов достаточно — это не аудит-лог, а буфер отката
	if len(o.snapshots) > 20 {
		o.snapshots = o.snapshots[len(o.snapshots)-20:]
	}
	return s
}

func (o *orchestrator) event(kind, msg string) {
	if o.timeline != nil {
		o.timeline.Record(kind, msg)
	}
}

// ---------------------------------------------------------------------
// ShattlBypassAdapter: единственная реализация на сегодня — обёртка над
// vps-mode.sh с health-check и автооткатом.
// ---------------------------------------------------------------------

// healthCheckVPSMode — проверяет, что переключение реально сработало:
//   - mode=on: VPS отвечает на TCP (порт gRPC/Vision), xray живой
//   - mode=off: zapret жив (единственное, что должно продолжать работать)
//
// Никогда не паникует и не возвращает stack trace — только структурированный
// результат (ТЗ п.10/п.8: "Unable to activate X. Previous configuration has
// been restored", без внутренних деталей в основном UI).
func (o *orchestrator) healthCheckVPSMode(mode string) HealthResult {
	if mode == "off" {
		out, err := runCmd("systemctl", "is-active", "zapret.service")
		if err != nil || strings.TrimSpace(out) != "active" {
			return HealthResult{HealthFailed, "zapret.service не активен после переключения"}
		}
		return HealthResult{HealthHealthy, "zapret активен, VPS-маршрутизация отключена по запросу"}
	}

	// mode == "on"
	addr, _ := readConfigVar(o.configEnv, "VPS_ADDR")
	port, _ := readConfigVar(o.configEnv, "VPS_PORT_GRPC")
	if addr == "" {
		return HealthResult{HealthDegraded, "VPS_ADDR не задан в config.env — проверка пропущена"}
	}
	if port == "" {
		port = "443"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(addr, port), 5*time.Second)
	if err != nil {
		return HealthResult{HealthFailed, fmt.Sprintf("VPS %s:%s недоступен: %v", addr, port, err)}
	}
	conn.Close()

	out, err := runCmd("systemctl", "is-active", "xray.service")
	if err != nil || strings.TrimSpace(out) != "active" {
		return HealthResult{HealthFailed, "xray.service не активен после включения VPS-режима"}
	}
	return HealthResult{HealthHealthy, fmt.Sprintf("VPS %s:%s отвечает, xray активен", addr, port)}
}

// applyVPSMode — backup -> apply -> health-check -> rollback (ТЗ п.7-8, п.20).
// Заменяет прямой вызов vps-mode.sh из handleVPSMode.
func (o *orchestrator) applyVPSMode(targetMode string) (HealthResult, error) {
	prevMode := readVPSMode()
	if prevMode == targetMode {
		// нечего менять — не гоняем health-check зря, но и не молчим
		return HealthResult{HealthHealthy, "уже в режиме " + targetMode}, nil
	}

	snap := o.saveSnapshot("vps-mode", "before switching "+prevMode+" -> "+targetMode,
		map[string]string{"mode": prevMode})
	o.event("engine.switch.started", fmt.Sprintf("Traffic Engine: переключение VPS-режима %s → %s", prevMode, targetMode))

	if out, err := runCmd("/opt/gateway/vps-mode.sh", targetMode); err != nil {
		o.event("engine.error", "vps-mode.sh завершился с ошибкой: "+err.Error())
		return HealthResult{HealthFailed, "не удалось применить: " + err.Error() + " (" + out + ")"}, err
	}

	// нативная нестабильность (DNS/маршруты после смены правил) — даём секунду
	time.Sleep(1 * time.Second)
	res := o.healthCheckVPSMode(targetMode)
	if res.ok() {
		o.event("engine.switch.completed", fmt.Sprintf("Traffic Engine: VPS-режим переключён на %s (%s)", targetMode, res.Detail))
		return res, nil
	}

	// health check не прошёл — откат (ТЗ п.8)
	o.event("engine.health.failed", "Traffic Engine: health-check провален после переключения: "+res.Detail)
	o.event("engine.rollback.started", "Traffic Engine: откатываю VPS-режим на "+prevMode)

	if out, err := runCmd("/opt/gateway/vps-mode.sh", snap.Data["mode"]); err != nil {
		// откат тоже не удался — это уже не тихая история, логируем максимально явно
		o.event("engine.error", "КРИТИЧНО: откат VPS-режима не удался: "+err.Error()+" ("+out+")")
		return HealthResult{HealthFailed, "переключение не удалось, И откат не удался — требуется ручное вмешательство: " + err.Error()}, err
	}
	rollbackHealth := o.healthCheckVPSMode(prevMode)
	o.event("engine.rollback.completed", "Traffic Engine: откат выполнен, режим "+prevMode+" ("+rollbackHealth.Detail+")")

	return HealthResult{
		Status: HealthFailed,
		Detail: fmt.Sprintf("Не удалось включить режим %s (%s) — восстановлена предыдущая конфигурация (%s)", targetMode, res.Detail, prevMode),
	}, nil
}

// handleEngineSnapshots — для Advanced Diagnostics (ТЗ п.12): показать, что
// оркестратор реально делал — достаточно отдать сырые снапшоты, страница
// диагностики отрисует список без отдельной жёстко закодированной вьюхи.
func (s *server) handleEngineSnapshots(w http.ResponseWriter, r *http.Request) {
	s.orchestrator.mu.Lock()
	defer s.orchestrator.mu.Unlock()
	out := make([]snapshot, len(s.orchestrator.snapshots))
	copy(out, s.orchestrator.snapshots)
	writeJSON(w, http.StatusOK, out)
}
