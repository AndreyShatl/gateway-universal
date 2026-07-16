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
	autoroutePort    = "12347"          // dokodemo autoroute-in (TCP REDIRECT)
	autorouteUDPPort = "12346"          // существующий tproxy-udp inbound -> proxy-mux
	autorouteMark    = "0x1/0xffffffff"
)

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
	if !a.Enabled {
		runCmd("systemctl", "stop", "gateway-detector.service")
		runCmd("ipset", "flush", autorouteIPSet)
		return
	}
	s.ensureAutorouteInfra()
	runCmd("systemctl", "start", "gateway-detector.service")
	runCmd("ipset", "flush", autorouteIPSet)
	for _, e := range a.Entries {
		addr := e.Addr
		if strings.HasPrefix(addr, "geosite:") {
			continue // geosite не резолвится в IP — только через xray-роутинг (не наш путь)
		}
		if net.ParseIP(addr) != nil || strings.Contains(addr, "/") {
			runCmd("ipset", "add", autorouteIPSet, addr, "-exist")
			continue
		}
		for _, ip := range resolveV4(addr) {
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
	Enabled bool    `json:"enabled"`
	Entries []entry `json:"entries"`
}

// entry — адрес в авто-обходе + метаданные (когда и чем добавлен).
type entry struct {
	Addr   string `json:"addr"`
	Added  string `json:"added,omitempty"`  // RFC3339
	Source string `json:"source,omitempty"` // manual | rst-after-clienthello | syn-timeout | quic-no-response | legacy
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

func (s *server) handleAutoRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a := s.readAutoRoute()
		writeJSON(w, http.StatusOK, map[string]any{"enabled": a.Enabled, "entries": a.Entries})

	case http.MethodPost:
		a := s.readAutoRoute()
		switch r.FormValue("action") {
		case "enable":
			a.Enabled = r.FormValue("on") == "true"
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
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action: enable|add|remove"})
			return
		}
		if err := s.writeAutoRoute(a); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		s.syncAutoroute(a) // применить на лету (ipset), без рестарта xray
		writeJSON(w, http.StatusOK, map[string]any{"enabled": a.Enabled, "entries": a.Entries})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
