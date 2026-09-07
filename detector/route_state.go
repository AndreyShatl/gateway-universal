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
	fs.Parse(os.Args[2:])

	snap, decisions := buildRouteSnapshot(true)
	snap.Decisions = decisions
	if err := writeSnapshot(snap); err != nil {
		fmt.Fprintf(os.Stderr, "brain-observe: снапшот не записан: %v\n", err)
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
