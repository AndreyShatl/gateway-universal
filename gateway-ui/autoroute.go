package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// applier: применяем авто-обход БЕЗ рестарта xray.
//   - выделенный inbound autoroute-in :12347 (в шаблоне) всегда идёт в proxy-mux;
//   - iptables редиректит клиентский tcp 80,443 к dst из ipset gw_autoroute → :12347;
//   - добавление адреса = ipset add (мгновенно). Домены резолвятся в IP (dnscrypt).
const (
	autorouteIPSet   = "gw_autoroute"
	autorouteLAN     = "192.168.0.0/16"
	autoroutePort    = "12347" // dokodemo autoroute-in (TCP REDIRECT)
	autorouteUDPPort = "12346" // существующий tproxy-udp inbound -> proxy-mux
	autorouteMark    = "0x1/0xffffffff"
)

// autorouteProtectedIPs — адреса, которые нельзя добавлять в gw_autoroute ни
// при каких обстоятельствах: xray безусловно заворачивает весь трафик с
// tproxy-udp в proxy-mux (VPS), без исключений по домену/порту. Обнаружено
// на практике (2026-08-09/10): DoH-домены из списка автообхода (dns.google,
// one.one.one.one, cloudflare-dns.com и т.п.) резолвятся AdGuardHome в
// 127.0.0.1 (анти-DoH-перехват) — этот адрес затем безусловно попадал в
// ipset при каждом sync, что заворачивало ВЕСЬ локальный DNS в VPS-туннель
// и полностью ронял интернет клиентам. См. такую же защиту в
// detector/applier/applier.go (isProtected) — здесь отдельная копия логики
// авто-обхода в gateway-ui, требует своей проверки.
var autorouteProtectedIPs = map[string]bool{
	"127.0.0.1":       true,
	"::1":             true,
	"8.8.8.8":         true,
	"8.8.4.4":         true,
	"1.1.1.1":         true,
	"1.0.0.1":         true,
	"9.9.9.9":         true,
	"9.9.9.10":        true,
	"149.112.112.112": true,
	"208.67.222.222":  true,
	"208.67.220.220":  true,
}

// autorouteProtected — true, если addr нельзя добавлять в gw_autoroute.
func autorouteProtected(addr string) bool {
	if autorouteProtectedIPs[addr] {
		return true
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

// ensureAutorouteInfra — ipset + iptables (TCP REDIRECT + UDP TPROXY), идемпотентно.
func (s *server) ensureAutorouteInfra() {
	runCmd("ipset", "create", autorouteIPSet, "hash:net", "family", "inet", "-exist")
	// TCP: все порты (не только 80,443) — игровые серверы блокируются по IP на своих
	// портах; dokodemo autoroute-in с followRedirect форвардит любой порт на VPS.
	s.ensureAutorouteRule("nat", []string{"-s", autorouteLAN, "-p", "tcp",
		"-m", "set", "--match-set", autorouteIPSet, "dst", "-j", "REDIRECT", "--to-ports", autoroutePort})
	// UDP: TPROXY на существующий tproxy-udp :12346 (-> proxy-mux). Маршрут
	// fwmark 0x1 -> table 100 -> lo уже настроен основным tproxy-потоком.
	s.ensureAutorouteRule("mangle", []string{"-s", autorouteLAN, "-p", "udp",
		"-m", "set", "--match-set", autorouteIPSet, "dst",
		"-j", "TPROXY", "--on-port", autorouteUDPPort, "--on-ip", "0.0.0.0", "--tproxy-mark", autorouteMark})
}

func (s *server) ensureAutorouteRule(table string, spec []string) {
	if _, err := runCmd("iptables", append([]string{"-t", table, "-C", "PREROUTING"}, spec...)...); err != nil {
		runCmd("iptables", append([]string{"-t", table, "-I", "PREROUTING", "1"}, spec...)...)
	}
}

// syncAutoroute — привести ipset в соответствие со списком + запустить/остановить
// детектор по тумблеру (вызывается на каждое изменение и при старте gateway-ui).
func (s *server) syncAutoroute(a autoRoute) {
	// детектор (пополнение) — независимо от маршрута
	if a.detect() {
		runCmd("systemctl", "start", "gateway-detector.service")
	} else {
		runCmd("systemctl", "stop", "gateway-detector.service")
	}
	// маршрут (применение) — независимо от детектора
	if !a.route() {
		runCmd("ipset", "flush", autorouteIPSet) // список остаётся в файле, но не применяется
		return
	}
	s.ensureAutorouteInfra()
	runCmd("ipset", "flush", autorouteIPSet)
	for _, e := range a.Entries {
		addr := e.Addr
		if strings.HasPrefix(addr, "geosite:") {
			continue // geosite не резолвится в IP — только через xray-роутинг (не наш путь)
		}
		// T-route-manager-phase2d: port-scoped UDP-записи (Proto="udp",
		// T-ip-engine-phase1e) живут в ОТДЕЛЬНОМ ipset (gw_autoroute_udp_pp,
		// hash:ip,port) — детектор сам его поддерживает своим recheck-циклом.
		// Если добавить их сюда как обычный adres, они попадут в whole-IP
		// ipset (autorouteIPSet) и потянут за собой ВЕСЬ трафик к этому IP
		// на VPS, а не только игровой порт — ровно то, ради чего вводился
		// port-scoping. Пропускаем, не дублируя вторую ipset-инфраструктуру
		// здесь — это забота detector/applier, не gateway-ui.
		if e.Proto == "udp" && e.Port > 0 {
			continue
		}
		if autorouteProtected(addr) {
			continue
		}
		if net.ParseIP(addr) != nil || strings.Contains(addr, "/") {
			runCmd("ipset", "add", autorouteIPSet, addr, "-exist")
			continue
		}
		for _, ip := range resolveV4(addr) {
			if autorouteProtected(ip) {
				continue
			}
			runCmd("ipset", "add", autorouteIPSet, ip, "-exist")
		}
	}
}

func resolveV4(host string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	addrs, _ := net.DefaultResolver.LookupHost(ctx, host)
	var v4 []string
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			v4 = append(v4, a)
		}
	}
	return v4
}

