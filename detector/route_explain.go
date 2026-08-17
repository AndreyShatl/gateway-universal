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
	"net"
	"os"
	"os/exec"
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

// allBrainDomains — множество всех доменов, входящих в ЛЮБУЮ DPI-группу
// (все три движка). Используется reconcileDomainConflicts ниже — так же,
// как findDomainRoute выше, но за один проход по всем целям сразу (не
// по одной, N обращений к диску на каждую).
func allBrainDomains() map[string]bool {
	set := map[string]bool{}
	for _, path := range []string{
		"/etc/gateway/brain-services.json",
		"/etc/gateway/brain-services-ciadpi.json",
		"/etc/gateway/brain-services-zapret2.json",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var groups []struct {
			Domains []string `json:"domains"`
		}
		if json.Unmarshal(data, &groups) != nil {
			continue
		}
		for _, g := range groups {
			for _, d := range g.Domains {
				set[strings.ToLower(d)] = true
			}
		}
	}
	return set
}

// reconcileDomainConflicts (T-route-manager-phase2b, 2026-08-18) — живая
// находка тем же вечером: 37 доменов одновременно были DPI-сущностью
// (доменная подсистема) И зависшей LEARNED-записью в IP-автообходе (VPS) —
// артефакт более раннего состояния (домен получил DPI-стратегию ПОСЛЕ
// того, как уже попал в автообход по старому провалу, запись не снялась
// сама — recheck пробует DIRECT, не DPI, и часто DIRECT реально не
// работает, так что "снятие по чистому direct" никогда не срабатывает,
// хотя DPI уже чинит проблему другим путём). Разовая ручная чистка не
// защищает от повторного накопления — теперь это часть regular recheck.
// Убирает найденные конфликты: из autoroute.json (через тот же путь, что
// TTL-удаление), из ipset, сбрасывает conntrack. apply=false — только лог
// (тень), сколько нашли бы.
func reconcileDomainConflicts(apply bool) []string {
	brainDomains := allBrainDomains()
	s, err := applier.Load()
	if err != nil {
		return nil
	}
	var conflicts []string
	for _, e := range s.Entries {
		if brainDomains[strings.ToLower(e.Addr)] {
			conflicts = append(conflicts, e.Addr)
		}
	}
	if len(conflicts) == 0 || !apply {
		return conflicts
	}
	remove := map[string]bool{}
	for _, c := range conflicts {
		remove[c] = true
	}
	applier.UpdateClean(remove, nil, nil)
	// ipset хранит РЕЗОЛВЛЕННЫЕ IP, не сами доменные строки — резолвим
	// каждый конфликтующий домен и чистим и ipset, и conntrack (та же
	// причина, что везде в этой сессии: залипшая conntrack-запись не даст
	// новому соединению увидеть новый маршрут, пока не истечёт сама).
	for _, addr := range conflicts {
		for _, ip := range resolveV4ForCleanup(addr) {
			exec.Command("ipset", "del", applier.IPSet, ip, "-exist").Run()
			exec.Command("conntrack", "-D", "-d", ip).Run()
		}
	}
	if reloaded, err := applier.Load(); err == nil && reloaded.RouteOn() {
		applier.Sync(reloaded.Entries)
	}
	return conflicts
}

func resolveV4ForCleanup(host string) []string {
	if ip := net.ParseIP(host); ip != nil {
		return []string{host}
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil
	}
	var v4 []string
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			v4 = append(v4, a)
		}
	}
	return v4
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
