// Package applier — применяет подтверждённый блок в авто-обход БЕЗ рестарта xray:
// добавляет цель в общий autoroute.json (единый источник, что и у gateway-ui) под
// файловой блокировкой, затем ipset add (мгновенный эффект через выделенный
// inbound :12347 → proxy-mux). Домены резолвятся в IP (через локальный dnscrypt).
package applier

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	IPSet   = "gw_autoroute"
	LAN     = "192.168.0.0/16"
	Port    = "12347" // dokodemo autoroute-in (TCP REDIRECT)
	UDPPort = "12346" // существующий tproxy-udp inbound -> proxy-mux
	Mark    = "0x1/0xffffffff"
	File    = "/etc/gateway/autoroute.json"
)

// Store — содержимое autoroute.json.
type Store struct {
	Enabled bool    `json:"enabled"`
	Detect  *bool   `json:"detect,omitempty"`
	Route   *bool   `json:"route,omitempty"`
	Entries []Entry `json:"entries"`
}

func (s Store) DetectOn() bool {
	if s.Detect != nil {
		return *s.Detect
	}
	return s.Enabled
}
func (s Store) RouteOn() bool {
	if s.Route != nil {
		return *s.Route
	}
	return s.Enabled
}

// Entry — адрес + метаданные. Совместимо со схемой gateway-ui (поля round-trip).
type Entry struct {
	Addr            string `json:"addr"`
	Added           string `json:"added,omitempty"`
	Source          string `json:"source,omitempty"`
	DPort           int    `json:"port,omitempty"`            // порт блокировки (для точной перепроверки)
	Clean           int    `json:"clean,omitempty"`            // подряд чистых ночных проверок (2 -> удаление)
	CollapsedSuffix string `json:"collapsed_suffix,omitempty"` // T-collapse-uuid: если есть — эта запись
	// представитель ЦЕЛОЙ пачки случайных UUID-поддоменов с общим хвостом
	// (см. collapseSuffix), новые "братья" не создают отдельных записей.

	// Type (T-ip-engine-phase1, 2026-08-17) — STATIC | LEARNED (пусто = LEARNED,
	// обратная совместимость со старыми записями). STATIC — добавлена вручную
	// оператором/конфигурацией (напр. известный игровой сервер), НИКОГДА не
	// удаляется ночной перепроверкой (runRecheck пропускает такие записи
	// независимо от результата пробы) и не участвует в TTL-старении. LEARNED —
	// обнаружена системой автоматически, обычный жизненный цикл (recheck может
	// снять после 2 чистых прогонов подряд).
	Type string `json:"type,omitempty"`

	// Health state (T-ip-engine-phase1b, 2026-08-17) — из ТЗ п.9: "результат
	// probe должен быть отделён от изменения маршрута", здесь — минимальная
	// часть этого: копим историю без немедленной реакции на неё (recheck и
	// live-путь пока принимают решения как раньше, эти поля пока НАБЛЮДЕНИЕ,
	// не управление — заготовка под настоящий hysteresis/cooldown следующим
	// шагом, чтобы не смешивать в одном коммите схему и поведение).
	State               string `json:"state,omitempty"`                // UNKNOWN|HEALTHY|DEGRADED|FAILED (пусто = UNKNOWN)
	SuccessCount        int    `json:"success_count,omitempty"`        // всего успешных проверок за всё время
	FailureCount        int    `json:"failure_count,omitempty"`        // всего проваленных проверок за всё время
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"` // подряд с последнего успеха, сбрасывается им
	LastSuccess         string `json:"last_success,omitempty"`         // RFC3339, последняя успешная проверка
	LastFailure         string `json:"last_failure,omitempty"`         // RFC3339, последняя проваленная проверка

	// LastSeen (T-ip-engine-phase1d, 2026-08-17) — из ТЗ п.17 (aging для
	// LEARNED IP): когда живой трафик к этому target последний раз реально
	// наблюдался (обновляется в Apply() при каждом повторном "уже в списке"
	// совпадении — то есть при каждом новом кандидате детектора на тот же
	// addr, не при каждом пакете). Пусто у записей, добавленных ДО этого
	// поля, и у geosite:/CIDR-записей (Apply матчит их только по точному
	// совпадению строки target — CIDR/geosite target детектор никогда не
	// формирует буквально, трогать их старение здесь сознательно не берёмся,
	// см. PruneStale).
	LastSeen string `json:"last_seen,omitempty"`
}

