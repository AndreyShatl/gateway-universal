package main

// route_state.go (Этап 5 / STAGE 4 OBSERVE из схемы Shattl Brain, 2026-09-07) —
// единый read-only снапшот состояния всех известных назначений + PolicyEngine-
// скелет в режиме «я бы решил». NOTHING here mutates routing: ни autoroute,
// ни ipset, ни очередь мозга. Единственная запись — собственный снапшот-файл
// /etc/gateway/observe/route-state.json (пересоздаётся, читается gateway-ui).
//
// Зачем: состояние маршрута назначения размазано по четырём источникам
// (brain-services*.json = LOCAL-группы, autoroute.json = VPS-автообход с
// health-state, gateway.db services = confidence/next_reeval, xray
// config.json = статический VPS-список). Схема требует per-destination
// state machine (раздел 7/16) — это первый кирпич: собрать картину одной
// командой, прежде чем строить арбитраж (STAGE 5+).

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gateway-detector/applier"
)

// --- источники (те же пути, что остальной код детектора) ---

const (
	xrayConfigPath    = "/opt/xray/config.json"
	aiServicesList    = "/root/gateway-universal/xray/domains/ai-services.txt"
	observeStateDir   = "/etc/gateway/observe"
	vpsHealthyMinAge  = 72 * time.Hour // R2: VPS-домен HEALTHY дольше этого — кандидат на ночную попытку LOCAL
	coverageMaxPerRun = 250            // R1: не больше N DNS+ipset проверок за прогон (все DPI-домены сразу не рвём)
)

var observeStateFile = observeStateDir + "/route-state.json"

// --- модель снапшота (раздел 7 схемы: current_route/why/confidence/checks) ---

type VPSAutoInfo struct {
	Source             string `json:"source,omitempty"`
	State              string `json:"state,omitempty"` // UNKNOWN|HEALTHY|DEGRADED|FAILED
	FailureCount       int    `json:"failure_count,omitempty"`
	ConsecutiveFailure int    `json:"consecutive_failures,omitempty"`
	LastSuccess        string `json:"last_success,omitempty"`
	LastFailure        string `json:"last_failure,omitempty"`
	Added              string `json:"added,omitempty"`
	LastSeen           string `json:"last_seen,omitempty"`
	Static             bool   `json:"static,omitempty"` // STATIC — операторская, автоматика не трогает
	PortScoped         bool   `json:"port_scoped,omitempty"`
}

type DestinationState struct {
	Domain             string       `json:"domain"`
	CurrentRoute       string       `json:"current_route"` // local_zapret|local_ciadpi|local_zapret2|vps_auto|vps_static|conflict
	Engine             string       `json:"engine,omitempty"`
	GroupID            string       `json:"group_id,omitempty"`
	Strategy           string       `json:"strategy,omitempty"`
	Pinned             bool         `json:"pinned,omitempty"`           // ai-services: VPS навсегда, схемы п. «пиннед-категория»
	ActuallyCovered    *bool        `json:"actually_covered,omitempty"` // nil = не проверяли в этом прогоне
	VPSAuto            *VPSAutoInfo `json:"vps_auto,omitempty"`
	VPSFallbackDormant bool         `json:"vps_fallback_dormant,omitempty"` // в DPI-группе И в VPS-автообходе (RETURN выигрывает)
	Confidence         int          `json:"confidence,omitempty"`
	LastReeval         string       `json:"last_reeval_at,omitempty"`
	NextReeval         string       `json:"next_reeval_at,omitempty"`
	Reasons            []string     `json:"reasons,omitempty"`
}

type RouteTotals struct {
	LocalZapret   int `json:"local_zapret"`
	LocalCiadpi   int `json:"local_ciadpi"`
	LocalZapret2  int `json:"local_zapret2"`
	VPSAutoDomain int `json:"vps_auto_domain"`
	VPSAutoIP     int `json:"vps_auto_ip"` // чистые IP/порты — не домены, считаем отдельно
	VPSStatic     int `json:"vps_static"`  // xray config.json proxy-mux (без пиннед-подробностей)
	Conflicts     int `json:"conflicts"`
}

