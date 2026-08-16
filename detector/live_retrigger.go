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
	"strings"
	"sync"
	"syscall"
	"time"
)

const liveRetriggerCooldown = 15 * time.Minute

var liveRetriggerLast = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func maybeRetriggerBrainEntity(domain, source string) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return
	}

	liveRetriggerLast.Lock()
	if last, ok := liveRetriggerLast.m[domain]; ok && time.Since(last) < liveRetriggerCooldown {
		liveRetriggerLast.Unlock()
		return
	}
	liveRetriggerLast.m[domain] = time.Now()
	liveRetriggerLast.Unlock()

	if enqueueBrainForRecheck(domain, source) {
		log.Printf("⚠ живой сигнал провала (%s) для уже назначенного домена %s — на переоценку", source, domain)
	}
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
