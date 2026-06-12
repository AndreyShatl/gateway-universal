#!/bin/bash
# Zapret DPI bypass — унифицированный скрипт управления
# Генерируется install.sh с подстановкой ${LAN}

ZAPRET_DIR=/opt/zapret
CONFIG_DIR=/opt/zapret-config
NFQWS=$ZAPRET_DIR/nfq/nfqws
LAN="__LAN__"

FAKE_TLS="--dpi-desync-fake-tls=$ZAPRET_DIR/files/fake/tls_clienthello_www_google_com.bin"
FAKE_QUIC="--dpi-desync-fake-quic=$ZAPRET_DIR/files/fake/quic_initial_www_google_com.bin"

# Список сервисов (правит веб-UI). Пользовательский — в /etc/gateway, сид — рядом.
SERVICES=/etc/gateway/zapret-services.json
[ -f "$SERVICES" ] || SERVICES=$CONFIG_DIR/services.json

# build_proto <tcp|udp> <queue> — поднять nfqws со всеми сегментами этого протокола
# (по одному --new на сервис, порядок из JSON = приоритет) и навесить NFQUEUE.
build_proto() {
    local proto=$1 qnum=$2
    local first=1
    local -a cmd=("$NFQWS" --daemon --qnum=$qnum)
    local -a portsets=()
    # разделитель полей — Unit Separator (\037), не-whitespace: read не схлопывает пустые поля
    while IFS=$'\037' read -r df ports l7 desync; do
        [ -n "$ports" ] || continue
        desync=${desync//\$FAKE_TLS/$FAKE_TLS}
        desync=${desync//\$FAKE_QUIC/$FAKE_QUIC}
        [ $first -eq 1 ] || cmd+=(--new)
        first=0
        cmd+=(--filter-$proto=$ports)
        if [ -n "$l7" ]; then
            cmd+=(--filter-l7=$l7)
        elif [ -n "$df" ]; then
            cmd+=(--hostlist=$CONFIG_DIR/domains/$df)
        fi
        cmd+=($desync)
        case " ${portsets[*]} " in *" $ports "*) ;; *) portsets+=("$ports");; esac
    done < <(jq -r --arg p "$proto" '.[] | .domains_file as $df | .channels[] | select(.proto==$p) | [$df, .ports, (.l7 // ""), .desync] | join("\u001f")' "$SERVICES")

    [ $first -eq 1 ] && return 0   # нет сервисов этого протокола
    "${cmd[@]}"

    local ports ipt
    for ports in "${portsets[@]}"; do
        ipt=${ports//-/:}   # диапазоны: nfqws '-' -> iptables multiport ':'
        iptables -t mangle -A POSTROUTING -p $proto -m multiport --dports "$ipt" \
            -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
            -j NFQUEUE --queue-num $qnum --queue-bypass
    done
    echo "  Started $proto (queue $qnum)"
}

start() {
    echo "Starting Zapret..."

    # === Instagram QUIC bypass: allow UDP/443 to Meta IPs before global DROP ===
    # Без этого QUIC к Instagram блокируется и падает на TCP, который режет ТСПУ
    META_IPS="31.13.24.0/21 31.13.64.0/18 102.132.96.0/20 129.134.0.0/17 157.240.0.0/16 179.60.192.0/22 185.60.216.0/22"
    for cidr in $META_IPS; do
        iptables -t mangle -I PREROUTING 1 -p udp --dport 443 -s $LAN -d $cidr -j ACCEPT
        iptables -I FORWARD 1 -p udp --dport 443 -s $LAN -d $cidr -j ACCEPT
    done
    echo "  Allowed QUIC to Meta IPs (Instagram bypass)"

    # === Сервисы из JSON: очередь 200 (TCP) / 201 (UDP) ===
    build_proto tcp 200
    build_proto udp 201

    echo "Zapret started."
}

stop() {
    echo "Stopping Zapret..."
    killall nfqws 2>/dev/null
    iptables -t mangle -F POSTROUTING 2>/dev/null

    # Удалить Meta QUIC ACCEPT правила
    META_IPS="31.13.24.0/21 31.13.64.0/18 102.132.96.0/20 129.134.0.0/17 157.240.0.0/16 179.60.192.0/22 185.60.216.0/22"
    for cidr in $META_IPS; do
        iptables -t mangle -D PREROUTING -p udp --dport 443 -s $LAN -d $cidr -j ACCEPT 2>/dev/null
        iptables -D FORWARD -p udp --dport 443 -s $LAN -d $cidr -j ACCEPT 2>/dev/null
    done
    echo "Zapret stopped."
}

status() {
    echo "Zapret processes:"
    pgrep -a nfqws 2>/dev/null || echo "  Not running"
    echo ""
    echo "NFQUEUE rules:"
    iptables -t mangle -L POSTROUTING -n -v 2>/dev/null | grep NFQUEUE || echo "  No rules"
    echo ""
    echo "Meta QUIC ACCEPT (FORWARD):"
    iptables -L FORWARD -n 2>/dev/null | grep -E 'ACCEPT.*udp.*(31\.13|157\.240|102\.132|129\.134|185\.60|179\.60)' | wc -l
}

case "$1" in
    start) start ;;
    stop) stop ;;
    restart) stop; sleep 1; start ;;
    status) status ;;
    *) echo "Usage: $0 {start|stop|restart|status}" ;;
esac
