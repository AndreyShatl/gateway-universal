package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Mission Timeline — локальный журнал событий шлюза (T-shattl-gwui-spec,
// 2026-08-05): "VPN tunnel established", "Xray restarted", "Configuration
// updated" и т.п. на Overview. Работает полностью локально (append-only
// JSONL-файл на диске шлюза), без зависимости от gmp-server/VPS — та же
// идея, что и audit_log там, но независимая реализация: цель этой панели —
// показывать состояние шлюза, даже если сервер/интернет недоступны.
type timelineEvent struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // "service.restart" | "vpn.up" | "vpn.down" | "config.updated" | "system.boot"
	Message string    `json:"message"`
}

type timelineLog struct {
	mu   sync.Mutex
	path string
}

func newTimelineLog(path string) *timelineLog {
	return &timelineLog{path: path}
}

// Record — дописывает одну строку в конец файла. Ошибка записи только
// логируется, не всплывает вызывающему коду: потеря одной записи ленты не
// должна валить основное действие (рестарт сервиса, применение конфига).
func (t *timelineLog) Record(kind, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	f, err := os.OpenFile(t.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	line, err := json.Marshal(timelineEvent{At: time.Now(), Kind: kind, Message: message})
	if err != nil {
		return
	}
	f.Write(line)
	f.Write([]byte("\n"))
}

// Recent — последние n событий, новые первыми. Читает файл целиком — журнал
// на домашнем шлюзе растёт медленно (единицы записей в день), файл на
// мегабайты не разрастётся между чистками, отдельный индекс избыточен.
func (t *timelineLog) Recent(n int) []timelineEvent {
	t.mu.Lock()
	defer t.mu.Unlock()

	f, err := os.Open(t.path)
	if err != nil {
		return []timelineEvent{}
	}
	defer f.Close()

	var all []timelineEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var ev timelineEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			all = append(all, ev)
		}
	}

	out := make([]timelineEvent, 0, n)
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, all[i])
	}
	return out
}

func (s *server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.timeline.Recent(50))
}

// vpnWatchLoop — единственный способ узнать про реконнект VPN-туннеля (xray)
// без правки самого xray/systemd-юнитов: опрашиваем `systemctl is-active` на
// тикере и логируем только ПЕРЕХОДЫ состояния, не каждый тик — иначе лента
// заполнится копиями одного и того же статуса.
func (s *server) vpnWatchLoop() {
	const interval = 10 * time.Second
	prev := ""
	for {
		out, _ := runCmd("systemctl", "is-active", "xray.service")
		state := strings.TrimSpace(out)
		if prev != "" && state != prev {
			switch state {
			case "active":
				s.timeline.Record("vpn.up", "VPN tunnel established")
			default:
				s.timeline.Record("vpn.down", "VPN tunnel lost")
			}
		}
		prev = state
		time.Sleep(interval)
	}
}

// dnsWatchLoop — то же самое для DNS (T-shattl-dns-flap, 2026-08-05): линк
// сетевой карты уже видели физически падавшим (dmesg: enp2s0 Link is Down на
// 44с), но не каждое падение DNS сопровождается таким событием — короткие
// сбои резолвера проходят мимо dmesg. Этот watcher ловит именно момент и
// длительность падения, чтобы не приходилось ловить его вживую по жалобе
// пользователя постфактум.
func (s *server) dnsWatchLoop() {
	const interval = 5 * time.Second
	const timeout = 3 * time.Second
	prevOK := true
	var downSince time.Time
	for {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		_, err := net.DefaultResolver.LookupHost(ctx, "cloudflare.com")
		cancel()
		ok := err == nil

		if ok && !prevOK {
			dur := time.Since(downSince).Round(time.Second)
			s.timeline.Record("dns.up", "DNS resolution restored (down for "+dur.String()+")")
		} else if !ok && prevOK {
			downSince = time.Now()
			s.timeline.Record("dns.down", "DNS resolution failed")
		}
		prevOK = ok
		time.Sleep(interval)
	}
}
