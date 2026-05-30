#!/bin/bash
# Zapret DPI bypass — унифицированный скрипт управления
# 6 отдельных nfqws процессов: TCP+UDP для YouTube, Discord, Instagram
# Генерируется install.sh с подстановкой ${LAN}

ZAPRET_DIR=/opt/zapret
CONFIG_DIR=/opt/zapret-config
NFQWS=$ZAPRET_DIR/nfq/nfqws
LAN="__LAN__"
META_IPS="31.13.24.0/21 31.13.64.0/18 102.132.96.0/20 129.134.0.0/17 157.240.0.0/16 179.60.192.0/22 185.60.216.0/22"

FAKE_TLS=$ZAPRET_DIR/files/fake/tls_clienthello_www_google_com.bin
FAKE_QUIC=$ZAPRET_DIR/files/fake/quic_initial_www_google_com.bin
FAKE_QUIC_VK=$ZAPRET_DIR/files/fake/quic_initial_vk_com.bin
FAKE_DISCORD=$ZAPRET_DIR/files/fake/discord-ip-discovery-with-port.bin
FAKE_STUN=$ZAPRET_DIR/files/fake/stun.bin

cleanup_meta_rules() {
    for cidr in $META_IPS; do
        while iptables -t mangle -D PREROUTING -p udp --dport 443 -s $LAN -d $cidr -j ACCEPT 2>/dev/null; do :; done
        while iptables -D FORWARD -p udp --dport 443 -s $LAN -d $cidr -j ACCEPT 2>/dev/null; do :; done
    done
}

start() {
    echo "Starting Zapret (6 strategies: YouTube/Discord/Instagram × TCP/UDP)..."

    # === Instagram QUIC bypass: allow UDP/443 to Meta IPs ===
    cleanup_meta_rules
    for cidr in $META_IPS; do
        iptables -t mangle -I PREROUTING 1 -p udp --dport 443 -s $LAN -d $cidr -j ACCEPT
        iptables -I FORWARD 1 -p udp --dport 443 -s $LAN -d $cidr -j ACCEPT
    done
    echo "  Allowed QUIC to Meta IPs (Instagram bypass)"

    # =========================================================
    # YOUTUBE TCP (queue 200)
    # Ключевые параметры: --ip-id=zero + --dpi-desync-fakedsplit-pattern=0x00
    # =========================================================
    $NFQWS --daemon --qnum=200 \
        --filter-tcp=80,443 --hostlist=$CONFIG_DIR/domains/youtube.txt --ip-id=zero \
            --dpi-desync=fake,fakedsplit \
            --dpi-desync-repeats=6 \
            --dpi-desync-fooling=ts \
            --dpi-desync-fakedsplit-pattern=0x00 \
            --dpi-desync-fake-tls=$FAKE_TLS

    iptables -t mangle -A POSTROUTING -p tcp -m multiport --dports 80,443 \
        --hostlist=$CONFIG_DIR/domains/youtube.txt 2>/dev/null || \
    iptables -t mangle -A POSTROUTING -p tcp -m multiport --dports 80,443 \
        -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
        -j NFQUEUE --queue-num 200 --queue-bypass
    echo "  YouTube TCP started (queue 200)"

    # =========================================================
    # YOUTUBE UDP (queue 201) — QUIC bypass
    # =========================================================
    $NFQWS --daemon --qnum=201 \
        --filter-udp=443 --hostlist=$CONFIG_DIR/domains/youtube.txt \
            --dpi-desync=fake \
            --dpi-desync-repeats=6 \
            --dpi-desync-fake-quic=$FAKE_QUIC

    iptables -t mangle -A POSTROUTING -p udp --dport 443 \
        -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
        -j NFQUEUE --queue-num 201 --queue-bypass
    echo "  YouTube UDP started (queue 201)"

    # =========================================================
    # DISCORD TCP (queue 202)
    # Порты: 443 + Discord-специфичные (2053,2083,2087,2096,8443)
    # =========================================================
    $NFQWS --daemon --qnum=202 \
        --filter-tcp=443,2053,2083,2087,2096,8443 --hostlist=$CONFIG_DIR/domains/discord.txt \
            --dpi-desync=fake,fakedsplit \
            --dpi-desync-repeats=6 \
            --dpi-desync-fooling=ts \
            --dpi-desync-fakedsplit-pattern=0x00 \
            --dpi-desync-fake-tls=$FAKE_TLS

    iptables -t mangle -A POSTROUTING -p tcp -m multiport --dports 443,2053,2083,2087,2096,8443 \
        -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
        -j NFQUEUE --queue-num 202 --queue-bypass
    echo "  Discord TCP started (queue 202)"

    # =========================================================
    # DISCORD UDP (queue 203)
    # Порты 19294-19344 (Discord voice primary) + 50000-50100
    # + 10000-65535 (Discord RTP/голос общий диапазон)
    # =========================================================
    $NFQWS --daemon --qnum=203 \
        --filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun \
            --dpi-desync=fake \
            --dpi-desync-repeats=6 \
            --dpi-desync-fake-discord=$FAKE_DISCORD \
            --dpi-desync-fake-stun=$FAKE_STUN \
      --new \
        --filter-udp=10000-65535 \
            --dpi-desync=fake \
            --dpi-desync-repeats=8 \
            --dpi-desync-cutoff=n3 \
            --dpi-desync-fake-quic=$FAKE_QUIC_VK

    iptables -t mangle -A POSTROUTING -p udp -m multiport --dports 19294:19344,50000:50100 \
        -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
        -j NFQUEUE --queue-num 203 --queue-bypass
    iptables -t mangle -A POSTROUTING -p udp -m multiport --dports 10000:65535 \
        -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
        -j NFQUEUE --queue-num 203 --queue-bypass
    echo "  Discord UDP started (queue 203)"

    # =========================================================
    # INSTAGRAM TCP (queue 204)
    # Стратегия multidisorder — эффективна против ТСПУ для Meta
    # =========================================================
    $NFQWS --daemon --qnum=204 \
        --filter-tcp=443 --hostlist=$CONFIG_DIR/domains/instagram.txt \
            --dpi-desync=fake,multidisorder \
            --dpi-desync-split-pos=1,midsld \
            --dpi-desync-repeats=11 \
            --dpi-desync-fooling=md5sig,badseq \
            --dpi-desync-autottl=2:2-12 \
            --dpi-desync-fake-tls=$FAKE_TLS

    iptables -t mangle -A POSTROUTING -p tcp --dport 443 \
        -m connbytes --connbytes-dir=original --connbytes-mode=packets --connbytes 1:6 \
        -j NFQUEUE --queue-num 204 --queue-bypass
    echo "  Instagram TCP started (queue 204)"

    # =========================================================
    # INSTAGRAM UDP (queue 205)
    # QUIC к Meta IP подсетям (разрешено выше через ACCEPT правила)
    # =========================================================
    $NFQWS --daemon --qnum=205 \
        --filter-udp=443 --hostlist=$CONFIG_DIR/domains/instagram.txt \
            --dpi-desync=fake \
            --dpi-desync-repeats=6 \
            --dpi-desync-fake-quic=$FAKE_QUIC

    # UDP 443 уже перехватывается queue 201 (YouTube) — Instagram QUIC
    # идёт через Meta ACCEPT правила, поэтому отдельное iptables не нужно
    echo "  Instagram UDP started (queue 205)"

    echo "Zapret started. Processes:"
    pgrep -c nfqws && echo "nfqws instances running"
}

