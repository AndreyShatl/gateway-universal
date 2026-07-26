// Package watcher — пассивный (pcap) детектор блокировок на ПРЯМОМ трафике.
// Читает КОПИЮ пакетов на WAN, в путь трафика не вмешивается (безопасно для
// живого шлюза). Сигнал: TLS ClientHello ушёл, а сервер ответил RST раньше,
// чем прислал данные — классическая подпись DPI-сброса по SNI.
//
// В dry-run только логирует кандидатов. Иначе — зовёт OnCandidate (дальше их
// подтверждает prober и применяет applier).
package watcher

import (
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
)

var debugQUIC = os.Getenv("DEBUG_QUIC") != ""

type Candidate struct {
	SNI    string
	DstIP  string
	Port   int // порт назначения (для syn-timeout на не-443, напр. игровые серверы)
	Signal string
	Seen   time.Time
}

type Watcher struct {
	Iface       string
	VPSIP       string // исключаем трафик туннеля
	DryRun      bool
	OnCandidate func(Candidate)

	mu       sync.Mutex
	flows    map[string]*flowState
	quic     map[string]*quicState
	udp      map[string]*udpState // dst:port -> счётчик UDP без ответа (не 443/53)
	synFails map[string]*synFail  // dst -> счётчик неудачных SYN (порог, чтобы не шуметь)
}

// synFail — сколько раз к данному dst SYN ушёл без SYN-ACK. Флагуем блок только
// после synThreshold попыток (одноразовые сбои happy-eyeballs/спекулятивные — мимо).
type synFail struct {
	count   int
	lastAt  time.Time
	emitted bool
}

const synThreshold = 5

// quicState — QUIC-соединение (HTTP/3): Initial ушёл, ждём ответ сервера.
type quicState struct {
	sni     string
	dstIP   string
	sentAt  time.Time
	gotResp bool
	emitted bool
}

// udpState — общий UDP (не 443/53): шлём пакеты, считаем ответы. Много ушло, ноль
// пришло = блок по IP (игровые сессии, напр. AWS GameLift). Флагуем по порогу.
type udpState struct {
	dstIP   string
	dstPort uint16
	sent    int
	recv    int
	firstAt time.Time
	emitted bool
}

const udpThreshold = 8 // пакетов ушло без единого ответа -> кандидат

type flowState struct {
	sni       string
	dstIP     string
	dstPort   uint16
	synAt     time.Time // время исходящего SYN (для сигнала syn-timeout)
	gotSynAck bool      // сервер прислал SYN-ACK -> соединение установилось
	chAt      time.Time
	sawData   bool
	emitted   bool
}

func (w *Watcher) Run() error {
	handle, err := pcap.OpenLive(w.Iface, 1600, false, pcap.BlockForever)
	if err != nil {
		return fmt.Errorf("pcap open %s: %w", w.Iface, err)
	}
	defer handle.Close()
	// ловим SYN/SYN-ACK (для сигнала syn-timeout), RST и пакеты, начинающиеся с
	// TLS-записи (0x16) — не смотрим полезную нагрузку соединений.
	// SYN/SYN-ACK ловим на ВСЕХ портах (блок игровых серверов по IP — не только 443);
	// RST и TLS-record — только на 443 (веб-сигналы); QUIC — udp/443.
	bpf := "((tcp and tcp[tcpflags] & tcp-syn != 0) or (tcp and port 443 and (tcp[tcpflags] & tcp-rst != 0 or tcp[((tcp[12]&0xf0)>>2)] = 0x16)) or (udp and not port 53))"
	if w.VPSIP != "" {
		bpf += " and not host " + w.VPSIP
	}
	if err := handle.SetBPFFilter(bpf); err != nil {
		return fmt.Errorf("bpf: %w", err)
	}
	w.Init()

	log.Printf("watcher: слушаю %s (dry-run=%v), фильтр: %s", w.Iface, w.DryRun, bpf)
	src := gopacket.NewPacketSource(handle, handle.LinkType())
	for pkt := range src.Packets() {
		w.handle(pkt)
	}
	return nil
}

