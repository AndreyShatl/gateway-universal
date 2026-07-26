#!/usr/bin/env bash
# Game Mode — бланкет-ACCEPT для эфемерных портов игровых серверов (T-gamemode).
# Идея: игровые сервера сидят на случайных высоких портах, которые не имеют
# отношения к 80/443-ориентированному zapret/brain — вместо детекта/desync
# для них просто отключаем обработку целиком (без VPS, без nfqws), чтобы не
# добавлять задержку и не давать DPI лишний повод резать соединение.
#
# Диапазон порта для UDP ограничен СВЕРХУ 49999, чтобы не перехватывать
# голосовой Discord (UDP 50000-65535, iptables/discord-tproxy.sh) — тот
# сознательно закреплён на VPS-туннеле (напрямую не пробивается), Game Mode
# его трогать не должен. TCP-диапазон Discord не занимает — там ограничений нет.
#
# Режимы: off | tcp | udp | both. Состояние — /etc/gateway/game-mode.conf
# (одно слово), чтобы UI и restore на боевую читали то же самое.
set -e

LAN="192.168.0.0/16"
TCP_RANGE="1024:65535"
UDP_RANGE="1024:49999"
STATE_FILE="/etc/gateway/game-mode.conf"

usage() { echo "usage: $0 {off|tcp|udp|both}" >&2; exit 2; }

remove_rules() {
    iptables -t mangle -D PREROUTING -s "$LAN" -p tcp --dport "$TCP_RANGE" -j ACCEPT 2>/dev/null || true
    iptables -D FORWARD -s "$LAN" -p tcp --dport "$TCP_RANGE" -j ACCEPT 2>/dev/null || true
    iptables -t mangle -D PREROUTING -s "$LAN" -p udp --dport "$UDP_RANGE" -j ACCEPT 2>/dev/null || true
    iptables -D FORWARD -s "$LAN" -p udp --dport "$UDP_RANGE" -j ACCEPT 2>/dev/null || true
}

add_tcp() {
    iptables -t mangle -C PREROUTING -s "$LAN" -p tcp --dport "$TCP_RANGE" -j ACCEPT 2>/dev/null || \
        iptables -t mangle -I PREROUTING 1 -s "$LAN" -p tcp --dport "$TCP_RANGE" -j ACCEPT
    iptables -C FORWARD -s "$LAN" -p tcp --dport "$TCP_RANGE" -j ACCEPT 2>/dev/null || \
        iptables -I FORWARD 1 -s "$LAN" -p tcp --dport "$TCP_RANGE" -j ACCEPT
}

add_udp() {
    iptables -t mangle -C PREROUTING -s "$LAN" -p udp --dport "$UDP_RANGE" -j ACCEPT 2>/dev/null || \
        iptables -t mangle -I PREROUTING 1 -s "$LAN" -p udp --dport "$UDP_RANGE" -j ACCEPT
    iptables -C FORWARD -s "$LAN" -p udp --dport "$UDP_RANGE" -j ACCEPT 2>/dev/null || \
        iptables -I FORWARD 1 -s "$LAN" -p udp --dport "$UDP_RANGE" -j ACCEPT
}

mode="${1:-}"
case "$mode" in
    off)  remove_rules ;;
    tcp)  remove_rules; add_tcp ;;
    udp)  remove_rules; add_udp ;;
    both) remove_rules; add_tcp; add_udp ;;
    *)    usage ;;
esac

mkdir -p "$(dirname "$STATE_FILE")"
echo "$mode" > "$STATE_FILE"
echo "game-mode: $mode применён"
