package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Сохранённые подключения (T32): несколько VPS-хостов с переключением.
// Хранилище — /etc/gateway/connections.json (рут-доступ, как config.env).
// Каждая запись = распарсенные VPS_* поля; одна помечена active (применена).

type conn struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Active bool              `json:"active"`
	Fields map[string]string `json:"fields"` // VPS_ADDR, VPS_UUID_GRPC, ...
}

func (s *server) readConns() []conn {
	data, err := os.ReadFile(s.connsFile)
	if err != nil {
		return []conn{}
	}
	var c []conn
	json.Unmarshal(data, &c)
	return c
}

func (s *server) writeConns(c []conn) error {
	if err := os.MkdirAll(filepath.Dir(s.connsFile), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := s.connsFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.connsFile)
}

// handleConnections — GET список (секреты замаскированы), POST action add|activate|delete.
func (s *server) handleConnections(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.ensureCurrentConn() // текущий хост из config.env — добавить в список, если его там нет
		out := []map[string]any{}
		for _, c := range s.readConns() {
			out = append(out, map[string]any{
				"id": c.ID, "name": c.Name, "active": c.Active,
				"addr": c.Fields["VPS_ADDR"], "port_grpc": c.Fields["VPS_PORT_GRPC"],
				"uuid_grpc": mask(c.Fields["VPS_UUID_GRPC"]), "pubkey": mask(c.Fields["VPS_PUBKEY"]),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"connections": out})

	case http.MethodPost:
		switch r.FormValue("action") {
		case "add":
			s.connAdd(w, r)
		case "activate":
			s.connActivate(w, r)
		case "edit":
			s.connEdit(w, r)
		case "delete":
			s.connDelete(w, r)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action: add|activate|delete"})
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) connAdd(w http.ResponseWriter, r *http.Request) {
	fields, err := parseVless(r.FormValue("link"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if cur, _ := readConfigVar(s.configEnv, "VPS_UUID_VISION"); cur == "" {
		if u, ok := fields["VPS_UUID_GRPC"]; ok {
			fields["VPS_UUID_VISION"] = u
		}
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = fields["VPS_ADDR"]
	}
	c := s.readConns()
	c = append(c, conn{ID: "c" + strconv.FormatInt(time.Now().UnixNano(), 36), Name: name, Fields: fields})
	if err := s.writeConns(c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) connActivate(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	conns := s.readConns()
	var target *conn
	for i := range conns {
		conns[i].Active = conns[i].ID == id
		if conns[i].ID == id {
			target = &conns[i]
		}
	}
	if target == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "нет такого подключения"})
		return
	}
	// записать поля в config.env + перерендерить + рестарт xray
	for k, v := range target.Fields {
		if err := writeConfigVar(s.configEnv, k, v); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "config.env: " + err.Error()})
			return
		}
	}
	if out, err := s.applyXray(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "применение: " + err.Error(), "output": out})
		return
	}
	s.writeConns(conns)

	// discord-tproxy.sh хранит VPS_IP как захардкоженную подстановку из
	// install-time (см. install.sh: sed __VPS_ADDR__) — обнаружено вживую
	// (2026-08-11): при смене активного VPS без этого шага discord-tproxy
	// продолжал исключать из TPROXY-перехвата СТАРЫЙ VPS IP, ловил в
	// туннель и голосовые пакеты Discord к новому VPS тоже — голос ломался.
	// Трогаем только если сервис реально включён — не форсируем discord
	// тем, у кого он выключен.
	discordWarn := ""
	if serviceActive("discord-tproxy.service") {
		if err := s.regenerateDiscordTproxy(target.Fields["VPS_ADDR"]); err != nil {
			discordWarn = "VPS переключён, но discord-tproxy не удалось перенастроить: " + err.Error()
		}
	}

	resp := map[string]any{"ok": true, "addr": target.Fields["VPS_ADDR"]}
	if discordWarn != "" {
		resp["warning"] = discordWarn
	}
	writeJSON(w, http.StatusOK, resp)
}

// regenerateDiscordTproxy — перегенерирует /opt/gateway/discord-tproxy.sh с
// НОВЫМ VPS_ADDR из шаблона в репозитории (та же подстановка, что делает
// install.sh при первой установке) и перезапускает сервис — тот сам
// идемпотентно чистит и пересоздаёт iptables-цепочку (flush в начале
// скрипта), так что старые правила под старый VPS корректно уходят.
func (s *server) regenerateDiscordTproxy(vpsAddr string) error {
	tmplPath := filepath.Join(s.repoDir, "iptables/discord-tproxy.sh")
	tmpl, err := os.ReadFile(tmplPath)
	if err != nil {
		return err
	}
	rendered := strings.ReplaceAll(string(tmpl), "__VPS_ADDR__", vpsAddr)
	if err := os.WriteFile("/opt/gateway/discord-tproxy.sh", []byte(rendered), 0o755); err != nil {
		return err
	}
	_, err = runCmd("systemctl", "restart", "discord-tproxy.service")
	return err
}

// ensureCurrentConn: если в списке нет активного, заводит запись из config.env
// (текущий применённый хост), чтобы им можно было управлять из UI.
func (s *server) ensureCurrentConn() {
	addr, _ := readConfigVar(s.configEnv, "VPS_ADDR")
	if addr == "" {
		return
	}
	conns := s.readConns()
	for _, c := range conns {
		if c.Active {
			return
		}
	}
	f := map[string]string{}
	for _, k := range []string{"VPS_ADDR", "VPS_PORT_GRPC", "VPS_PORT_VISION", "VPS_UUID_GRPC", "VPS_UUID_VISION", "VPS_PUBKEY", "VPS_SHORT_ID", "VPS_SERVER_NAME", "VPS_FINGERPRINT"} {
		if v, _ := readConfigVar(s.configEnv, k); v != "" {
			f[k] = v
		}
	}
	cur := conn{ID: "c" + strconv.FormatInt(time.Now().UnixNano(), 36), Name: "Текущий хост", Active: true, Fields: f}
	s.writeConns(append([]conn{cur}, conns...))
}

// connEdit: заменить ссылку/имя существующего хоста (если активен — переприменить).
func (s *server) connEdit(w http.ResponseWriter, r *http.Request) {
	fields, err := parseVless(r.FormValue("link"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if cur, _ := readConfigVar(s.configEnv, "VPS_UUID_VISION"); cur == "" {
		if u, ok := fields["VPS_UUID_GRPC"]; ok {
			fields["VPS_UUID_VISION"] = u
		}
	}
	id := r.FormValue("id")
	conns := s.readConns()
	var t *conn
	for i := range conns {
		if conns[i].ID == id {
			t = &conns[i]
		}
	}
	if t == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "нет такого подключения"})
		return
	}
	if n := strings.TrimSpace(r.FormValue("name")); n != "" {
		t.Name = n
	}
	t.Fields = fields
	if err := s.writeConns(conns); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if t.Active { // активный — переприменить
		for k, v := range fields {
			writeConfigVar(s.configEnv, k, v)
		}
		if out, err := s.applyXray(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "применение: " + err.Error(), "output": out})
			return
		}
		if serviceActive("discord-tproxy.service") {
			if err := s.regenerateDiscordTproxy(fields["VPS_ADDR"]); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": "VPS переприменён, но discord-tproxy не удалось перенастроить: " + err.Error()})
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) connDelete(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	var kept []conn
	for _, c := range s.readConns() {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	if err := s.writeConns(kept); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