// Init — завести карты состояния + фоновый cleaner. Общий вход и для pcap
// (Run), и для eBPF-источника (T59) — источник пакетов заменяем, состояние и
// пороговая логика (OnTCPPacket/OnUDPPacket/cleaner) — ОДНА и та же, не дублируем.
func (w *Watcher) Init() {
	w.flows = map[string]*flowState{}
	w.quic = map[string]*quicState{}
	w.udp = map[string]*udpState{}
	w.synFails = map[string]*synFail{}
	go w.cleaner()
}

func (w *Watcher) handle(pkt gopacket.Packet) {
	ipl := pkt.Layer(layers.LayerTypeIPv4)
	if ipl == nil {
		return
	}
	if udpl := pkt.Layer(layers.LayerTypeUDP); udpl != nil {
		ip := ipl.(*layers.IPv4)
		udp := udpl.(*layers.UDP)
		w.OnUDPPacket(ip.SrcIP, ip.DstIP, uint16(udp.SrcPort), uint16(udp.DstPort), udp.Payload)
		return
	}
	tcpl := pkt.Layer(layers.LayerTypeTCP)
	if tcpl == nil {
		return
	}
	ip := ipl.(*layers.IPv4)
	tcp := tcpl.(*layers.TCP)
	w.OnTCPPacket(ip.SrcIP, ip.DstIP, uint16(tcp.SrcPort), uint16(tcp.DstPort), tcp.SYN, tcp.ACK, tcp.RST, tcp.Payload)
}

// OnTCPPacket — та же логика, что раньше была инлайн в handle(), вынесена на
// примитивных параметрах (не gopacket-типах), чтобы её мог звать и eBPF-путь
// (T59, см. detector/ebpfsensor) без дублирования состояния/таймингов.
func (w *Watcher) OnTCPPacket(srcIP, dstIP net.IP, srcPort, dstPort uint16, syn, ack, rst bool, payload []byte) {
	key := flowKey(srcIP, srcPort, dstIP, dstPort)

	// SYN без ACK — исходящая попытка соединения (инициатор). Заводим flow, чтобы
	// поймать «SYN ушёл, SYN-ACK не пришёл» = блок по IP (SYN дропается).
	if syn && !ack {
		if isPrivate(dstIP) {
			return
		}
		w.mu.Lock()
		if w.flows[key] == nil {
			w.flows[key] = &flowState{dstIP: dstIP.String(), dstPort: dstPort, synAt: time.Now()}
		}
		w.mu.Unlock()
		return
	}
	// SYN-ACK — сервер ответил, соединение устанавливается: снимаем подозрение.
	if syn && ack {
		w.mu.Lock()
		if f := w.flows[key]; f != nil {
			f.gotSynAck = true
		}
		w.mu.Unlock()
		return
	}

	if rst {
		w.mu.Lock()
		f := w.flows[key]
		if f != nil && !f.sawData && time.Since(f.chAt) < 4*time.Second {
			cand := Candidate{SNI: f.sni, DstIP: f.dstIP, Signal: "rst-after-clienthello", Seen: time.Now()}
			delete(w.flows, key)
			w.mu.Unlock()
			w.emit(cand)
			return
		}
		w.mu.Unlock()
		return
	}

	pl := payload
	if len(pl) > 5 && pl[0] == 0x16 && pl[5] == 0x01 { // TLS handshake record, ClientHello
		// пропускаем ClientHello к приватным адресам (не интернет)
		if isPrivate(dstIP) {
			return
		}
		w.mu.Lock()
		f := w.flows[key]
		if f == nil {
			f = &flowState{dstIP: dstIP.String()}
			w.flows[key] = f
		}
		f.sni = parseSNI(pl)
		f.chAt = time.Now()
		f.gotSynAck = true // ClientHello ушёл -> соединение установлено
		w.mu.Unlock()
	} else if len(pl) > 0 {
		w.mu.Lock()
		if f := w.flows[key]; f != nil {
			f.sawData = true // сервер прислал данные -> последующий RST не считаем блоком
		}
		w.mu.Unlock()
	}
}

func (w *Watcher) handleUDP(ip *layers.IPv4, udp *layers.UDP) {
	w.OnUDPPacket(ip.SrcIP, ip.DstIP, uint16(udp.SrcPort), uint16(udp.DstPort), udp.Payload)
}

