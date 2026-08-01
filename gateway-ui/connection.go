package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Подключение к VPS (T19). Текущее показываем только для чтения (секреты
// замаскированы) — изменить можно лишь вставкой новой vless:// ссылки, чтобы
// пользователь случайно ничего не стёр. Применение — через render-config + restart.

func mask(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= 6 {
		return "••••"
	}
	return s[:4] + "…" + s[len(s)-2:]
}

// handleConnection — GET текущее подключение (read-only, маскировано),
// POST link=vless://… — распарсить и применить.
func (s *server) handleConnection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		get := func(k string) string { v, _ := readConfigVar(s.configEnv, k); return v }
		addr := get("VPS_ADDR")
		writeJSON(w, http.StatusOK, map[string]any{
			"configured":  addr != "",
			"addr":        addr,
			"port_grpc":   get("VPS_PORT_GRPC"),
			"port_vision": get("VPS_PORT_VISION"),
			"sni":         get("VPS_SERVER_NAME"),
			"fingerprint": get("VPS_FINGERPRINT"),
			"uuid_grpc":   mask(get("VPS_UUID_GRPC")),
			"pubkey":      mask(get("VPS_PUBKEY")),
			"short_id":    mask(get("VPS_SHORT_ID")),
		})

	case http.MethodPost:
		fields, err := parseVless(r.FormValue("link"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		// Vision-канал в роутинге не используется, но шаблон конфига требует
		// валидные UUID И порт для него — если пусты, берём из gRPC. Раньше
		// фоллбэк был только для UUID: ссылка с type=grpc на чистом шлюзе (без
		// VPS_PORT_VISION в config.env вообще) рендерила шаблон с пустым
		// значением порта — envsubst подставлял "" на месте числа, xray -test
		// падал с "invalid character ',' looking for beginning of value"
		// (живой инцидент 2026-08-01, воспроизведён на тестовой VM).
		if cur, _ := readConfigVar(s.configEnv, "VPS_UUID_VISION"); cur == "" {
			if u, ok := fields["VPS_UUID_GRPC"]; ok {
				fields["VPS_UUID_VISION"] = u
			}
		}
		if cur, _ := readConfigVar(s.configEnv, "VPS_PORT_VISION"); cur == "" {
			if _, alreadySet := fields["VPS_PORT_VISION"]; !alreadySet {
				if p, ok := fields["VPS_PORT_GRPC"]; ok {
					fields["VPS_PORT_VISION"] = p
				}
			}
		}
		for k, v := range fields {
			if err := writeConfigVar(s.configEnv, k, v); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "запись config.env: " + err.Error()})
				return
			}
		}
		if out, err := s.applyXray(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "применение не удалось: " + err.Error(), "output": out})
			return
		}
		// Первое подключение VPS на шлюзе, который ставился без VPS (install.sh
		// всегда кладёт бинарник/юнит про запас, см. install.sh) — xray.service
		// мог быть не enable'нут, а редирект LAN->xray ещё не стоит вообще
		// (vps-mode.conf=off с самой установки). vps-mode.sh сам идемпотентен
		// (проверяет -C перед -I), безопасно звать и когда уже "on".
		runCmd("systemctl", "enable", "xray.service")
		if readVPSMode() != "on" {
			if out, err := runCmd("/opt/gateway/vps-mode.sh", "on"); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": "xray настроен, но не удалось включить редирект трафика: " + err.Error(), "output": out,
				})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "addr": fields["VPS_ADDR"]})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// parseVless разбирает vless://UUID@host:port?params#name в ключи config.env.
// Поддерживает Reality. transport grpc (основной) и tcp/vision.
func parseVless(link string) (map[string]string, error) {
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(link, "vless://") {
		return nil, fmt.Errorf("ожидается ссылка vless://")
	}
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("не разобрать ссылку: %v", err)
	}
	uuid := u.User.Username()
	host := u.Hostname()
	port := u.Port()
	q := u.Query()
	if uuid == "" || host == "" || port == "" {
		return nil, fmt.Errorf("в ссылке нет UUID / адреса / порта")
	}
	if strings.ToLower(q.Get("security")) != "reality" {
		return nil, fmt.Errorf("поддерживается только security=reality")
	}
	pbk, sid := q.Get("pbk"), q.Get("sid")
	if pbk == "" || sid == "" {
		return nil, fmt.Errorf("в ссылке нет Reality-ключей (pbk/sid)")
	}

	f := map[string]string{
		"VPS_ADDR":     host,
		"VPS_PUBKEY":   pbk,
		"VPS_SHORT_ID": sid,
	}
	if v := q.Get("sni"); v != "" {
		f["VPS_SERVER_NAME"] = v
	}
	if v := q.Get("fp"); v != "" {
		f["VPS_FINGERPRINT"] = v
	}
	switch strings.ToLower(q.Get("type")) {
	case "grpc":
		f["VPS_PORT_GRPC"] = port
		f["VPS_UUID_GRPC"] = uuid
	case "tcp", "":
		// vision / xtls
		f["VPS_PORT_VISION"] = port
		f["VPS_UUID_VISION"] = uuid
		// если gRPC ещё не настроен — на нём же
		f["VPS_PORT_GRPC"] = port
		f["VPS_UUID_GRPC"] = uuid
	default:
		return nil, fmt.Errorf("транспорт %q не поддержан (нужен grpc или tcp)", q.Get("type"))
	}
	return f, nil
}
