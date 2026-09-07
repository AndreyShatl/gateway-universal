package main

// live_retrigger.go (T-live-retrigger, 2026-08-16) — переоценка домена,
// у которого УЖЕ есть назначенная DPI-стратегия, но реальный клиентский
// трафик прямо сейчас показывает признак блокировки (rst-after-clienthello/
// syn-timeout/no-response-after-clienthello/quic-no-response — все сигналы
// watcher'а по конструкции уже означают подозрение на блокировку, не любой
// трафик). Раньше это тихо игнорировалось: см. комментарий в main.go у
// isBrainEntity — единственная цель была не зациклиться на собственном
// тестовом трафике solve.sh, но заодно похоронила и реальные живые сигналы
// провала для уже "решённых" доменов.
//
// Cooldown (не на каждый обрыв подряд, живой поток запросов может слать
// сигнал многократно за секунды) + собственный, отдельный от enqueueBrain
// путь постановки в очередь (той функции НУЖНА защита isBrainEntity для
// обычного пути — новых доменов; здесь домен УЖЕ сущность, это и есть смысл
// вызова).

import (
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const liveRetriggerCooldown = 15 * time.Minute

// T-circuit-breaker (2026-08-16) — живой кейс: updates.discord.com имел
// ciadpi-стратегию, которую НАШ curl-тест считал рабочей, а реальный .NET-
// клиент апдейтера — нет (другой TLS-отпечаток). T-live-retrigger сам по
// себе тут бессилен зациклится: переоценка тем же curl-тестом переназначит
// ТУ ЖЕ "рабочую по нашим меркам" стратегию, реальный клиент снова
// сломается, живой сигнал провала придёт снова через cooldown — бесконечный
// цикл без реального решения. Предохранитель: если домен получил
// liveRetriggerBreakerThreshold живых сигналов провала БЕЗ достаточного
// периода тишины между ними (liveRetriggerBreakerWindow) — значит наш тест
// структурно не видит проблему для этого домена, и дальнейшие попытки
// переназначить стратегию бессмысленны. Принудительный VPS вместо ещё
// одной попытки — VPS не зависит от TLS-отпечатка клиента вообще.
const liveRetriggerBreakerThreshold = 3
const liveRetriggerBreakerWindow = time.Hour

type liveRetriggerState struct {
	lastSeen time.Time
	count    int
}

var liveRetriggerLast = struct {
	sync.Mutex
	m map[string]*liveRetriggerState
}{m: map[string]*liveRetriggerState{}}

func maybeRetriggerBrainEntity(domain, source string) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return
	}

	liveRetriggerLast.Lock()
	st, ok := liveRetriggerLast.m[domain]
	if ok && time.Since(st.lastSeen) < liveRetriggerCooldown {
		liveRetriggerLast.Unlock()
		return // тот же обрыв уже отреагирован недавно, не дребезжим
	}
	if !ok {
		st = &liveRetriggerState{}
		liveRetriggerLast.m[domain] = st
	}
	if time.Since(st.lastSeen) > liveRetriggerBreakerWindow {
		st.count = 0 // было достаточно тихо — считаем прошлые провалы устаревшими
	}
	st.count++
	st.lastSeen = time.Now()
	tripped := st.count >= liveRetriggerBreakerThreshold
	if tripped {
		st.count = 0 // сбрасываем после срабатывания предохранителя
	}
	liveRetriggerLast.Unlock()

	if tripped {
		forceVPSCircuitBreaker(domain, source)
		return
	}

	if enqueueBrainForRecheck(domain, source) {
		log.Printf("⚠ живой сигнал провала (%s) для уже назначенного домена %s — на переоценку", source, domain)
	}
}

// forceVPSCircuitBreaker — та же команда, что кнопка "закрепить на VPS" в UI
// (T-vps-pin) применяет к одному домену, но БЕЗ закрепления сервиса целиком
// (mode сервиса не трогаем — это именно предохранитель для одного проблемного
// домена, не решение "весь Discord теперь навсегда на VPS"). Следующая
// ночная/живая переоценка сможет снова попробовать DPI для ЭТОГО домена
// (счётчик сброшен) — предохранитель не постоянный запрет, а пауза от
// зацикливания прямо сейчас.
func forceVPSCircuitBreaker(domain, source string) {
	cmd := exec.Command("bash", "/opt/gateway-brain/brain-apply.sh", "vps", domain)
	if err := cmd.Run(); err != nil {
		log.Printf("⚠ предохранитель: не удалось перевести %s на VPS: %v", domain, err)
		return
	}
	log.Printf("🔴 предохранитель: %s получил %d живых провалов подряд (%s) — тест не видит проблему, принудительно на VPS", domain, liveRetriggerBreakerThreshold, source)
}

// enqueueBrainForRecheck — та же механика, что enqueueBrain (дедуп через
// inBrainQueue + flock), но НАМЕРЕННО без isBrainEntity-проверки: смысл
// вызова именно в том, что домен УЖЕ сущность, а мы всё равно хотим его
// перепроверить из-за живого сигнала провала.
func enqueueBrainForRecheck(domain, source string) bool {
	if domain == "" || strings.HasPrefix(domain, "geosite:") || inBrainQueue(domain) {
		return false
	}
	lf, err := os.OpenFile(brainQueueLock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer lf.Close()
	syscall.Flock(int(lf.Fd()), syscall.LOCK_EX)
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	if inBrainQueue(domain) { // повторная проверка под локом
		return false
	}
	f, err := os.OpenFile(brainQueueFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.WriteString(domain + "\t" + source + "\n")
	return err == nil
}