// OnUDPPacket — QUIC (HTTP/3) на :443 + общий UDP (не 443/53), логика вынесена
// на примитивных параметрах (см. OnTCPPacket) для переиспользования из eBPF-пути.
func (w *Watcher) OnUDPPacket(srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte) {
	// --- QUIC (HTTP/3) на 443 ---
	if dstPort == 443 || srcPort == 443 {
		pl := payload
		if dstPort == 443 && len(pl) >= 1200 && isQUICClientInitial(pl) {
			if isPrivate(dstIP) {
				return
			}
			key := flowKey(srcIP, srcPort, dstIP, dstPort)
			sni := parseQUICInitialSNI(pl)
			if debugQUIC {
				log.Printf("🔧 quic-initial dst=%s len=%d sni=%q", dstIP, len(pl), sni)
			}
			w.mu.Lock()
			if w.quic[key] == nil {
				w.quic[key] = &quicState{sni: sni, dstIP: dstIP.String(), sentAt: time.Now()}
			}
			w.mu.Unlock()
			return
		}
		if srcPort == 443 {
			key := flowKey(dstIP, dstPort, srcIP, srcPort)
			w.mu.Lock()
			if q := w.quic[key]; q != nil && q.dstIP == srcIP.String() {
				q.gotResp = true
			}
			w.mu.Unlock()
		}
		return
	}

	// --- общий UDP (не 443/53): «шлём, ответа нет» = блок по IP (игровые сессии) ---
	// внешний адрес должен быть обычным публичным unicast (не multicast/broadcast —
	// они не отвечают by design и дали бы ложные срабатывания).
	out := isPrivate(srcIP) && pubUnicast(dstIP) // исходящий к публичному
	in := pubUnicast(srcIP) && isPrivate(dstIP)  // входящий от публичного
	if out {
		key := fmt.Sprintf("%s:%d", dstIP, dstPort)
		w.mu.Lock()
		u := w.udp[key]
		if u == nil {
			u = &udpState{dstIP: dstIP.String(), dstPort: dstPort, firstAt: time.Now()}
			w.udp[key] = u
		}
		u.sent++
		w.mu.Unlock()
	} else if in {
		key := fmt.Sprintf("%s:%d", srcIP, srcPort)
		w.mu.Lock()
		if u := w.udp[key]; u != nil {
			u.recv++
		}
		w.mu.Unlock()
	}
}

func (w *Watcher) emit(c Candidate) {
	if w.DryRun || w.OnCandidate == nil {
		s := c.SNI
		if s == "" {
			s = "(без SNI)"
		}
		log.Printf("🔎 кандидат: sni=%s dst=%s сигнал=%s", s, c.DstIP, c.Signal)
		return
	}
	w.OnCandidate(c)
}