// removeCooldownFile — отдельный маленький файл (не смешан с основной схемой
// Entry — снятая запись из autoroute.json удаляется целиком, держать
// tombstone-запись внутри самого списка означало бы учить каждого
// потребителя Entries (ipset-синк, UI, recheck) отличать "реальный маршрут"
// от "просто память о недавнем снятии", это лишняя связанность ради узкой
// задачи). addr -> RFC3339 "cooldown_until".
const removeCooldownFile = "/etc/gateway/autoroute-removed.json"
const removeCooldown = 30 * time.Minute

// RegisterRemoval — отметить addr как недавно снятый (T-ip-engine-phase1c).
// Вызывается из UpdateClean для каждого реально удалённого addr.
func RegisterRemoval(addr string) {
	m := readRemovalCooldowns()
	m[addr] = time.Now().UTC().Add(removeCooldown).Format(time.RFC3339)
	saveRemovalCooldowns(m)
}

// IsInRemovalCooldown — true, если addr был снят настолько недавно, что
// повторное добавление сейчас нужно придержать (защита от дребезга —
// ТЗ п.15: "VPS -> DIRECT не должно моментально превратиться в DIRECT ->
// VPS из-за одного неудачного probe"). Попутно чистит истёкшие записи —
// файл маленький, отдельного GC-прохода не заводим.
func IsInRemovalCooldown(addr string) bool {
	m := readRemovalCooldowns()
	until, ok := m[addr]
	if !ok {
		return false
	}
	t, err := time.Parse(time.RFC3339, until)
	if err != nil || !time.Now().UTC().Before(t) {
		delete(m, addr)
		saveRemovalCooldowns(m)
		return false
	}
	return true
}

func readRemovalCooldowns() map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(removeCooldownFile)
	if err != nil {
		return m
	}
	json.Unmarshal(data, &m)
	return m
}

func saveRemovalCooldowns(m map[string]string) {
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := removeCooldownFile + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, removeCooldownFile)
	}
}

func (e Entry) IsStatic() bool { return e.Type == "STATIC" }

// RecordCheck — обновить health-историю записи результатом одной проверки
// (T-ip-engine-phase1b). Правило state (без гистерезиса — это наблюдение,
// не решение о маршруте, см. комментарий у полей выше):
//
//	успех              -> HEALTHY, сброс ConsecutiveFailures
//	1-й провал подряд  -> DEGRADED
//	2+ провалов подряд -> FAILED
func (e *Entry) RecordCheck(ok bool) {
	now := time.Now().UTC().Format(time.RFC3339)
	if ok {
		e.SuccessCount++
		e.LastSuccess = now
		e.ConsecutiveFailures = 0
		e.State = "HEALTHY"
		return
	}
	e.FailureCount++
	e.LastFailure = now
	e.ConsecutiveFailures++
	if e.ConsecutiveFailures >= 2 {
		e.State = "FAILED"
	} else {
		e.State = "DEGRADED"
	}
}

// protectedIPs — адреса, которые НИКОГДА нельзя заворачивать в авто-обход
// (VPS-туннель), даже если проба ошибочно посчитала их "заблокированными".
// Обнаружено на практике (2026-08-09): 8.8.8.8 (DNS-резолвер клиента) и
// 127.0.0.1 попали в gw_autoroute (ложный TCP/UDP syn-timeout к :53 под
// нагрузкой), после чего весь DNS-трафик клиентов, использующих эти
// резолверы, безусловно заворачивался в proxy-mux (правило xray для
// inboundTag tproxy-udp не делает исключений) — VPS этот путь для чистого
// DNS не тянет, что уронило интернет клиенту целиком.
var protectedIPs = map[string]bool{
	"127.0.0.1": true,
	"::1":       true,
	"8.8.8.8":   true,
	"8.8.4.4":   true,
	"1.1.1.1":   true,
	"1.0.0.1":   true,
	"9.9.9.9":   true,
	"149.112.112.112": true,
	"208.67.222.222":  true,
	"208.67.220.220":  true,
}

// IsProtected — true, если addr нельзя добавлять в авто-обход: сам адрес в
// списке защищённых, либо loopback/link-local/приватный диапазон.
func IsProtected(addr string) bool {
	return isProtected(addr)
}

