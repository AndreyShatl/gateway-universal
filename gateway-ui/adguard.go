package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Реклама/DNS (AdGuard Home, 2026-07-26): DNS-блокировка рекламы/трекеров на самом
// шлюзе (dnscrypt-proxy сдвинут на 127.0.0.1:5353, стал upstream для AdGuard Home
// на :53 — см. DECISIONS). Полноценная своя админка (фильтры/клиенты/лог запросов)
// уже есть у AdGuard Home на :3000 (LAN-only firewall, как gateway-ui на :8088) —
// не дублируем, тут только сводка + ссылка на неё.
const adguardAddr = "http://127.0.0.1:3000"

var (
	adguardMu     sync.Mutex
	adguardCookie string
)

// adguardLogin — получить сессионную cookie AdGuard Home. Пароль читаем из
// config.env (ADGUARD_PASSWORD) — тот же, что задан при установке.
func (s *server) adguardLogin() (string, error) {
	user, _ := readConfigVar(s.configEnv, "ADGUARD_USER")
	if user == "" {
		user = "admin"
	}
	pass, _ := readConfigVar(s.configEnv, "ADGUARD_PASSWORD")
	body, _ := json.Marshal(map[string]string{"name": user, "password": pass})
	req, err := http.NewRequest(http.MethodPost, adguardAddr+"/control/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "agh_session" {
			return c.Value, nil
		}
	}
	return "", nil
}

// adguardGet — GET внутреннего API AdGuard Home с cookie-сессией (кэш + один
// повторный вход при протухшей сессии, не логинимся заново на каждый опрос —
// у AdGuard Home есть anti-bruteforce троттлинг повторных /control/login).
func (s *server) adguardGet(path string) ([]byte, error) {
	adguardMu.Lock()
	cookie := adguardCookie
	adguardMu.Unlock()

	doReq := func(cookie string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, adguardAddr+path, nil)
		if err != nil {
			return nil, err
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "agh_session", Value: cookie})
		}
		client := &http.Client{Timeout: 10 * time.Second}
		return client.Do(req)
	}

	resp, err := doReq(cookie)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		newCookie, err := s.adguardLogin()
		if err != nil {
			return nil, err
		}
		adguardMu.Lock()
		adguardCookie = newCookie
		adguardMu.Unlock()
		resp, err = doReq(newCookie)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return buf.Bytes(), nil
}

func (s *server) handleAdguard(w http.ResponseWriter, r *http.Request) {
	statsRaw, statsErr := s.adguardGet("/control/stats")
	filtRaw, filtErr := s.adguardGet("/control/filtering/status")
	if statsErr != nil && filtErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	var stats map[string]any
	var filt map[string]any
	json.Unmarshal(statsRaw, &stats)
	json.Unmarshal(filtRaw, &filt)
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"stats":     stats,
		"filtering": filt,
		"ui_url":    "http://" + r.Host[:indexOrLen(r.Host, ':')] + ":3000",
	})
}

func indexOrLen(s string, sep byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return i
		}
	}
	return len(s)
}