// cleaner: каждые 2с — эмитим «таймаут» (ClientHello ушёл, сервер молчит >3с,
// RST не пришёл = DROP-блок) и чистим старые записи.
func (w *Watcher) cleaner() {
	for range time.NewTicker(2 * time.Second).C {
		var out []Candidate
		w.mu.Lock()
		for k, f := range w.flows {
			// сигнал 1: ClientHello ушёл, сервер молчит >3с (DROP-блок по SNI)
			if !f.chAt.IsZero() {
				age := time.Since(f.chAt)
				if !f.sawData && !f.emitted && age > 3*time.Second && age < 12*time.Second {
					f.emitted = true
					out = append(out, Candidate{SNI: f.sni, DstIP: f.dstIP, Signal: "no-response-after-clienthello", Seen: time.Now()})
				}
			}
			// сигнал 2: SYN ушёл, SYN-ACK так и не пришёл >3с. Одна неудача — не блок
			// (happy-eyeballs, спекулятивные коннекты). Флагуем только после
			// synThreshold попыток к одному dst:port за окно.
			if !f.synAt.IsZero() && !f.gotSynAck && !f.emitted {
				age := time.Since(f.synAt)
				if age > 3*time.Second && age < 12*time.Second {
					f.emitted = true
					key := fmt.Sprintf("%s:%d", f.dstIP, f.dstPort)
					sf := w.synFails[key]
					if sf == nil {
						sf = &synFail{}
						w.synFails[key] = sf
					}
					sf.count++
					sf.lastAt = time.Now()
					if sf.count >= synThreshold && !sf.emitted {
						sf.emitted = true
						out = append(out, Candidate{DstIP: f.dstIP, Port: int(f.dstPort), Signal: "syn-timeout", Seen: time.Now()})
					}
				}
			}
			if since(f) > 30*time.Second {
				delete(w.flows, k)
			}
		}
		// QUIC (HTTP/3): Initial ушёл, сервер молчит >3с = блок. Эмитим только при
		// известном SNI (маршрут по IP для CDN с общими адресами опасен).
		for k, q := range w.quic {
			age := time.Since(q.sentAt)
			if !q.gotResp && !q.emitted && age > 3*time.Second && age < 12*time.Second {
				q.emitted = true
				if q.sni != "" {
					out = append(out, Candidate{SNI: q.sni, DstIP: q.dstIP, Signal: "quic-no-response", Seen: time.Now()})
				} else {
					log.Printf("🟣 QUIC-блок без SNI на %s — пропускаю (нельзя маршрутить по IP CDN)", q.dstIP)
				}
			}
			if age > 30*time.Second {
				delete(w.quic, k)
			}
		}
		// сброс счётчиков SYN без активности >15 мин (окно накопления попыток)
		for k, sf := range w.synFails {
			if time.Since(sf.lastAt) > 15*time.Minute {
				delete(w.synFails, k)
			}
		}
		// UDP (не 443/53): много ушло, ноль пришло >3с = блок по IP (игровые сессии)
		for k, u := range w.udp {
			age := time.Since(u.firstAt)
			if u.recv == 0 && u.sent >= udpThreshold && !u.emitted && age > 3*time.Second && age < 30*time.Second {
				u.emitted = true
				out = append(out, Candidate{DstIP: u.dstIP, Port: int(u.dstPort), Signal: "udp-no-reply", Seen: time.Now()})
			}
			if age > 60*time.Second {
				delete(w.udp, k)
			}
		}
		w.mu.Unlock()
		for _, c := range out {
			w.emit(c)
		}
	}
}

// since — возраст flow по самой поздней известной метке (SYN или ClientHello).
func since(f *flowState) time.Duration {
	t := f.synAt
	if f.chAt.After(t) {
		t = f.chAt
	}
	return time.Since(t)
}

func flowKey(a net.IP, ap uint16, b net.IP, bp uint16) string {
	s1, s2 := fmt.Sprintf("%s:%d", a, ap), fmt.Sprintf("%s:%d", b, bp)
	if s1 < s2 {
		return s1 + "-" + s2
	}
	return s2 + "-" + s1
}

func isPrivate(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// pubUnicast — обычный публичный unicast (не приватный, не multicast/broadcast/
// loopback). Для udp-no-reply, чтобы не ловить multicast (он не отвечает by design).
func pubUnicast(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate()
}

// parseSNI — извлечь server_name из TLS ClientHello (payload TLS-записи).
func parseSNI(b []byte) string {
	// TLS record: type(1) ver(2) len(2) | handshake: type(1) len(3) ver(2) random(32) ...
	if len(b) < 43 {
		return ""
	}
	p := b[5:] // handshake
	if len(p) < 38 || p[0] != 0x01 {
		return ""
	}
	p = p[38:] // после type(1)+len(3)+ver(2)+random(32)
	// session id
	if len(p) < 1 {
		return ""
	}
	sl := int(p[0])
	p = p[1:]
	if len(p) < sl+2 {
		return ""
	}
	p = p[sl:]
	// cipher suites
	cl := int(p[0])<<8 | int(p[1])
	p = p[2:]
	if len(p) < cl+1 {
		return ""
	}
	p = p[cl:]
	// compression
	ml := int(p[0])
	p = p[1:]
	if len(p) < ml+2 {
		return ""
	}
	p = p[ml:]
	// extensions
	el := int(p[0])<<8 | int(p[1])
	p = p[2:]
	if len(p) > el {
		p = p[:el]
	}
	for len(p) >= 4 {
		et := int(p[0])<<8 | int(p[1])
		xl := int(p[2])<<8 | int(p[3])
		p = p[4:]
		if len(p) < xl {
			return ""
		}
		if et == 0x0000 { // server_name
			x := p[:xl]
			if len(x) < 5 {
				return ""
			}
			// list len(2) type(1) name len(2) name
			nl := int(x[3])<<8 | int(x[4])
			if len(x) < 5+nl {
				return ""
			}
			return string(x[5 : 5+nl])
		}
		p = p[xl:]
	}
	return ""
}
