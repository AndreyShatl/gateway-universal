package main

// route_explain.go (T-route-manager-phase2a, 2026-08-18) — первый кирпичик
// Этапа 2 из ТЗ ("общий Route Manager"): read-only диагностика, показывает,
// что говорит про цель КАЖДАЯ подсистема (доменная brain-группа + IP-
// автообход) ОДНОВРЕМЕННО, ничего не меняя. Сейчас у нас нет единого
// источника истины — DomainSubsystem и IPSubsystem пишут в разные файлы
// (brain-services*.json vs autoroute.json) независимо (см. исследование
// перед стартом ТЗ-2: "два независимых владельца routing state" — ровно
// то, что документ называет анти-паттерном, п.18). Настоящее объединение
// (RouteManager с приоритетом/specificity, п.19-27) требует трогать
// доменную bash-цепочку (brain-worker.sh/brain-apply.sh) — сознательно
// оставлено на отдельную сессию, это другой периметр риска. Здесь —
// только видимость: узнать "что решили обе системы" одной командой,
// прежде чем строить механизм арбитража между ними.
import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gateway-detector/applier"
)

type domainVerdict struct {
	Engine   string // zapret | ciadpi | zapret2
	GroupID  string
	Strategy string
}

// findDomainRoute — та же механика, что isBrainEntity(), но возвращает
// подробности (движок/группа/стратегия), не просто bool.
func findDomainRoute(domain string) *domainVerdict {
	domain = strings.ToLower(strings.TrimSpace(domain))
	engines := []struct {
		name string
		path string
	}{
		{"zapret", "/etc/gateway/brain-services.json"},
		{"ciadpi", "/etc/gateway/brain-services-ciadpi.json"},
		{"zapret2", "/etc/gateway/brain-services-zapret2.json"},
	}
	for _, eng := range engines {
		data, err := os.ReadFile(eng.path)
		if err != nil {
			continue
		}
		var groups []struct {
			GroupID  string   `json:"group_id"`
			Strategy string   `json:"strategy"`
			Domains  []string `json:"domains"`
		}
		if json.Unmarshal(data, &groups) != nil {
			continue
		}
		for _, g := range groups {
			for _, d := range g.Domains {
				if strings.EqualFold(d, domain) {
					return &domainVerdict{Engine: eng.name, GroupID: g.GroupID, Strategy: g.Strategy}
				}
			}
		}
	}
	return nil
}

// runRouteExplain — CLI: gateway-detector route-explain <target>.
func runRouteExplain() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: gateway-detector route-explain <domain|ip>")
		os.Exit(2)
	}
	target := strings.ToLower(strings.TrimSpace(os.Args[2]))

	fmt.Printf("=== route-explain: %s ===\n", target)

	// Доменная подсистема
	if dv := findDomainRoute(target); dv != nil {
		fmt.Printf("[domain-подсистема]  DPI, движок=%s группа=%s стратегия=%q\n", dv.Engine, dv.GroupID, dv.Strategy)
	} else if inAutoroute(target) {
		fmt.Printf("[domain-подсистема]  в общем автообходе (VPS) как домен — совпадение по строке в autoroute.json\n")
	} else if inZapretHostlist(target) {
		fmt.Printf("[domain-подсистема]  под hostlist-сервисом (SNI-based), IP не важен\n")
	} else if isWhitelisted(target) {
		fmt.Printf("[domain-подсистема]  whitelisted (.ru/.рф/.su и т.п.) — не анализируется вообще\n")
	} else {
		fmt.Printf("[domain-подсистема]  нет записи (не сущность, не в автообходе, не под hostlist)\n")
	}

	// IP-подсистема (autoroute.json — тот же файл, что inAutoroute() читает
	// для строкового совпадения выше, но здесь смотрим ИМЕННО IP-семантику:
	// port-scoped записи и health/type метаданные, недоступные через
	// простой inAutoroute()).
	s, err := applier.Load()
	if err != nil {
		fmt.Printf("[ip-подсистема]      ошибка чтения autoroute.json: %v\n", err)
	} else {
		found := false
		for _, e := range s.Entries {
			if e.Addr != target {
				continue
			}
			found = true
			kind := "LEARNED"
			if e.IsStatic() {
				kind = "STATIC"
			}
			scope := "весь IP"
			if e.IsPortScoped() {
				scope = fmt.Sprintf("только порт %d/%s", e.DPort, e.Proto)
			}
			state := e.State
			if state == "" {
				state = "UNKNOWN"
			}
			fmt.Printf("[ip-подсистема]      VPS, %s, применение=%s, health=%s (success=%d fail=%d)\n",
				kind, scope, state, e.SuccessCount, e.FailureCount)
		}
		if !found {
			fmt.Printf("[ip-подсистема]      нет записи (не в автообходе)\n")
		}
	}

	// Явный конфликт (ТЗ п.22-25): домен говорит одно, IP-запись с тем же
	// буквальным значением target говорит другое — на практике у нас это
	// редкость (домен матчится по SNI/xray-роутингу, IP-автообход — по
	// dst-адресу, разные механизмы применения), но само наличие обеих
	// записей уже стоит показать явно, не молчать.
	if findDomainRoute(target) != nil && inAutoroute(target) {
		fmt.Println("⚠ ОБЕ подсистемы имеют мнение об этой строке — сейчас применяются НЕЗАВИСИМО (нет арбитража specificity), см. ТЗ Этап 2")
	}
}
