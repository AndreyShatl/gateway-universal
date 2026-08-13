#!/usr/bin/env bash
# harden-internal-ports.sh (T-security-audit, 2026-08-13) — LAN+loopback-only
# для портов, которые никогда не должны быть видны из WAN, но раньше не имели
# ни одного iptables-правила (держались только на том, что домашний роутер
# их не форвардит — не defense-in-depth, а надежда):
#   - 53 tcp+udp   — AdGuardHome DNS-резолвер (открытый резолвер — классический
#                    вектор DNS-амплификации, если когда-то станет виден извне)
#   - 1080,8080,12345,12347 — внутренние xray-листенеры (dokodemo/socks для
#                    TPROXY-перенаправления локального трафика, не для внешних
#                    подключений в принципе)
#   - 15000-15500  — весь пул портов ciadpi (CPORT_BASE..+CPORT_POOL из
#                    brain-apply.sh) — транзитные systemd-run юниты по одному
#                    на группу, нет статичного unit-файла на который вешать
#                    ExecStartPre, поэтому один общий рейндж-фикс здесь
# Идемпотентно (-C проверка перед -I), безопасно перезапускать сколько угодно.
set -euo pipefail

LAN=192.168.0.0/16

add_rule() { # <proto> <port-spec>
  local proto=$1 port=$2
  iptables -C INPUT -p "$proto" --dport "$port" -s 127.0.0.1 -j ACCEPT 2>/dev/null || \
    iptables -I INPUT -p "$proto" --dport "$port" -s 127.0.0.1 -j ACCEPT
  iptables -C INPUT -p "$proto" --dport "$port" -s "$LAN" -j ACCEPT 2>/dev/null || \
    iptables -I INPUT -p "$proto" --dport "$port" -s "$LAN" -j ACCEPT
  iptables -C INPUT -p "$proto" --dport "$port" -j DROP 2>/dev/null || \
    iptables -A INPUT -p "$proto" --dport "$port" -j DROP
}

add_rule tcp 53
add_rule udp 53
add_rule tcp 1080
add_rule tcp 8080
add_rule tcp 12345
add_rule tcp 12347
add_rule tcp 15000:15500

echo "ok: внутренние порты (DNS, xray-листенеры, пул ciadpi) закрыты от WAN"
