#!/bin/bash
# Zapret DPI bypass — унифицированный скрипт управления
# Генерируется install.sh с подстановкой ${LAN}

ZAPRET_DIR=/opt/zapret
CONFIG_DIR=/opt/zapret-config
NFQWS=$ZAPRET_DIR/nfq/nfqws
LAN="__LAN__"

start() {
    echo "Starting Zapret..."

    # === Стратегии desync по блокам (калиброванные дефолты) ===
    # Переопределяются из /etc/gateway/zapret-overrides.env (правит веб-UI):
    # переменная DESYNC_<BLOCK> содержит полный набор --dpi-desync* флагов.
    FAKE_TLS="--dpi-desync-fake-tls=$ZAPRET_DIR/files/fake/tls_clienthello_www_google_com.bin"
    FAKE_QUIC="--dpi-desync-fake-quic=$ZAPRET_DIR/files/fake/quic_initial_www_google_com.bin"
    DESYNC_TCP_INSTAGRAM="--dpi-desync=fake,multidisorder --dpi-desync-split-pos=1,midsld --dpi-desync-repeats=11 --dpi-desync-fooling=md5sig,badseq --dpi-desync-autottl=2:2-12 $FAKE_TLS"
    DESYNC_TCP_DISCORD="--dpi-desync=fake,fakedsplit --dpi-desync-repeats=6 --dpi-desync-fooling=ts $FAKE_TLS"
    DESYNC_TCP_GENERAL="--dpi-desync=fake,fakedsplit --dpi-desync-repeats=6 --dpi-desync-fooling=ts $FAKE_TLS"
    DESYNC_TCP_YOUTUBE="--dpi-desync=fake,fakedsplit --dpi-desync-repeats=6 --dpi-desync-fooling=ts"
    DESYNC_UDP_INSTAGRAM="--dpi-desync=fake --dpi-desync-repeats=6 $FAKE_QUIC"
    DESYNC_UDP_GENERAL="--dpi-desync=fake --dpi-desync-repeats=6 $FAKE_QUIC"
    DESYNC_UDP_DISCORD="--dpi-desync=fake --dpi-desync-repeats=6"
    # shellcheck disable=SC1091
    [ -f /etc/gateway/zapret-overrides.env ] && . /etc/gateway/zapret-overrides.env

    # === Instagram QUIC bypass: allow UDP/443 to Meta IPs before global DROP ===
    # Без этого QUIC к Instagram блокируется и падает на TCP, который режет ТСПУ
    META_IPS="31.13.24.0/21 31.13.64.0/18 102.132.96.0/20 129.134.0.0/17 157.240.0.0/16 179.60.192.0/22 185.60.216.0/22"
    for cidr in $META_IPS; do
        iptables -t mangle -I PREROUTING 1 -p udp --dport 443 -s $LAN -d $cidr -j ACCEPT
        iptables -I FORWARD 1 -p udp --dport 443 -s $LAN -d $cidr -j ACCEPT
    done
    echo "  Allowed QUIC to Meta IPs (Instagram bypass)"

    # === TCP unified instance (queue 200) ===
    # Все TCP стратегии в одном nfqws через --new (порядок важен)
    $NFQWS --daemon --qnum=200 \
        --filter-tcp=443 --hostlist=$CONFIG_DIR/domains/instagram.txt $DESYNC_TCP_INSTAGRAM \
      --new \
        --filter-tcp=443,2053,2083,2087,2096,8443 --hostlist=$CONFIG_DIR/domains/discord.txt $DESYNC_TCP_DISCORD \
      --new \
        --filter-tcp=80,443 --hostlist=$CONFIG_DIR/domains/general.txt $DESYNC_TCP_GENERAL \
      --new \
        --filter-tcp=80,443 --hostlist=$CONFIG_DIR/domains/youtube.txt $DESYNC_TCP_YOUTUBE

    # TCP iptables — одно правило на уникальный набор портов
    iptables -t mangle -A POSTROUTING -p tcp -m multiport --dports 80,443 \
        -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
        -j NFQUEUE --queue-num 200 --queue-bypass
    iptables -t mangle -A POSTROUTING -p tcp -m multiport --dports 2053,2083,2087,2096,8443 \
        -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
        -j NFQUEUE --queue-num 200 --queue-bypass

    echo "  Started unified TCP (queue 200)"

    # === UDP unified instance (queue 201) ===
    $NFQWS --daemon --qnum=201 \
        --filter-udp=443 --hostlist=$CONFIG_DIR/domains/instagram.txt $DESYNC_UDP_INSTAGRAM \
      --new \
        --filter-udp=443 $DESYNC_UDP_GENERAL \
      --new \
        --filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun $DESYNC_UDP_DISCORD

    # UDP iptables
    iptables -t mangle -A POSTROUTING -p udp --dport 443 \
        -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
        -j NFQUEUE --queue-num 201 --queue-bypass
    iptables -t mangle -A POSTROUTING -p udp -m multiport --dports 19294:19344,50000:50100 \
        -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
        -j NFQUEUE --queue-num 201 --queue-bypass

    echo "  Started unified UDP (queue 201)"
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
