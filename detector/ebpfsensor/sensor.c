//go:build ignore
// sensor.c — T59: классификатор пакетов для авто-обхода (замена pcap-захвата
// из detector/watcher). Смотрит TCP SYN/SYN-ACK (все порты — блок игровых
// серверов по IP), RST + TLS ClientHello (порт 443), UDP включая QUIC Initial
// (порт 443) — шлёт события через ring buffer в Go, где ПЕРЕИСПОЛЬЗУЕТСЯ
// существующая логика порогов/агрегации (Watcher.OnTCPPacket/OnUDPPacket),
// не дублируется. Полезная нагрузка копируется в событие ТОЛЬКО когда реально
// нужна (TLS-record/QUIC Initial) — иначе событие без payload, дёшево.
//
// bpf_skb_load_bytes вместо прямых data/data_end указателей — надёжнее
// проходит верификатор в TC-программах с динамическими смещениями.

#include <linux/bpf.h>
#include <linux/pkt_cls.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>

// barrier_var (из bpf_helpers.h) заставляет верификатор перепроверить границы
// значения на месте использования: без этого clang доказывает copy>0 на
// уровне C и убирает нижнюю проверку как мёртвый код, а верификатор теряет
// эту гарантию после усечения до 32 бит.

#define ETH_HLEN 14
#define MAX_PAYLOAD 1480
#define IPPROTO_TCP_ 6
#define IPPROTO_UDP_ 17
#define ETH_P_IP_ 0x0800

enum {
	EVT_TCP_SYN    = 1,
	EVT_TCP_SYNACK = 2,
	EVT_TCP_RST    = 3,
	EVT_TCP_DATA   = 4, /* порт 443: либо TLS-record (с payload), либо просто "данные пошли" (без payload) */
	EVT_UDP        = 5, /* порт 443: QUIC (Initial — с payload, иначе без); прочее — только occurrence */
};

struct event {
	__u8  type;
	__u8  _pad[3];
	__u32 saddr;
	__u32 daddr;
	__u16 sport;
	__u16 dport;
	__u32 payload_len;
	__u8  payload[MAX_PAYLOAD];
};

struct {
	__uint(type, BPF_MAP_TYPE_RINGBUF);
	__uint(max_entries, 1 << 20); /* 1MB */
} events SEC(".maps");

static __always_inline struct event *reserve(void) {
	return bpf_ringbuf_reserve(&events, sizeof(struct event), 0);
}