func isProtected(addr string) bool {
	if protectedIPs[addr] {
		return true
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return false // домен — не адрес, проверяется отдельно на этапе резолва
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

// uuidPrefixRe — CDN-провайдеры (замечено на *.fbcdn.net) иногда генерируют
// per-сессионные поддомены вида "<uuid>-netseer-ipaddr-assoc.xz.fbcdn.net" —
// кардинальность практически неограничена (новый UUID на почти каждый визит).
// Без схлопывания это отдельная запись autoroute.json НАВСЕГДА (нет прунинга
// для рабочих через VPS доменов) — список и ночная очередь растут без предела.
var uuidPrefixRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-(.+)$`)

// collapseSuffix — хвост домена после случайного UUID-префикса, или "" если
// домен под паттерн не подходит (обычные домены не трогаем).
func collapseSuffix(host string) string {
	m := uuidPrefixRe.FindStringSubmatch(strings.ToLower(host))
	if m == nil {
		return ""
	}
	return m[1]
}

// UnmarshalJSON — принимает и объект, и старую строку (миграция формата).
func (e *Entry) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		e.Addr, e.Source = s, "legacy"
		return nil
	}
	type raw Entry
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	*e = Entry(r)
	return nil
}

// withLock — выполнить fn, держа эксклюзивный flock на autoroute.json.
func withLock(fn func(*Store)) error {
	f, err := os.OpenFile(File, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	var s Store
	data, _ := os.ReadFile(File)
	json.Unmarshal(data, &s)
	fn(&s)
	return nil
}

// Load — прочитать autoroute.json (без блокировки, для перепроверки-снимка).
func Load() (Store, error) {
	var s Store
	data, err := os.ReadFile(File)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// save — атомарная запись (вызывающий держит flock).
func save(s *Store) {
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := File + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, File)
	}
}

// EnsureInfra — ipset + iptables (TCP REDIRECT + UDP TPROXY), идемпотентно.
func EnsureInfra() {
	run("ipset", "create", IPSet, "hash:net", "family", "inet", "-exist")
	ensureRule("nat", []string{"-s", LAN, "-p", "tcp",
		"-m", "set", "--match-set", IPSet, "dst", "-j", "REDIRECT", "--to-ports", Port})
	ensureRule("mangle", []string{"-s", LAN, "-p", "udp",
		"-m", "set", "--match-set", IPSet, "dst",
		"-j", "TPROXY", "--on-port", UDPPort, "--on-ip", "0.0.0.0", "--tproxy-mark", Mark})
}

func ensureRule(table string, spec []string) {
	if run("iptables", append([]string{"-t", table, "-C", "PREROUTING"}, spec...)...) != nil {
		run("iptables", append([]string{"-t", table, "-I", "PREROUTING", "1"}, spec...)...)
	}
}

// Sync — привести ipset в соответствие со списком (flush + перезаполнение).
func Sync(entries []Entry) {
	EnsureInfra()
	run("ipset", "flush", IPSet)
	for _, e := range entries {
		addr := e.Addr
		if strings.HasPrefix(addr, "geosite:") || isProtected(addr) {
			continue
		}
		if net.ParseIP(addr) != nil || strings.Contains(addr, "/") {
			run("ipset", "add", IPSet, addr, "-exist")
			continue
		}
		for _, ip := range resolveV4(addr) {
			if isProtected(ip) {
				continue
			}
			run("ipset", "add", IPSet, ip, "-exist")
		}
	}
}

// Apply — добавить цель в авто-обход. source — чем поймано, port — порт блокировки.
// Возвращает true, если реально добавлено (не было раньше и авто-обход включён).
// Для случайных UUID-поддоменов (см. collapseSuffix) новая запись в JSON не
// создаётся, если под тот же хвост уже есть представитель — но ipset ниже
// всё равно получает IP этого конкретного домена, трафик не теряется, растёт
// только список того, что нужно перепроверять по ночам, а не сама маршрутизация.
func Apply(target, source string, port int) bool {
	if isProtected(target) {
		return false
	}
	if IsInRemovalCooldown(target) { // T-ip-engine-phase1c: см. п.15 ТЗ, дребезг сразу после снятия
		return false
	}
	added := false
	route := false
	suffix := collapseSuffix(target)
	withLock(func(s *Store) {
		if !s.DetectOn() {
			return // пополнение выключено — не добавляем
		}
		route = s.RouteOn()
		collapsed := false
		now := time.Now().UTC().Format(time.RFC3339)
		for i, e := range s.Entries {
			if e.Addr == target {
				s.Entries[i].LastSeen = now // T-ip-engine-phase1d: живой трафик всё ещё идёт — не устарела
				save(s)
				return
			}
			if suffix != "" && e.CollapsedSuffix == suffix {
				collapsed = true
			}
		}
		if !collapsed {
			s.Entries = append(s.Entries, Entry{
				Addr:            target,
				Added:           now,
				LastSeen:        now,
				Source:          source,
				DPort:           port,
				CollapsedSuffix: suffix,
			})
			save(s)
		}
		added = true
	})
	if !added {
		return false
	}
	if !route {
		return true // добавили в список, но применение выключено — в ipset не пишем
	}
	EnsureInfra()
	if net.ParseIP(target) != nil || strings.Contains(target, "/") {
		run("ipset", "add", IPSet, target, "-exist")
	} else {
		for _, ip := range resolveV4(target) {
			if isProtected(ip) {
				continue
			}
			run("ipset", "add", IPSet, ip, "-exist")
		}
	}
	return true
}

// AddStatic — добавить STATIC-запись (T-ip-engine-phase1, 2026-08-17):
// оператор явно закрепляет известный target (напр. игровой сервер) — в
// отличие от Apply() (LEARNED, авто-обнаружение), эта запись никогда не
// удаляется ночной перепроверкой (см. runRecheck в main.go) независимо от
// результата пробы. Игнорирует тумблер "detect" (STATIC — это явное решение
// оператора, не результат обнаружения) но уважает "route" — как и Apply().
func AddStatic(target string, port int) bool {
	if isProtected(target) {
		return false
	}
	added := false
	route := false
	withLock(func(s *Store) {
		route = s.RouteOn()
		for i, e := range s.Entries {
			if e.Addr == target {
				if !e.IsStatic() {
					s.Entries[i].Type = "STATIC" // повысить существующую LEARNED-запись
					save(s)
				}
				added = true
				return
			}
		}
		s.Entries = append(s.Entries, Entry{
			Addr:   target,
			Added:  time.Now().UTC().Format(time.RFC3339),
			Source: "static",
			DPort:  port,
			Type:   "STATIC",
		})
		save(s)
		added = true
	})
	if !added || !route {
		return added
	}
	EnsureInfra()
	if net.ParseIP(target) != nil || strings.Contains(target, "/") {
		run("ipset", "add", IPSet, target, "-exist")
	} else {
		for _, ip := range resolveV4(target) {
			if isProtected(ip) {
				continue
			}
			run("ipset", "add", IPSet, ip, "-exist")
		}
	}
	return true
}

// UpdateClean — под блокировкой применить результаты перепроверки к текущему файлу
// (мерж по addr, чтобы не затереть параллельные добавления ночью). remove — набор
// addr на удаление; clean — новые значения счётчика для оставшихся. Возвращает
// оставшиеся записи (для ресинка ipset).
// checkResults — T-ip-engine-phase1b: результат последней ночной пробы по
// addr (true=direct работает), для RecordCheck(). Опционально — recheck
// может не передавать (nil), тогда health-история не трогается (совместимо
// со старыми вызовами).
func UpdateClean(remove map[string]bool, clean map[string]int, checkResults map[string]bool) []Entry {
	var kept []Entry
	withLock(func(s *Store) {
		out := s.Entries[:0:0]
		for _, e := range s.Entries {
			if remove[e.Addr] {
				RegisterRemoval(e.Addr)
				continue
			}
			if v, ok := clean[e.Addr]; ok {
				e.Clean = v
			}
			if checkResults != nil {
				if ok, seen := checkResults[e.Addr]; seen {
					e.RecordCheck(ok)
				}
			}
			out = append(out, e)
		}
		s.Entries = out
		save(s)
		kept = out
	})
	return kept
}

// staleAfter — LEARNED-запись с LastSeen старше этого считается EXPIRED
// (T-ip-engine-phase1d, ТЗ п.17: "ACTIVE -> STALE -> EXPIRED"). Одно
// пороговое значение вместо двух стадий — упрощение первого среза: реальное
// действие важнее промежуточной метки состояния, которая ни на что не
// влияет, пока сама не приведёт к удалению.
const staleAfter = 30 * 24 * time.Hour

// PruneStale — удалить LEARNED-записи, которые давно не видели живьём
// (LastSeen старше staleAfter). STATIC никогда не трогаем (ТЗ п.7/17: "их
// не удалять автоматически"). Записи БЕЗ LastSeen (добавлены до этого поля,
// либо CIDR/geosite — см. комментарий у Entry.LastSeen) пропускаем — нет
// данных для решения, значит не решаем. Возвращает удалённые addr (для
// синка ipset у вызывающего).
func PruneStale() []string {
	var removed []string
	withLock(func(s *Store) {
		out := s.Entries[:0:0]
		for _, e := range s.Entries {
			if e.IsStatic() || e.LastSeen == "" {
				out = append(out, e)
				continue
			}
			t, err := time.Parse(time.RFC3339, e.LastSeen)
			if err != nil || time.Since(t) < staleAfter {
				out = append(out, e)
				continue
			}
			removed = append(removed, e.Addr)
		}
		if len(removed) > 0 {
			s.Entries = out
			save(s)
		}
	})
	return removed
}

func run(name string, args ...string) error {
	return exec.Command(name, args...).Run()
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