stop() {
    echo "Stopping Zapret..."
    pkill -x nfqws 2>/dev/null
    iptables -t mangle -F POSTROUTING 2>/dev/null
    cleanup_meta_rules
    echo "Zapret stopped."
}

status() {
    echo "=== Zapret processes ==="
    pgrep -a nfqws 2>/dev/null | while read pid cmd; do
        qnum=$(echo $cmd | grep -oP '(?<=--qnum=)\d+')
        case $qnum in
            200) echo "  [queue $qnum] YouTube TCP" ;;
            201) echo "  [queue $qnum] YouTube UDP" ;;
            202) echo "  [queue $qnum] Discord TCP" ;;
            203) echo "  [queue $qnum] Discord UDP" ;;
            204) echo "  [queue $qnum] Instagram TCP" ;;
            205) echo "  [queue $qnum] Instagram UDP" ;;
            *)   echo "  [queue $qnum] Unknown" ;;
        esac
    done || echo "  Not running"
    echo ""
    echo "=== NFQUEUE rules ==="
    iptables -t mangle -L POSTROUTING -n -v 2>/dev/null | grep NFQUEUE || echo "  No rules"
    echo ""
    echo "=== Meta QUIC ACCEPT (FORWARD) ==="
    iptables -L FORWARD -n 2>/dev/null | grep -E 'ACCEPT.*udp.*(31\.13|157\.240|102\.132|129\.134|185\.60|179\.60)' | wc -l
}

case "$1" in
    start) start ;;
    stop) stop ;;
    restart) stop; sleep 1; start ;;
    status) status ;;
    *) echo "Usage: $0 {start|stop|restart|status}" ;;
esac
