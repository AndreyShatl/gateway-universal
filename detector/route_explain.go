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

// dpiIpsetName — имя ipset для движка+группы (тот же паттерн префиксов,
// что brain-refresh-ips.sh: brain_/brainc_/brainz2_).
func dpiIpsetName(v *domainVerdict) string {
	switch v.Engine {
	case "ciadpi":
		return "brainc_" + v.GroupID
	case "zapret2":
		return "brainz2_" + v.GroupID
	default:
		return "brain_" + v.GroupID
	}
}

// dpiActuallyCoversDomain (T-route-manager-phase2e, 2026-08-18) — живая
// находка: домен "числится" в DPI-группе (findDomainRoute находит его в
// brain-services*.json), но это НЕ значит, что реальный трафик реально
// перехватывается — если ipset группы устарел (та же болезнь, что чинит
// T-cdn-refresh/brain-refresh-ips.sh: ротирующийся CDN, точечный резолв в
// прошлом не поймал текущий IP), домен идёт мимо ipset совсем, DPI не
// применяется, хотя "на бумаге" всё в порядке. reconcileDomainConflicts
// раньше доверял ТОЛЬКО факту членства в группе — снимал VPS-подстраховку
// как "избыточную", хотя она была ЕДИНСТВЕННОЙ реально работающей защитой
// (живой инцидент: scontent.xx.fbcdn.net, Instagram "чёрный экран половины
// видео" — VPS-запись снята при вчерашней чистке конфликтов, DPI-группа
// на бумаге была, но ipset не покрывал текущий IP). Теперь перед снятием
// VPS-подстраховки проверяем ФАКТ: резолвим домен сейчас, требуем чтобы
// ВСЕ текущие IP реально были в ipset группы — не только сам факт
// членства в JSON.
func dpiActuallyCoversDomain(domain string, v *domainVerdict) bool {
	ipset := dpiIpsetName(v)
	ips, err := net.LookupHost(domain)
	if err != nil || len(ips) == 0 {
		return false // не смогли резолвить сейчас — не можем подтвердить покрытие, консервативно считаем "не покрыт"
	}
	for _, ip := range ips {
		if net.ParseIP(ip) == nil || strings.Contains(ip, ":") {
			continue // IPv6/мусор — ipset у нас family inet (v4), не проверяем
		}
		if err := exec.Command("ipset", "test", ipset, ip).Run(); err != nil {
			return false // хотя бы один текущий IP не в ipset — реальное покрытие неполное
		}
	}
	return true
}

// allBrainDomainVerdicts — domain -> *domainVerdict для ВСЕХ доменов во
// ВСЕХ DPI-группах (все три движка), за один проход по файлам (не N
// обращений к диску на каждую цель — reconcileDomainConflicts проверяет
// потенциально тысячи записей). Замена прежней allBrainDomains (только
// bool) — T-route-manager-phase2e теперь нужен Engine/GroupID для проверки
// реального покрытия ipset, не только факт членства в JSON.
func allBrainDomainVerdicts() map[string]*domainVerdict {
	m := map[string]*domainVerdict{}
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
				m[strings.ToLower(d)] = &domainVerdict{Engine: eng.name, GroupID: g.GroupID, Strategy: g.Strategy}
			}
		}
	}
	return m
}

// reconcileDomainConflicts (T-route-manager-phase2b, 2026-08-18, доработано
// T-route-manager-phase2e) — живая находка тем же вечером: 37 доменов
// одновременно были DPI-сущностью (доменная подсистема) И зависшей
// LEARNED-записью в IP-автообходе (VPS) — артефакт более раннего состояния
// (домен получил DPI-стратегию ПОСЛЕ того, как уже попал в автообход по
// старому провалу, запись не снялась сама — recheck пробует DIRECT, не
// DPI, и часто DIRECT реально не работает). Разовая ручная чистка не
// защищает от повторного накопления — теперь это часть regular recheck.
//
// T-route-manager-phase2e (доработка после живого инцидента): раньше
// доверяли ТОЛЬКО факту членства в DPI-группе (JSON) — снимали VPS-
// подстраховку как "избыточную", не проверяя, реально ли ipset группы
// покрывает ТЕКУЩИЕ IP домена. Для доменов с устаревшим ipset (та же
// болезнь, что чинит T-cdn-refresh) это снимало ЕДИНСТВЕННУЮ реально
// работающую защиту (Instagram-инцидент: scontent.xx.fbcdn.net — DPI-
// группа на бумаге была, ipset не покрывал текущий IP, VPS-подстраховку
// сняли, домен остался вообще без защиты). Теперь перед снятием — реальная
// проверка (dpiActuallyCoversDomain): резолвим домен сейчас, требуем чтобы
// ВСЕ текущие IP были в ipset. Не покрыт — оставляем VPS-запись как есть,
// не смотря на формальное членство в группе.
//
// Убирает найденные конфликты: из autoroute.json (через тот же путь, что
// TTL-удаление), из ipset, сбрасывает conntrack. apply=false — только лог
// (тень), сколько нашли бы.
func reconcileDomainConflicts(apply bool) []string {
	brainDomains := allBrainDomainVerdicts()
	s, err := applier.Load()
	if err != nil {
		return nil
	}
	var conflicts []string
	for _, e := range s.Entries {
		if e.IsStatic() {
			continue // STATIC — явное решение оператора, автоматика её не трогает нигде (recheck, PruneStale) — и здесь не должна
		}
		v, ok := brainDomains[strings.ToLower(e.Addr)]
		if !ok {
			continue
		}
		if !dpiActuallyCoversDomain(e.Addr, v) {
			continue // числится в группе, но ipset реально не покрывает текущие IP — VPS-подстраховка нужна, не трогаем
		}
		conflicts = append(conflicts, e.Addr)
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
		covered := "да"
		if !dpiActuallyCoversDomain(target, dv) {
			covered = "НЕТ — числится в группе, но ipset не покрывает текущие резолвящиеся IP (см. T-cdn-refresh/brain-refresh-ips.sh)"
		}
		fmt.Printf("[domain-подсистема]  DPI, движок=%s группа=%s стратегия=%q реально_покрыт=%s\n", dv.Engine, dv.GroupID, dv.Strategy, covered)
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
