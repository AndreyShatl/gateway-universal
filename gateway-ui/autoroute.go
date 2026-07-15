package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Авто-обход (T41): отдельный список адресов через VPS + тумблер.
// Пока только визуал/хранилище — детекция недоступности и применение к
// роутингу придут отдельным этапом. Хранилище — /etc/gateway/autoroute.json.

type autoRoute struct {
	Enabled bool     `json:"enabled"`
	Entries []string `json:"entries"`
}

var domainRe = regexp.MustCompile(`^(geosite:[a-z0-9_-]+|([a-z0-9_-]+\.)+[a-z]{2,})$`)

func (s *server) readAutoRoute() autoRoute {
	var a autoRoute
	if data, err := os.ReadFile(s.autorouteFile); err == nil {
		json.Unmarshal(data, &a)
	}
	if a.Entries == nil {
		a.Entries = []string{}
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
				seen[e] = true
			}
			for _, raw := range strings.FieldsFunc(r.FormValue("value"), func(c rune) bool { return c == ',' || c == ' ' || c == '\n' || c == '\r' || c == '\t' }) {
				v := strings.ToLower(strings.TrimSpace(raw))
				if !validAddr(v) || seen[v] {
					continue
				}
				seen[v] = true
				a.Entries = append(a.Entries, v)
			}
		case "remove":
			v := strings.ToLower(strings.TrimSpace(r.FormValue("value")))
			kept := a.Entries[:0:0]
			for _, e := range a.Entries {
				if e != v {
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
		writeJSON(w, http.StatusOK, map[string]any{"enabled": a.Enabled, "entries": a.Entries})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