SEC("tc")
int gw_sensor(struct __sk_buff *skb) {
	__u16 eth_proto;
	if (bpf_skb_load_bytes(skb, 12, &eth_proto, 2) < 0)
		return TC_ACT_OK;
	if (bpf_ntohs(eth_proto) != ETH_P_IP_)
		return TC_ACT_OK;

	__u8 ver_ihl;
	if (bpf_skb_load_bytes(skb, ETH_HLEN, &ver_ihl, 1) < 0)
		return TC_ACT_OK;
	__u32 ip_hlen = (ver_ihl & 0x0F) * 4;
	if (ip_hlen < 20)
		return TC_ACT_OK;

	__u8 proto;
	if (bpf_skb_load_bytes(skb, ETH_HLEN + 9, &proto, 1) < 0)
		return TC_ACT_OK;

	__u32 saddr, daddr;
	bpf_skb_load_bytes(skb, ETH_HLEN + 12, &saddr, 4);
	bpf_skb_load_bytes(skb, ETH_HLEN + 16, &daddr, 4);

	__u32 l4_off = ETH_HLEN + ip_hlen;
	__u32 total_len = skb->len;

	if (proto == IPPROTO_TCP_) {
		__u16 sport, dport;
		bpf_skb_load_bytes(skb, l4_off + 0, &sport, 2);
		bpf_skb_load_bytes(skb, l4_off + 2, &dport, 2);
		sport = bpf_ntohs(sport);
		dport = bpf_ntohs(dport);

		__u8 doff_byte, flags;
		if (bpf_skb_load_bytes(skb, l4_off + 12, &doff_byte, 1) < 0)
			return TC_ACT_OK;
		if (bpf_skb_load_bytes(skb, l4_off + 13, &flags, 1) < 0)
			return TC_ACT_OK;
		__u32 tcp_hlen = ((doff_byte >> 4) & 0xF) * 4;
		if (tcp_hlen < 20)
			return TC_ACT_OK;

		int syn = flags & 0x02;
		int ack = flags & 0x10;
		int rst = flags & 0x04;
		int is443 = (sport == 443 || dport == 443);

		__u8 type = 0;
		if (syn && !ack) {
			type = EVT_TCP_SYN; /* все порты */
		} else if (syn && ack) {
			type = EVT_TCP_SYNACK; /* все порты */
		} else if (rst && is443) {
			type = EVT_TCP_RST;
		} else if (is443) {
			__u32 payload_off = l4_off + tcp_hlen;
			__u32 plen = total_len > payload_off ? total_len - payload_off : 0;
			if (plen == 0)
				return TC_ACT_OK; // голый ACK — не интересно
			type = EVT_TCP_DATA;

			struct event *e = reserve();
			if (!e)
				return TC_ACT_OK;
			e->type = type;
			e->saddr = saddr;
			e->daddr = daddr;
			e->sport = sport;
			e->dport = dport;
			e->payload_len = 0;
			// нужен payload только если это похоже на TLS-record (0x16) —
			// первый байт проверяем отдельным маленьким чтением, дёшево
			__u8 first;
			if (bpf_skb_load_bytes(skb, payload_off, &first, 1) == 0 && first == 0x16) {
				__u32 copy = plen;
				barrier_var(copy);
				if (copy < 1)
					copy = 1;
				if (copy > MAX_PAYLOAD)
					copy = MAX_PAYLOAD;
				if (bpf_skb_load_bytes(skb, payload_off, e->payload, copy) == 0)
					e->payload_len = copy;
			}
			bpf_ringbuf_submit(e, 0);
			return TC_ACT_OK;
		} else {
			return TC_ACT_OK; // не 443, не SYN/SYN-ACK — не интересно
		}

		struct event *e = reserve();
		if (!e)
			return TC_ACT_OK;
		e->type = type;
		e->saddr = saddr;
		e->daddr = daddr;
		e->sport = sport;
		e->dport = dport;
		e->payload_len = 0;
		bpf_ringbuf_submit(e, 0);
		return TC_ACT_OK;
	}

	if (proto == IPPROTO_UDP_) {
		__u16 sport, dport;
		bpf_skb_load_bytes(skb, l4_off + 0, &sport, 2);
		bpf_skb_load_bytes(skb, l4_off + 2, &dport, 2);
		sport = bpf_ntohs(sport);
		dport = bpf_ntohs(dport);
		if (dport == 53 || sport == 53)
			return TC_ACT_OK; // DNS — не интересно (как и в pcap-фильтре)

		__u32 payload_off = l4_off + 8; /* udphdr = 8 байт */
		__u32 plen = total_len > payload_off ? total_len - payload_off : 0;

		struct event *e = reserve();
		if (!e)
			return TC_ACT_OK;
		e->type = EVT_UDP;
		e->saddr = saddr;
		e->daddr = daddr;
		e->sport = sport;
		e->dport = dport;
		e->payload_len = 0;

		// payload нужен только для потенциального QUIC Initial (dport==443,
		// long-header бит виден по первому байту) — иначе событие пустое,
		// Go-стороне для occurrence-логики (sent/recv, gotResp) хватит meta.
		if (dport == 443 && plen >= 1200) {
			__u8 first;
			if (bpf_skb_load_bytes(skb, payload_off, &first, 1) == 0 &&
			    (first & 0x80) && (first & 0x40)) {
				__u32 copy = plen;
				barrier_var(copy);
				if (copy < 1)
					copy = 1;
				if (copy > MAX_PAYLOAD)
					copy = MAX_PAYLOAD;
				if (bpf_skb_load_bytes(skb, payload_off, e->payload, copy) == 0)
					e->payload_len = copy;
			}
		}
		bpf_ringbuf_submit(e, 0);
		return TC_ACT_OK;
	}

	return TC_ACT_OK;
}

char _license[] SEC("license") = "GPL";