// Авто-обход (T41): отдельный список адресов через VPS + тумблер.
// Пока только визуал/хранилище — детекция недоступности и применение к
// роутингу придут отдельным этапом. Хранилище — /etc/gateway/autoroute.json.

type autoRoute struct {
	Enabled bool    `json:"enabled"`          // legacy: означает «оба» (для старых читателей)
	Detect  *bool   `json:"detect,omitempty"` // пополнять список (детектор)
	Route   *bool   `json:"route,omitempty"`  // применять список к маршруту (ipset)
	Entries []entry `json:"entries"`
}

// detect/route — с миграцией: если новые поля не заданы, берём из legacy Enabled.
func (a autoRoute) detect() bool {
	if a.Detect != nil {
		return *a.Detect
	}
	return a.Enabled
}
func (a autoRoute) route() bool {
	if a.Route != nil {
		return *a.Route
	}
	return a.Enabled
}

// entry — адрес в авто-обходе + метаданные (когда и чем добавлен).
//
// T-route-manager-phase2d (2026-08-18, живая находка код-ревью): у
// gateway-ui СВОЯ отдельная копия этой структуры, независимая от
// detector/applier.Entry — тот же autoroute.json, два разных Go-типа читают
// и пишут его. detector/applier.Entry за этот вечер обзавёлся полями
// Type/State/SuccessCount/FailureCount/ConsecutiveFailures/LastSuccess/
// LastFailure/LastSeen/Proto (T-ip-engine-phase1*) — если бы эта структура
// их не знала, ЛЮБОЕ действие в UI (add/remove/toggle — все идут через
// readAutoRoute -> писать -> writeAutoRoute, весь список целиком) молча
// стёрло бы эти поля у ВСЕХ 1700+ записей при следующей записи (Go
// json.Unmarshal в структуру без поля — тихо теряет его, Marshal обратно
// это поле уже не пишет). Реального стирания не произошло (до первого
// UI-действия после сегодняшних изменений) — поймано и починено раньше.
// Поля здесь НЕ используются активно gateway-ui (он их не отображает и не
// меняет) — только round-trip, чтобы не терять то, что пишет detector.
type entry struct {
	Addr      string `json:"addr"`
	Added     string `json:"added,omitempty"`      // RFC3339
	Source    string `json:"source,omitempty"`     // manual | rst-after-clienthello | syn-timeout | quic-no-response | legacy
	Port      int    `json:"port,omitempty"`       // порт блокировки (для ночной перепроверки)
	Clean     int    `json:"clean,omitempty"`      // подряд чистых перепроверок (round-trip, пишет детектор)
	Status    string `json:"status,omitempty"`     // "no_bypass" — ни напрямую, ни через VPS (T55, пишет brain-nightly.sh)
	CheckedAt string `json:"checked_at,omitempty"` // RFC3339, последняя проверка no_bypass (T55)

	// Round-trip-only (T-ip-engine-phase1*, см. detector/applier/applier.go —
	// единственный источник правды для семантики этих полей).
	Type                string `json:"type,omitempty"`
	State               string `json:"state,omitempty"`
	SuccessCount        int    `json:"success_count,omitempty"`
	FailureCount        int    `json:"failure_count,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
	LastSuccess         string `json:"last_success,omitempty"`
	LastFailure         string `json:"last_failure,omitempty"`
	LastSeen            string `json:"last_seen,omitempty"`
	Proto               string `json:"proto,omitempty"`
}

// UnmarshalJSON — принимает и новый объект, и старую строку (миграция формата).
func (e *entry) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		e.Addr, e.Source = s, "legacy"
		return nil
	}
	type raw entry
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	*e = entry(r)
	return nil
}

var domainRe = regexp.MustCompile(`^(geosite:[a-z0-9_-]+|([a-z0-9_-]+\.)+[a-z]{2,})$`)

func (s *server) readAutoRoute() autoRoute {
	var a autoRoute
	if data, err := os.ReadFile(s.autorouteFile); err == nil {
		json.Unmarshal(data, &a)
	}
	if a.Entries == nil {
		a.Entries = []entry{}
	}
	return a
}

func (s *server) writeAutoRoute(a autoRoute) error {
	// нормализуем legacy Enabled = detect||route (для applier детектора и совместимости)
	d, r := a.detect(), a.route()
	a.Detect, a.Route, a.Enabled = &d, &r, d || r
	if err := os.MkdirAll(filepath.Dir(s.autorouteFile), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(a, "", "  ")
	tmp := s.autorouteFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.autorouteFile)
}

// validAddr: домен/geosite или IP/CIDR.
func validAddr(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return false
	}
	if autorouteProtected(v) {
		return false
	}
	if domainRe.MatchString(v) {
		return true
	}
	if net.ParseIP(v) != nil {
		return true
	}
	if _, _, err := net.ParseCIDR(v); err == nil {
		return true
	}
	return false
}

// handleAutoRouteStats (T-route-manager-phase2d) — сводка по IP-движку
// (T-ip-engine-phase1*) для панели в UI: сколько записей, какого типа
// (STATIC/LEARNED), сколько port-scoped (игровые UDP), разбивка по
// health-state. Read-only, без побочных эффектов — просто агрегация уже
// прочитанного autoroute.json.
func (s *server) handleAutoRouteStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	a := s.readAutoRoute()
	var static, learned, portScoped int
	states := map[string]int{"HEALTHY": 0, "DEGRADED": 0, "FAILED": 0, "UNKNOWN": 0}
	for _, e := range a.Entries {
		if e.Type == "STATIC" {
			static++
		} else {
			learned++
		}
		if e.Proto == "udp" && e.Port > 0 {
			portScoped++
		}
		st := e.State
		if st == "" {
			st = "UNKNOWN"
		}
		states[st]++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":       len(a.Entries),
		"static":      static,
		"learned":     learned,
		"port_scoped": portScoped,
		"states":      states,
	})
}

func (s *server) handleAutoRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a := s.readAutoRoute()
		writeJSON(w, http.StatusOK, map[string]any{"enabled": a.Enabled, "detect": a.detect(), "route": a.route(), "entries": a.Entries})

	case http.MethodPost:
		a := s.readAutoRoute()
		switch r.FormValue("action") {
		case "enable": // legacy: единый тумблер = оба
			on := r.FormValue("on") == "true"
			a.Detect, a.Route = &on, &on
		case "mode": // независимые тумблеры detect/route
			d, rt := r.FormValue("detect") == "true", r.FormValue("route") == "true"
			a.Detect, a.Route = &d, &rt
		case "add":
			seen := map[string]bool{}
			for _, e := range a.Entries {
				seen[e.Addr] = true
			}
			now := time.Now().UTC().Format(time.RFC3339)
			for _, raw := range strings.FieldsFunc(r.FormValue("value"), func(c rune) bool { return c == ',' || c == ' ' || c == '\n' || c == '\r' || c == '\t' }) {
				v := strings.ToLower(strings.TrimSpace(raw))
				if !validAddr(v) || seen[v] {
					continue
				}
				seen[v] = true
				a.Entries = append(a.Entries, entry{Addr: v, Added: now, Source: "manual"})
			}
		case "remove":
			v := strings.ToLower(strings.TrimSpace(r.FormValue("value")))
			kept := a.Entries[:0:0]
			for _, e := range a.Entries {
				if e.Addr != v {
					kept = append(kept, e)
				}
			}
			a.Entries = kept
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action: enable|mode|add|remove"})
			return
		}
		if err := s.writeAutoRoute(a); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		a = s.readAutoRoute()
		s.syncAutoroute(a) // применить на лету (ipset), без рестарта xray
		writeJSON(w, http.StatusOK, map[string]any{"enabled": a.Enabled, "detect": a.detect(), "route": a.route(), "entries": a.Entries})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
