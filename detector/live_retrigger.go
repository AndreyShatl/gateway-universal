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

// T-instant-failover (2026-08-17) — было: T-circuit-breaker ждал
// liveRetriggerBreakerThreshold=3 живых сигналов провала за час, прежде чем
// принудительно перевести домен на VPS — реальный пользователь мог словить
// несколько обрывов подряд (живой кейс: Discord "checking for update"),
// прежде чем защита срабатывала. Разобрали с пользователем: сигнатуры
// watcher'а (rst-after-clienthello/syn-timeout/no-response-after-
// clienthello/quic-no-response) по конструкции УЖЕ означают подозрение на
// реальную блокировку, не общий шум — ждать подтверждения тем же
// структурно слепым curl-тестом (см. исходный комментарий T-circuit-breaker
// про updates.discord.com) только продлевает боль без выигрыша в
// надёжности. Теперь первый же сигнал (после cooldown — не дребезжим на
// потоке пакетов одного и того же обрыва) сразу переводит домен на VPS.
// Дальнейший подбор DPI-стратегии — не здесь и не тем же тестом сразу же
// (это и был первоначальный источник бесконечного цикла), а обычной ночной
// переоценкой (brain-nightly.sh перебирает ВСЕ управляемые домены, включая
// только что форсированные на VPS).
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
	last, ok := liveRetriggerLast.m[domain]
	if ok && time.Since(last) < liveRetriggerCooldown {
		liveRetriggerLast.Unlock()
		return // тот же обрыв уже отреагирован недавно, не дребезжим
	}
	liveRetriggerLast.m[domain] = time.Now()
	liveRetriggerLast.Unlock()

	forceVPSInstant(domain, source)
}

// forceVPSInstant — та же команда, что кнопка "закрепить на VPS" в UI
// (T-vps-pin) применяет к одному домену, но БЕЗ закрепления сервиса целиком
// (mode сервиса не трогаем — это защита для одного проблемного домена, не
// решение "весь Discord теперь навсегда на VPS"). Ближайшая ночная
// переоценка (brain-nightly.sh) сама попробует DPI для этого домена заново.
func forceVPSInstant(domain, source string) {
	cmd := exec.Command("bash", "/opt/gateway-brain/brain-apply.sh", "vps", domain)
	if err := cmd.Run(); err != nil {
		log.Printf("⚠ живой сигнал провала (%s) для %s — не удалось перевести на VPS: %v", source, domain, err)
		return
	}
	log.Printf("🔴 живой сигнал провала (%s) для %s — мгновенно переведён на VPS", source, domain)
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
