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
	"sync"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
)

type Candidate struct {
	SNI     string
	DstIP   string
	Signal  string
	Seen    time.Time
}

type Watcher struct {
	Iface       string
	VPSIP       string // исключаем трафик туннеля
	DryRun      bool
	OnCandidate func(Candidate)

	mu    sync.Mutex
	flows map[string]*flowState
}

type flowState struct {
	sni     string
	dstIP   string
	chAt    time.Time
	sawData bool
	emitted bool
}

func (w *Watcher) Run() error {
	handle, err := pcap.OpenLive(w.Iface, 1600, false, pcap.BlockForever)
	if err != nil {
		return fmt.Errorf("pcap open %s: %w", w.Iface, err)
	}
	defer handle.Close()
	// ловим только RST-пакеты и пакеты, начинающиеся с TLS-записи (0x16) — резко
	// снижает объём (не смотрим полезную нагрузку соединений).
	bpf := "tcp and port 443 and (tcp[tcpflags] & tcp-rst != 0 or tcp[((tcp[12]&0xf0)>>2)] = 0x16)"
	if w.VPSIP != "" {
		bpf += " and not host " + w.VPSIP
	}
	if err := handle.SetBPFFilter(bpf); err != nil {
		return fmt.Errorf("bpf: %w", err)
	}
	w.flows = map[string]*flowState{}
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
	tcpl := pkt.Layer(layers.LayerTypeTCP)
	if ipl == nil || tcpl == nil {
		return
	}
	ip := ipl.(*layers.IPv4)
	tcp := tcpl.(*layers.TCP)
	key := flowKey(ip.SrcIP, uint16(tcp.SrcPort), ip.DstIP, uint16(tcp.DstPort))

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
		w.flows[key] = &flowState{sni: parseSNI(pl), dstIP: ip.DstIP.String(), chAt: time.Now()}
		w.mu.Unlock()
	} else if len(pl) > 0 {
		w.mu.Lock()
		if f := w.flows[key]; f != nil {
			f.sawData = true // сервер прислал данные -> последующий RST не считаем блоком
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
			age := time.Since(f.chAt)
			if !f.sawData && !f.emitted && age > 3*time.Second && age < 12*time.Second {
				f.emitted = true
				out = append(out, Candidate{SNI: f.sni, DstIP: f.dstIP, Signal: "no-response-after-clienthello", Seen: time.Now()})
			}
			if age > 30*time.Second {
				delete(w.flows, k)
			}
		}
		w.mu.Unlock()
		for _, c := range out {
			w.emit(c)
		}
	}
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
