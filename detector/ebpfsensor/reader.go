// Package ebpfsensor (T59) — eBPF-источник событий для detector/watcher,
// замена pcap-захвата. Классификация пакетов — в sensor.c (ядро), пороги и
// агрегация — та же самая Go-логика watcher.Watcher (OnTCPPacket/OnUDPPacket),
// не дублируется.
package ebpfsensor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"

	"gateway-detector/watcher"
)

const (
	evtTCPSyn    = 1
	evtTCPSynAck = 2
	evtTCPRst    = 3
	evtTCPData   = 4
	evtUDP       = 5
)

const maxPayload = 1480

// rawEvent — БУКВАЛЬНО повторяет struct event из sensor.c (естественное
// выравнивание, без packed) — порядок полей и паддинг менять только вместе.
type rawEvent struct {
	Type       uint8
	_          [3]byte
	SAddr      uint32
	DAddr      uint32
	SPort      uint16
	DPort      uint16
	PayloadLen uint32
	Payload    [maxPayload]byte
}

type Sensor struct {
	objs        sensorObjects
	linkIngress link.Link
	linkEgress  link.Link
	reader      *ringbuf.Reader
}

// Load вешает sensor.c на TCX ingress+egress интерфейса (обе стороны нужны:
// egress — исходящий трафик LAN-клиентов через шлюз, ingress — ответы серверов).
func Load(iface string) (*Sensor, error) {
	ifaceObj, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", iface, err)
	}
	var objs sensorObjects
	if err := loadSensorObjects(&objs, nil); err != nil {
		return nil, fmt.Errorf("load bpf objects: %w", err)
	}
	li, err := link.AttachTCX(link.TCXOptions{Interface: ifaceObj.Index, Program: objs.GwSensor, Attach: ebpf.AttachTCXIngress})
	if err != nil {
		objs.Close()
		return nil, fmt.Errorf("attach tcx ingress: %w", err)
	}
	le, err := link.AttachTCX(link.TCXOptions{Interface: ifaceObj.Index, Program: objs.GwSensor, Attach: ebpf.AttachTCXEgress})
	if err != nil {
		li.Close()
		objs.Close()
		return nil, fmt.Errorf("attach tcx egress: %w", err)
	}
	rd, err := ringbuf.NewReader(objs.Events)
	if err != nil {
		le.Close()
		li.Close()
		objs.Close()
		return nil, fmt.Errorf("ringbuf reader: %w", err)
	}
	return &Sensor{objs: objs, linkIngress: li, linkEgress: le, reader: rd}, nil
}

func (s *Sensor) Close() error {
	s.reader.Close()
	s.linkEgress.Close()
	s.linkIngress.Close()
	return s.objs.Close()
}

// Run — читает ring buffer в цикле, кормит события в w (watcher.Watcher уже
// инициализирован — Init() + фоновый cleaner) до ошибки/закрытия. vpsIP —
// трафик к/от VPS исключаем (как "and not host VPSIP" в pcap-фильтре).
func (s *Sensor) Run(w *watcher.Watcher, vpsIP string) error {
	for {
		rec, err := s.reader.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return nil
			}
			log.Printf("ebpfsensor: read: %v", err)
			continue
		}
		if len(rec.RawSample) < 20 { // минимум до конца payload_len
			continue
		}
		typ := rec.RawSample[0]
		// saddr/daddr — 4 сырых байта пакета (сетевой порядок = порядок октетов
		// IP-адреса), просто как есть в net.IPv4, никакого byte-swap не нужно.
		srcIP := net.IPv4(rec.RawSample[4], rec.RawSample[5], rec.RawSample[6], rec.RawSample[7])
		dstIP := net.IPv4(rec.RawSample[8], rec.RawSample[9], rec.RawSample[10], rec.RawSample[11])
		sPort := binary.LittleEndian.Uint16(rec.RawSample[12:14])
		dPort := binary.LittleEndian.Uint16(rec.RawSample[14:16])
		payloadLen := binary.LittleEndian.Uint32(rec.RawSample[16:20])
		var payload []byte
		if payloadLen > 0 && len(rec.RawSample) >= 20+int(payloadLen) {
			payload = rec.RawSample[20 : 20+int(payloadLen)]
		}
		if vpsIP != "" && (srcIP.String() == vpsIP || dstIP.String() == vpsIP) {
			continue
		}

		switch typ {
		case evtTCPSyn:
			w.OnTCPPacket(srcIP, dstIP, sPort, dPort, true, false, false, nil)
		case evtTCPSynAck:
			w.OnTCPPacket(srcIP, dstIP, sPort, dPort, true, true, false, nil)
		case evtTCPRst:
			w.OnTCPPacket(srcIP, dstIP, sPort, dPort, false, false, true, nil)
		case evtTCPData:
			// без payload (не TLS-record) — всё равно сигнализируем "данные пошли"
			// непустым срезом ([]byte{0}), чтобы sawData=true в OnTCPPacket
			if payload == nil {
				payload = []byte{0}
			}
			w.OnTCPPacket(srcIP, dstIP, sPort, dPort, false, false, false, payload)
		case evtUDP:
			if payload == nil {
				payload = []byte{}
			}
			w.OnUDPPacket(srcIP, dstIP, sPort, dPort, payload)
		}
	}
}