type ObserveDecision struct {
	Rule   string `json:"rule"`
	Domain string `json:"domain,omitempty"`
	Action string `json:"action"` // would_keep_vps|would_enqueue_local|no_action|conflict_reported
	Reason string `json:"reason"`
}

type RouteSnapshot struct {
	Generated    time.Time          `json:"generated"`
	Totals       RouteTotals        `json:"totals"`
	Destinations []DestinationState `json:"destinations"`
	Decisions    []ObserveDecision  `json:"decisions,omitempty"` // заполняется в brain-observe
}

// --- сборка снапшота ---

func staticVPSDomains() map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(xrayConfigPath)
	if err != nil {
		return out
	}
	var cfg struct {
		Routing struct {
			Rules []struct {
				OutboundTag string   `json:"outboundTag"`
				Domain      []string `json:"domain"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return out
	}
	for _, r := range cfg.Routing.Rules {
		if r.OutboundTag != "proxy-mux" {
			continue
		}
		for _, d := range r.Domain {
			d = strings.ToLower(strings.TrimPrefix(strings.TrimSuffix(d, ","), "domain:"))
			d = strings.TrimPrefix(d, "full:")
			if d != "" && !strings.Contains(d, "*") {
				out[d] = true
			}
		}
	}
	return out
}

func pinnedAISet() map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(aiServicesList)
	if err != nil {
		return out
	}
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.ToLower(strings.TrimSpace(ln))
		if ln != "" && !strings.HasPrefix(ln, "#") {
			out[ln] = true
		}
	}
	return out
}

// gwdbServices — services-list TSV: domain, strategy_id, engine, strategy_name,
// confidence, last_reeval_at, next_reeval_at (тот же контракт, что gateway-ui).
func gwdbServices() map[string][5]string {
	out := map[string][5]string{}
	cmd := exec.Command("python3", gwdbScript, "services-list")
	raw, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		f := strings.Split(ln, "\t")
		if len(f) != 7 || f[0] == "" {
			continue
		}
		out[strings.ToLower(f[0])] = [5]string{f[2], f[3], f[4], f[5], f[6]} // engine, strategy_name, confidence, last_reeval, next_reeval
	}
	return out
}

func buildRouteSnapshot(checkCoverage bool) (*RouteSnapshot, []ObserveDecision) {
	snap := &RouteSnapshot{Generated: time.Now().UTC()}
	decisions := []ObserveDecision{}

	brain := allBrainDomainVerdicts() // domain -> LOCAL маршрут
	statics := staticVPSDomains()     // статический VPS-список xray
	pinned := pinnedAISet()           // ai-services — навсегда VPS
	svc := gwdbServices()             // confidence/next_reeval
	store, err := applier.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "route-state: autoroute.json не читается: %v\n", err)
		store = applier.Store{}
	}

	// VPS-автообход: доменные записи — в destinations, чистые IP — в счётчик
	vpsAuto := map[string]*applier.Entry{}
	for i := range store.Entries {
		e := &store.Entries[i]
		if net.ParseIP(e.Addr) != nil || strings.Contains(e.Addr, "/") || strings.HasPrefix(e.Addr, "geosite:") {
			snap.Totals.VPSAutoIP++
			continue
		}
		vpsAuto[strings.ToLower(e.Addr)] = e
	}

	union := map[string]*DestinationState{}
	get := func(d string) *DestinationState {
		if s, ok := union[d]; ok {
			return s
		}
		s := &DestinationState{Domain: d}
		union[d] = s
		return s
	}

	for d, v := range brain {
		s := get(d)
		s.CurrentRoute = "local_" + v.Engine
		s.Engine, s.GroupID, s.Strategy = v.Engine, v.GroupID, v.Strategy
	}
	for d, e := range vpsAuto {
		s := get(d)
		if s.CurrentRoute != "" {
			// домен И в DPI-группе, И в VPS-автообходе: iptables RETURN
			// выигрывает у REDIRECT — VPS-запись это дремлющий фолбэк (не баг)
			s.VPSFallbackDormant = true
			s.Reasons = append(s.Reasons, "vps_fallback_dormant: числится и в DPI-группе, и в VPS-автообходе; RETURN выше REDIRECT")
			snap.Totals.Conflicts++
			continue
		}
		s.CurrentRoute = "vps_auto"
		info := &VPSAutoInfo{
			Source: e.Source, State: e.State, FailureCount: e.FailureCount,
			ConsecutiveFailure: e.ConsecutiveFailures, LastSuccess: e.LastSuccess,
			LastFailure: e.LastFailure, Added: e.Added, LastSeen: e.LastSeen,
			Static: e.IsStatic(), PortScoped: e.IsPortScoped(),
		}
		s.VPSAuto = info
		if e.IsStatic() {
			s.Pinned = true
			s.Reasons = append(s.Reasons, "static: операторская запись, автоматика не трогает")
		}
	}
	for d := range statics {
		s := get(d)
		if s.CurrentRoute != "" {
			// есть в статическом списке И уже под мозгом/VPS-автообходом —
			// дремлющий фолбэк (см. Архитектура.md «Три списка доменов»)
			if s.VPSAuto == nil {
				s.Reasons = append(s.Reasons, "vps_static_dormant: в статическом xray-списке, но перехватывается раньше")
			}
			continue
		}
		s.CurrentRoute = "vps_static"
		if pinned[d] {
			s.Pinned = true
			s.Reasons = append(s.Reasons, "pinned: ai-services — VPS всегда, осознанное решение (DECISIONS/Архитектура)")
		}
	}
	for d, g := range svc {
		s := get(d)
		s.Confidence = atoiSafe(g[2])
		s.LastReeval = g[3]
		s.NextReeval = g[4]
		switch {
		case s.CurrentRoute == "":
			// в services БД, но маршрута нет (переехал на VPS руками/мозгом,
			// или Whitelist) — не выдумываем, помечаем для наблюдения
			s.CurrentRoute = "vps_auto"
			s.Reasons = append(s.Reasons, "in_services_db_no_route: есть в gateway.db, маршрута в JSON нет — наблюдать")
		}
	}

	// R1: реальное покрытие ipset у LOCAL-доменов (ограничение по числу проверок)
	checked := 0
	for d, s := range union {
		if !strings.HasPrefix(s.CurrentRoute, "local_") || !checkCoverage || checked >= coverageMaxPerRun {
			continue
		}
		checked++
		v := brain[d]
		cov := dpiActuallyCoversDomain(d, v)
		s.ActuallyCovered = &cov
		if !cov {
			decisions = append(decisions, ObserveDecision{
				Rule: "R1_dpi_not_covering", Domain: d, Action: "would_keep_vps",
				Reason: "числится в DPI-группе, но ipset не покрывает текущие IP (CDN-ротация?) — держал бы VPS-подстраховку, ждал brain-refresh-ips",
			})
		}
	}

	// R2: VPS-автообход домена стабильно HEALTHY давно — ночной кандидат на LOCAL
	for d, s := range union {
		if s.CurrentRoute != "vps_auto" || s.Pinned || s.VPSAuto == nil {
			continue
		}
		if s.VPSAuto.State == "HEALTHY" && s.VPSAuto.LastSuccess != "" {
			if t, err := time.Parse(time.RFC3339, s.VPSAuto.LastSuccess); err == nil && time.Since(t) > vpsHealthyMinAge {
				decisions = append(decisions, ObserveDecision{
					Rule: "R2_vps_stable_try_local", Domain: d, Action: "would_enqueue_local",
					Reason: fmt.Sprintf("VPS-автообход HEALTHY уже %s — поставил бы в ночную очередь на попытку LOCAL", time.Since(t).Round(time.Hour)),
				})
			}
		}
	}

	// R3/R5 фиксируются итоговыми счётчиками; R4 (двойной учёт) уже в Reasons.

	for _, s := range union {
		switch {
		case strings.HasPrefix(s.CurrentRoute, "local_zapret2"):
			snap.Totals.LocalZapret2++
		case strings.HasPrefix(s.CurrentRoute, "local_ciadpi"):
			snap.Totals.LocalCiadpi++
		case strings.HasPrefix(s.CurrentRoute, "local_"):
			snap.Totals.LocalZapret++
		case s.CurrentRoute == "vps_auto":
			snap.Totals.VPSAutoDomain++
		case s.CurrentRoute == "vps_static":
			snap.Totals.VPSStatic++
		}
	}
	snap.Totals.VPSStatic += 0 // статические, уже перехваченные мозгом, учтены выше как local

	snap.Destinations = make([]DestinationState, 0, len(union))
	for _, s := range union {
		snap.Destinations = append(snap.Destinations, *s)
	}
	sort.Slice(snap.Destinations, func(i, j int) bool {
		return snap.Destinations[i].Domain < snap.Destinations[j].Domain
	})
	return snap, decisions
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func writeSnapshot(snap *RouteSnapshot) error {
	if err := os.MkdirAll(observeStateDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := observeStateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, observeStateFile)
}

// --- подкоманды ---

// route-state: снапшот в stdout (JSON). --coverage — проверять реальное
// покрытие ipset у LOCAL-доменов (DNS-резолвы, до 250 за прогон).
func runRouteState() {
	fs := flag.NewFlagSet("route-state", flag.ExitOnError)
	coverage := fs.Bool("coverage", false, "проверять реальное покрытие ipset (DNS+ipset на каждый LOCAL-домен)")
	out := fs.String("out", "", "дополнительно записать снапшот в файл (atomic)")
	fs.Parse(os.Args[2:])
	snap, _ := buildRouteSnapshot(*coverage)
	if *out != "" {
		old := observeStateFile
		observeStateFile = *out
		if err := writeSnapshot(snap); err != nil {
			fmt.Fprintf(os.Stderr, "route-state: запись снапшота: %v\n", err)
		}
		observeStateFile = old
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(snap)
}

// brain-observe (STAGE 4 из схемы): применить правила видения к снапшоту и
// ТОЛЬКО залогировать решения. Ноль мутаций маршрутизации. Снапшот пишется в
// /etc/gateway/observe/route-state.json для gateway-ui.
func runBrainObserve() {
	fs := flag.NewFlagSet("brain-observe", flag.ExitOnError)
	coverage := fs.Bool("coverage", false, "проверять реальное покрытие ipset (медленно: DNS-резолвы, ~6 мин на 1300 доменов)")
	fs.Parse(os.Args[2:])

	snap, decisions := buildRouteSnapshot(*coverage)
	snap.Decisions = decisions
	if err := writeSnapshot(snap); err != nil {
		fmt.Fprintf(os.Stderr, "brain-observe: снапшот не записан: %v\n", err)
	}
	// STAGE 5: переходы решений (new/resolved) в decisions.jsonl — только
	// по правилам, реально вычисленным в этом прогоне (см. journalDecisions)
	events := journalDecisions(decisions, evaluatedRuleSet(decisions, *coverage))
	if len(events) > 0 {
		fmt.Printf("переходы решений: %d (журнал: %s)\n", len(events), decisionsLog)
	}

	fmt.Printf("brain-observe (STAGE 4, ноль мутаций) @ %s\n", snap.Generated.Format("2006-01-02 15:04:05Z07:00"))
	t := snap.Totals
	fmt.Printf("итого: LOCAL zapret=%d ciadpi=%d zapret2=%d | VPS auto(домены)=%d auto(IP/порты)=%d static=%d | конфликтов(двойной учёт)=%d\n",
		t.LocalZapret, t.LocalCiadpi, t.LocalZapret2, t.VPSAutoDomain, t.VPSAutoIP, t.VPSStatic, t.Conflicts)
	if len(decisions) == 0 {
		fmt.Println("решения: нет — всё стабильно, правилам реагировать не на что")
	}
	for _, d := range decisions {
		fmt.Printf("[%s] %-35s %s: %s\n", d.Rule, d.Domain, d.Action, d.Reason)
	}
	fmt.Printf("снапшот: %s (%d назначений)\n", filepath.Base(observeStateFile), len(snap.Destinations))
}

// ============================================================================
// STAGE 5 SHADOW (2026-09-07): журнал решений + ночная сверка с реальностью.
//
// Схема владельца, раздел 30: Shadow Mode = «мозг принимает решения параллельно
// существующей системе и сравнивает результаты». Здесь это материализовано так:
//   - brain-observe пишет ПЕРЕХОДЫ решений (появилось/пропало) в decisions.jsonl
//     — не каждый прогон целиком (шум), а только изменения состояния;
//   - shadow-verify (ночами, после coverage-прогона и ночной цепочки мозга)
//     сверяет: R1-домены всё ещё не покрыты? (refresh-ips справился?) и
//     R2-домены реально переехали на LOCAL ночью? — отчёт в shadow-report.json.
// По-прежнему ноль мутаций маршрутизации: только свои файлы в /etc/gateway/observe.
// ============================================================================

const (
	lastDecisionsFile = observeStateDir + "/last-decisions.json"
	decisionsLog      = observeStateDir + "/decisions.jsonl"
	shadowReportFile  = observeStateDir + "/shadow-report.json"
	shadowHistoryDays = 7
)

// DecisionEvent — строка decisions.jsonl. Один переход: решение появилось
// (new) или исчезло (resolved: домен починился/переехал/ушёл из группы).
type DecisionEvent struct {
	TS     string `json:"ts"`
	Event  string `json:"event"` // new|resolved
	Rule   string `json:"rule"`
	Domain string `json:"domain"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// journalDecisions — дифф против last-decisions.json ПОДМНОЖЕСТВОМ правил,
// реально вычисленных в этом прогоне (быстрый прогон считает только R2;
// R1 живёт в coverage-прогоне). Без этого быстрый прогон каждые 30 минут
// стирал бы R1-решения как «resolved» и через сутки создавал их заново.
// Возвращает события для лога.
func journalDecisions(decisions []ObserveDecision, evaluatedRules map[string]bool) []DecisionEvent {
	now := time.Now().UTC().Format(time.RFC3339)
	last := map[string]ObserveDecision{}
	if raw, err := os.ReadFile(lastDecisionsFile); err == nil {
		var prev []ObserveDecision
		if json.Unmarshal(raw, &prev) == nil {
			for _, d := range prev {
				last[d.Rule+"|"+d.Domain] = d
			}
		}
	}
	// новые текущие — только по вычисленным правилам
	cur := map[string]ObserveDecision{}
	for _, d := range decisions {
		if !evaluatedRules[d.Rule] {
			continue
		}
		cur[d.Rule+"|"+d.Domain] = d
	}
	var events []DecisionEvent
	// появившиеся (в текущем есть, в прошлых по этим правилам нет)
	for k, d := range cur {
		if _, ok := last[k]; !ok {
			events = append(events, DecisionEvent{TS: now, Event: "new", Rule: d.Rule, Domain: d.Domain, Action: d.Action, Reason: d.Reason})
		}
	}
	// исчезнувшие: были в last ПО ЭТИМ ЖЕ правилам, в текущих нет
	for k, d := range last {
		if !evaluatedRules[d.Rule] {
			continue // правило в этом прогоне не вычислялось — не трогаем
		}
		if _, ok := cur[k]; !ok {
			events = append(events, DecisionEvent{TS: now, Event: "resolved", Rule: d.Rule, Domain: d.Domain, Action: d.Action, Reason: d.Reason})
		}
	}
	// объединение: нетронутые правила остаются от last, вычисленные — от cur
	merged := make([]ObserveDecision, 0, len(last)+len(cur))
	seenCur := map[string]bool{}
	for _, d := range cur {
		merged = append(merged, d)
		seenCur[d.Rule+"|"+d.Domain] = true
	}
	for _, d := range last {
		if !seenCur[d.Rule+"|"+d.Domain] {
			merged = append(merged, d)
		}
	}
	if raw, err := json.MarshalIndent(merged, "", "  "); err == nil {
		os.MkdirAll(observeStateDir, 0o755)
		os.WriteFile(lastDecisionsFile, raw, 0o644)
	}
	if len(events) > 0 {
		f, err := os.OpenFile(decisionsLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			defer f.Close()
			for _, ev := range events {
				if b, err := json.Marshal(ev); err == nil {
					f.Write(append(b, '\n'))
				}
			}
		}
	}
	return events
}

func evaluatedRuleSet(decisions []ObserveDecision, coverage bool) map[string]bool {
	rules := map[string]bool{}
	for _, d := range decisions {
		rules[d.Rule] = true
	}
	if coverage {
		rules["R1_dpi_not_covering"] = true // даже если пусто — покрытие реально проверяли
	}
	return rules
}

// --- shadow-verify: ночная сверка «что бы решил» vs «что реально произошло» ---

type R1Verify struct {
	Domain     string `json:"domain"`
	FirstSeen  string `json:"first_seen"`
	LastNew    string `json:"last_new"`
	DaysOpen   int    `json:"days_open"`
	NowCovered *bool  `json:"now_covered"` // свежая перепроверка в момент отчёта
	Outcome    string `json:"outcome"`     // still_open|healed_pending_resolved
}

type R2Verify struct {
	Domain   string `json:"domain"`
	Decided  string `json:"decided_at"`
	RouteNow string `json:"route_now"`
	Outcome  string `json:"outcome"`                // moved_to_local|still_vps|gone
	History  string `json:"history_last,omitempty"` // последняя реальная проба мозга
}

type ShadowReport struct {
	Generated time.Time  `json:"generated"`
	R1        []R1Verify `json:"r1"`
	R2        []R2Verify `json:"r2"`
	Notes     []string   `json:"notes"`
}

func loadDecisionEvents() []DecisionEvent {
	var out []DecisionEvent
	raw, err := os.ReadFile(decisionsLog)
	if err != nil {
		return out
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		if ln == "" {
			continue
		}
		var ev DecisionEvent
		if json.Unmarshal([]byte(ln), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}

func gwdbHistoryLast(domain string, n int) string {
	out, err := exec.Command("python3", gwdbScript, "history-last", domain, fmt.Sprint(n)).Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return ""
	}
	return strings.Join(lines, "; ")
}

// runShadowVerify — сравнить observe-решения последних N дней с реальностью.
// Вызов: после ночного brain-observe --coverage (04:40) и ночной цепочки мозга.
func runShadowVerify() {
	fs := flag.NewFlagSet("shadow-verify", flag.ExitOnError)
	fs.Parse(os.Args[2:])

	report := ShadowReport{Generated: time.Now().UTC(), Notes: []string{}}
	events := loadDecisionEvents()
	cutoff := time.Now().UTC().AddDate(0, 0, -shadowHistoryDays)

	// R1: открытые (последнее событие new, без resolved после него)
	type span struct{ firstNew, lastNew time.Time }
	openR1 := map[string]*span{}
	resolvedAfter := map[string]bool{}
	for _, ev := range events {
		if ev.Rule != "R1_dpi_not_covering" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, ev.TS)
		if err != nil || ts.Before(cutoff) {
			continue
		}
		switch ev.Event {
		case "new":
			if _, ok := openR1[ev.Domain]; !ok {
				openR1[ev.Domain] = &span{firstNew: ts, lastNew: ts}
			} else {
				openR1[ev.Domain].lastNew = ts
			}
			resolvedAfter[ev.Domain] = false
		case "resolved":
			resolvedAfter[ev.Domain] = true
		}
	}

	// свежий быстрый снапшот для текущих маршрутов
	snap, _ := buildRouteSnapshot(false)
	routes := map[string]string{}
	for _, d := range snap.Destinations {
		routes[d.Domain] = d.CurrentRoute
	}

	// открытые R1: перепроверить покрытие прямо сейчас (их обычно десятки)
	sortedR1 := make([]string, 0, len(openR1))
	for d := range openR1 {
		if !resolvedAfter[d] {
			sortedR1 = append(sortedR1, d)
		}
	}
	sort.Strings(sortedR1)
	for _, d := range sortedR1 {
		v := R1Verify{Domain: d, FirstSeen: openR1[d].firstNew.Format(time.RFC3339), LastNew: openR1[d].lastNew.Format(time.RFC3339), DaysOpen: int(time.Since(openR1[d].firstNew).Hours() / 24)}
		cov := false
		if verdict := allBrainDomainVerdicts()[d]; verdict != nil {
			cov = dpiActuallyCoversDomain(d, verdict)
		}
		v.NowCovered = &cov
		if cov {
			v.Outcome = "healed_pending_resolved" // refresh-ips долечил, ждём resolved в ближайшем coverage-прогоне
		} else {
			v.Outcome = "still_open"
		}
		report.R1 = append(report.R1, v)
		if !cov && v.DaysOpen >= 1 {
			report.Notes = append(report.Notes, fmt.Sprintf("R1 %s открыт уже %d дн. — brain-refresh-ips не справляется, кандидат на CDN_CIDR_HINTS (наблюдение, не действие)", d, v.DaysOpen))
		}
	}

	// R2: домены с решением за N дней — переехали ли на LOCAL
	r2seen := map[string]string{}
	var r2order []string
	for _, ev := range events {
		if ev.Rule != "R2_vps_stable_try_local" || ev.Event != "new" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, ev.TS)
		if err != nil || ts.Before(cutoff) {
			continue
		}
		if _, ok := r2seen[ev.Domain]; !ok {
			r2order = append(r2order, ev.Domain)
		}
		r2seen[ev.Domain] = ev.TS
	}
	sort.Strings(r2order)
	for _, d := range r2order {
		v := R2Verify{Domain: d, Decided: r2seen[d]}
		route, ok := routes[d]
		if !ok {
			v.RouteNow, v.Outcome = "", "gone" // исчез из всех источников (снята/переименован)
		} else {
			v.RouteNow = route
			switch {
			case strings.HasPrefix(route, "local_"):
				v.Outcome = "moved_to_local" // ночной мозг согласился с observe
			case route == "vps_auto" || route == "vps_static":
				v.Outcome = "still_vps" // ночная попытка не удалась или ещё не было
			default:
				v.Outcome = "route_" + route
			}
		}
		v.History = gwdbHistoryLast(d, 3)
		report.R2 = append(report.R2, v)
	}

	os.MkdirAll(observeStateDir, 0o755)
	if raw, err := json.MarshalIndent(report, "", "  "); err == nil {
		os.WriteFile(shadowReportFile, raw, 0o644)
	}

	// человекочитаемый итог
	fmt.Printf("shadow-verify @ %s (окно %d дн.)\n", report.Generated.Format("2006-01-02 15:04"), shadowHistoryDays)
	still, healed := 0, 0
	for _, r := range report.R1 {
		if r.Outcome == "still_open" {
			still++
		} else {
			healed++
		}
	}
	fmt.Printf("R1 (DPI не покрывает): открытых=%d, из них всё ещё=%d, долечилось refresh-ips=%d\n", len(report.R1), still, healed)
	for _, r := range report.R1 {
		mark := "✓ долечен"
		if r.Outcome == "still_open" {
			mark = fmt.Sprintf("⚠ открыт %d дн.", r.DaysOpen)
		}
		fmt.Printf("  %-38s %s\n", r.Domain, mark)
	}
	moved, stillVps := 0, 0
	for _, r := range report.R2 {
		switch r.Outcome {
		case "moved_to_local":
			moved++
		case "still_vps":
			stillVps++
		}
	}
	fmt.Printf("R2 (VPS→LOCAL кандидаты): переехали=%d, остались на VPS=%d\n", moved, stillVps)
	for _, r := range report.R2 {
		fmt.Printf("  %-38s %s (%s)\n", r.Domain, r.Outcome, r.RouteNow)
	}
	for _, n := range report.Notes {
		fmt.Printf("  note: %s\n", n)
	}
	fmt.Printf("отчёт: %s\n", shadowReportFile)
}
