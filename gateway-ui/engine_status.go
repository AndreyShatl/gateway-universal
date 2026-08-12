package main

import (
	"net/http"
	"os"
	"strings"
)

// engine_status.go — агрегированный статус "Traffic Engine" (ТЗ п.11-14):
// пользователь видит одну строку "Traffic Engine ● Healthy", а не список
// systemd-юнитов. Внутренний состав — только в Advanced Mode (см.
// EngineStatus.Components), скрыт по умолчанию.
//
// ciadpi/zapret2 — НЕ демоны (динамические per-домен группы, см.
// комментарий в servicectl на стороне gmp-agent) — 0 активных групп это
// нормальное состояние "движок готов, но пока никому не назначен", а не
// поломка. В Components отражаем "движок установлен" (бинарь на диске), а
// не "хотя бы одна группа запущена" — иначе Advanced-панель врала бы
// красным на свежих установках без реальной причины.
type EngineComponents struct {
	Zapret  bool `json:"zapret"`  // zapret.service
	Xray    bool `json:"xray"`    // xray.service (VPS-туннель)
	CIADPI  bool `json:"ciadpi"`  // движок установлен (/opt/byedpi/ciadpi)
	Zapret2 bool `json:"zapret2"` // движок установлен (/opt/zapret2/...)
	Brain   bool `json:"brain"`   // gateway-brain-worker.service — раздаёт домены по движкам
}

type EngineStatus struct {
	Engine     string            `json:"engine"` // фиксировано "shattl-bypass" — единственный adapter сейчас
	Status     HealthStatus      `json:"status"`
	Detail     string            `json:"detail"`
	Components EngineComponents  `json:"components"`
}

func serviceActive(unit string) bool {
	out, err := runCmd("systemctl", "is-active", unit)
	return err == nil && strings.TrimSpace(out) == "active"
}

func fileExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// shattlBypassStatus — здоровье core-компонентов (zapret/xray/brain должны
// работать ВСЕГДА — их отсутствие снижает статус); ciadpi/zapret2 —
// информационные, не влияют на Healthy/Degraded (см. комментарий выше).
func shattlBypassStatus() EngineStatus {
	c := EngineComponents{
		Zapret:  serviceActive("zapret.service"),
		Xray:    serviceActive("xray.service"),
		CIADPI:  fileExecutable(ciadpiDir + "/ciadpi"),
		Zapret2: fileExecutable(zapret2Dir + "/nfq2/nfqws2"),
		Brain:   serviceActive("gateway-brain-worker.service"),
	}

	var down []string
	if !c.Zapret {
		down = append(down, "zapret")
	}
	if !c.Xray {
		down = append(down, "xray")
	}
	if !c.Brain {
		down = append(down, "мозг")
	}

	switch len(down) {
	case 0:
		return EngineStatus{Engine: "shattl-bypass", Status: HealthHealthy, Detail: "Все компоненты обхода работают", Components: c}
	case 1, 2:
		return EngineStatus{Engine: "shattl-bypass", Status: HealthDegraded, Detail: "Не работает: " + strings.Join(down, ", "), Components: c}
	default:
		return EngineStatus{Engine: "shattl-bypass", Status: HealthFailed, Detail: "Не работает: " + strings.Join(down, ", "), Components: c}
	}
}

func (s *server) handleEngineStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, shattlBypassStatus())
}
