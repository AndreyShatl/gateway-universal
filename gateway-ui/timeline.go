package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
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
//
// requireConsecutive=2 (п.4 ТЗ, 2026-08-12): без дебаунса одна пропущенная
// проверка резолвера (единичный UDP-таймаут, не реальный простой DNS) уже
// писала пару dns.down/dns.up в ленту — на живых шлюзах это давало десятки
// записей "down for 5s" в час и забивало Mission Timeline шумом. Реальная
// авария (упавший линк, потерянный upstream) держится дольше одного тика —
// двух проверок подряд достаточно, чтобы отсечь одиночные блипы и не
// потерять настоящие простои.
func (s *server) dnsWatchLoop() {
	const interval = 5 * time.Second
	const timeout = 3 * time.Second
	const requireConsecutive = 2
	prevOK := true
	failStreak := 0
	var downSince time.Time
	for {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		_, err := net.DefaultResolver.LookupHost(ctx, "cloudflare.com")
		cancel()
		ok := err == nil

		if ok {
			failStreak = 0
			if !prevOK {
				dur := time.Since(downSince).Round(time.Second)
				s.timeline.Record("dns.up", "DNS resolution restored (down for "+dur.String()+")")
				prevOK = true
			}
		} else {
			failStreak++
			if prevOK && failStreak >= requireConsecutive {
				downSince = time.Now().Add(-time.Duration(requireConsecutive-1) * interval)
				s.timeline.Record("dns.down", "DNS resolution failed")
				prevOK = false
			}
		}
		time.Sleep(interval)
	}
}

// cpuDiscordWatchLoop (2026-08-14) — пользователь заметил живую корреляцию:
// на слабом 2-ядерном CPU (132) во время просадок нагрузки под ~95%
// зелёный пиндикатор в Discord-звонке становился красным. Причина уже
// найдена и исправлена (SSH-мультиплексирование убрало лишний спавн
// systemd --user на каждую команду), но пользователь не сидит в мониторинге
// постоянно — нужен ПОСТФАКТУМ-лог, не живой дашборд. Пишем в Mission
// Timeline: (1) сами по себе всплески нагрузки (load1 относительно числа
// ядер), и (2) отдельно помечаем, если всплеск пришёлся на момент активного
// голосового Discord-соединения (conntrack на UDP 50000-65535 — тот же
// диапазон, что и discord-tproxy.sh) — прямая улика для разбора постфактум,
// не просто "нагрузка скакнула где-то там".
func (s *server) cpuDiscordWatchLoop() {
	const interval = 5 * time.Second
	const highRatio = 0.85 // доля от numCPU, после которой считаем это всплеском
	numCPU := float64(runtime.NumCPU())
	if numCPU < 1 {
		numCPU = 1
	}
	threshold := numCPU * highRatio
	spiking := false
	var spikeStart time.Time
	for {
		load1, ok := readLoadAvg1()
		if ok {
			discordActive := hasDiscordVoiceConntrack()
			if !spiking && load1 >= threshold {
				spiking = true
				spikeStart = time.Now()
				msg := fmt.Sprintf("нагрузка CPU %.2f/%.0f ядер", load1, numCPU)
				if discordActive {
					msg += " — идёт активный голосовой Discord-коннект (вероятная просадка пинга)"
					s.timeline.Record("cpu.spike.discord", msg)
				} else {
					s.timeline.Record("cpu.spike", msg)
				}
			} else if spiking && load1 < threshold {
				spiking = false
				dur := time.Since(spikeStart).Round(time.Second)
				s.timeline.Record("cpu.normal", "нагрузка CPU вернулась в норму (пик длился "+dur.String()+")")
			}
		}
		time.Sleep(interval)
	}
}

func readLoadAvg1() (float64, bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	return v, err == nil
}

// hasDiscordVoiceConntrack — есть ли прямо сейчас установленное UDP-соединение
// в диапазоне 50000-65535 (тот же, что перехватывает discord-tproxy.sh) —
// грубый, но дешёвый сигнал "человек сейчас в голосовом звонке", без парсинга
// самого голосового трафика.
func hasDiscordVoiceConntrack() bool {
	out, err := runCmd("bash", "-c", `conntrack -L -p udp 2>/dev/null | grep -oE 'dport=[0-9]+' | cut -d= -f2 | awk '$1>=50000 && $1<=65535 {c++} END{print c+0}'`)
	if err != nil {
		return false
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n > 0
}
