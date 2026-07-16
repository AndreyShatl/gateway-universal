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

	mu    sync.Mutex
	flows map[string]*flowState
	quic  map[string]*quicState
}

// quicState — QUIC-соединение (HTTP/3): Initial ушёл, ждём ответ сервера.
type quicState struct {
	sni     string
	dstIP   string
	sentAt  time.Time
	gotResp bool
	emitted bool
}

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
	bpf := "((tcp and tcp[tcpflags] & tcp-syn != 0) or (tcp and port 443 and (tcp[tcpflags] & tcp-rst != 0 or tcp[((tcp[12]&0xf0)>>2)] = 0x16)) or (udp and port 443))"
	if w.VPSIP != "" {
		bpf += " and not host " + w.VPSIP
	}
	if err := handle.SetBPFFilter(bpf); err != nil {
		return fmt.Errorf("bpf: %w", err)
	}
	w.flows = map[string]*flowState{}
	w.quic = map[string]*quicState{}
	go w.cleaner()

	log.Printf("watcher: слушаю %s (dry-run=%v), фильтр: %s", w.Iface, w.DryRun, bpf)
	src := gopacket.NewPacketSource(handle, handle.LinkType())
	for pkt := range src.Packets() {
		w.handle(pkt)
	}
	return nil
}

func (w *Watcher) handle(pkt gopacket.Packet) {
	ipl := pkt.Layer(layers.LayerTypeIPv4)
	if ipl == nil {
		return
	}
	if udpl := pkt.Layer(layers.LayerTypeUDP); udpl != nil {
		w.handleUDP(ipl.(*layers.IPv4), udpl.(*layers.UDP))
		return
	}
	tcpl := pkt.Layer(layers.LayerTypeTCP)
	if tcpl == nil {
		return
	}
	ip := ipl.(*layers.IPv4)
	tcp := tcpl.(*layers.TCP)
	key := flowKey(ip.SrcIP, uint16(tcp.SrcPort), ip.DstIP, uint16(tcp.DstPort))

	// SYN без ACK — исходящая попытка соединения (инициатор). Заводим flow, чтобы
	// поймать «SYN ушёл, SYN-ACK не пришёл» = блок по IP (SYN дропается).
	if tcp.SYN && !tcp.ACK {
		if isPrivate(ip.DstIP) {
			return
		}
		w.mu.Lock()
		if w.flows[key] == nil {
			w.flows[key] = &flowState{dstIP: ip.DstIP.String(), dstPort: uint16(tcp.DstPort), synAt: time.Now()}
		}
		w.mu.Unlock()
		return
	}
	// SYN-ACK — сервер ответил, соединение устанавливается: снимаем подозрение.
	if tcp.SYN && tcp.ACK {
		w.mu.Lock()
		if f := w.flows[key]; f != nil {
			f.gotSynAck = true
		}
		w.mu.Unlock()
		return
	}

	if tcp.RST {
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

	pl := tcp.Payload
	if len(pl) > 5 && pl[0] == 0x16 && pl[5] == 0x01 { // TLS handshake record, ClientHello
		// пропускаем ClientHello к приватным адресам (не интернет)
		if isPrivate(ip.DstIP) {
			return
		}
		w.mu.Lock()
		f := w.flows[key]
		if f == nil {
			f = &flowState{dstIP: ip.DstIP.String()}
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

// handleUDP — QUIC (HTTP/3) на :443. Ловим клиентский Initial (расшифровываем SNI)
// и следим, ответил ли сервер. Молчание сервера >3с после Initial = блок HTTP/3.
func (w *Watcher) handleUDP(ip *layers.IPv4, udp *layers.UDP) {
	if udp.SrcPort != 443 && udp.DstPort != 443 {
		return
	}
	pl := udp.Payload
	// клиентский Initial — исходящий (dst:443) и это разбираемый QUIC Initial
	if udp.DstPort == 443 && len(pl) >= 1200 && isQUICClientInitial(pl) {
		if isPrivate(ip.DstIP) {
			return
		}
		key := flowKey(ip.SrcIP, uint16(udp.SrcPort), ip.DstIP, uint16(udp.DstPort))
		sni := parseQUICInitialSNI(pl)
		if debugQUIC {
			log.Printf("🔧 quic-initial dst=%s len=%d sni=%q", ip.DstIP, len(pl), sni)
		}
		w.mu.Lock()
		if w.quic[key] == nil {
			w.quic[key] = &quicState{sni: sni, dstIP: ip.DstIP.String(), sentAt: time.Now()}
		}
		w.mu.Unlock()
		return
	}
	// ответ сервера (src:443) — соединение живо, снимаем подозрение
	if udp.SrcPort == 443 {
		key := flowKey(ip.DstIP, uint16(udp.DstPort), ip.SrcIP, uint16(udp.SrcPort))
		w.mu.Lock()
		if q := w.quic[key]; q != nil && q.dstIP == ip.SrcIP.String() {
			q.gotResp = true
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
			// сигнал 2: SYN ушёл, SYN-ACK так и не пришёл >3с (блок по IP, SYN дропается)
			if !f.synAt.IsZero() && !f.gotSynAck && !f.emitted {
				age := time.Since(f.synAt)
				if age > 3*time.Second && age < 12*time.Second {
					f.emitted = true
					out = append(out, Candidate{DstIP: f.dstIP, Port: int(f.dstPort), Signal: "syn-timeout", Seen: time.Now()})
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
