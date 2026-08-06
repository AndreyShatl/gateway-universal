// gmp.go — статус подключения к GMP-мониторингу (2026-08-06): отдельная
// read-only панель на Settings, отличная от "Подключений к VPS" — по
// фидбеку пользователь путал enrollment-токен GMP (регистрация в чужом
// мониторинге) с UUID VPS-подключения (собственный xray-туннель), это два
// независимых механизма. Читает то же, что видит сам gmp-agent —
// config.json (куда он подключён) + state.json (зарегистрирован ли,
// gateway_id) — без сетевых запросов к GMP, только локальные файлы.
package main

import (
	"encoding/json"
	"net/http"
	"os"
)

const (
	gmpAgentConfigPath = "/etc/gmp-agent/config.json"
	gmpAgentStatePath  = "/etc/gmp-agent/state.json"
)

type gmpAgentConfig struct {
	ServerURL string `json:"server_url"`
}

type gmpAgentState struct {
	GatewayID string `json:"gateway_id"`
}

type gmpStatus struct {
	Installed  bool   `json:"installed"`
	Registered bool   `json:"registered"`
	ServerURL  string `json:"server_url"`
	GatewayID  string `json:"gateway_id"`
}

func (s *server) handleGMPStatus(w http.ResponseWriter, r *http.Request) {
	var out gmpStatus

	if cfgRaw, err := os.ReadFile(gmpAgentConfigPath); err == nil {
		out.Installed = true
		var cfg gmpAgentConfig
		json.Unmarshal(cfgRaw, &cfg)
		out.ServerURL = cfg.ServerURL
	}
	if stateRaw, err := os.ReadFile(gmpAgentStatePath); err == nil {
		var st gmpAgentState
		json.Unmarshal(stateRaw, &st)
		out.GatewayID = st.GatewayID
		out.Registered = st.GatewayID != ""
	}

	writeJSON(w, http.StatusOK, out)
}
