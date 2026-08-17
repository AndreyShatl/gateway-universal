package main

// udp_learn.go (T-ip-engine-phase1, 2026-08-17) — первый срез IP-движка из
// ТЗ "IP-маршрутизация для игр": игровой UDP-трафик к чистому IP (без SNI,
// DNS домен здесь ни при чём) раньше вообще не учитывался — main.go тихо
// логировал "udp-no-reply" и выходил, объяснение было "UDP-молчание
// неоднозначно" (многие сервисы, напр. AWS GameLift, не отвечают by design,
// не из-за блокировки — синтетическая проба тут в принципе невозможна: мы
// не знаем протокол игры, нечем "постучаться" и получить осмысленный ответ).
//
// Вместо синтетической пробы — пассивное накопление подтверждений от
// РЕАЛЬНОГО трафика (тот же принцип, что в остальном ТЗ: "что мы наблюдаем
// ≠ что мы решаем"). Один-единственный no-reply ничего не значит (потеря
// пакета, сервер задумался). Несколько подряд для ОДНОГО И ТОГО ЖЕ ip:port
// в короткое окно — уже значимый сигнал. Ключ намеренно ip:port (не просто
// ip) — flow identity из ТЗ: игровой UDP-порт того же IP не должен решать
// маршрут для другого порта/протокола на нём же.
//
// Гранулярность применения (T-ip-engine-phase1e, тем же вечером) — port-
// scoped: applier.ApplyIPPort() добавляет ip:port в отдельный hash:ip,port
// ipset (gw_autoroute_udp_pp), НЕ весь IP в общий gw_autoroute. Другой
// трафик к тому же облачному адресу на другом порту не затронут.

import (
	"fmt"
	"log"
	"sync"
	"time"

	"gateway-detector/applier"
)

const (
	udpLearnThreshold = 3                // подряд no-reply для одного ip:port, прежде чем добавить
	udpLearnWindow     = 3 * time.Minute // если тишина между сигналами дольше — счётчик обнуляется
)

type udpLearnState struct {
	lastSeen time.Time
	count    int
}

var udpLearn = struct {
	sync.Mutex
	m map[string]*udpLearnState
}{m: map[string]*udpLearnState{}}

// maybeLearnUDP — вызывается для каждого "udp-no-reply" кандидата. Возвращает
// true, если после этого вызова счётчик достиг порога и цель добавлена в
// автообход (для лога в вызывающем коде).
func maybeLearnUDP(dstIP string, port int, apply bool) bool {
	if dstIP == "" || port == 0 {
		return false
	}
	key := fmt.Sprintf("%s:%d/udp", dstIP, port)

	udpLearn.Lock()
	st, ok := udpLearn.m[key]
	if !ok {
		st = &udpLearnState{}
		udpLearn.m[key] = st
	}
	if time.Since(st.lastSeen) > udpLearnWindow {
		st.count = 0 // было тихо дольше окна — прошлые сигналы устарели
	}
	st.count++
	st.lastSeen = time.Now()
	reached := st.count >= udpLearnThreshold
	if reached {
		st.count = 0 // сброс — не долбим Apply() на каждый следующий пакет той же цели
	}
	udpLearn.Unlock()

	if !reached {
		log.Printf("🔵 UDP без ответа: %s:%d (%d/%d, накапливаю)", dstIP, port, st.count, udpLearnThreshold)
		return false
	}

	if !apply {
		log.Printf("🟡 БЫ добавил (UDP, %d подряд без ответа): %s:%d", udpLearnThreshold, dstIP, port)
		return false
	}
	if applier.ApplyIPPort(dstIP, port, "udp-no-reply") {
		log.Printf("✅ мгновенно в VPS (UDP, %d подряд без ответа, только этот порт): %s:%d", udpLearnThreshold, dstIP, port)
		return true
	}
	return false
}
