#!/bin/bash
# Форсируем весь DNS в локальный dnscrypt-proxy (127.0.0.1:53):
#   - трафик клиентов LAN (PREROUTING),
#   - собственный трафик шлюза (OUTPUT, кроме уже локального).
# upstream у dnscrypt — DoH поверх :443, поэтому петли на :53 нет.
# Идемпотентно (проверяет -C перед -A). Вызывается gateway-dns.service при загрузке.
LAN="${1:-192.168.0.0/16}"
add() { iptables -t nat -C "$@" 2>/dev/null || iptables -t nat -A "$@"; }
for p in udp tcp; do
    add PREROUTING -s "$LAN" -p "$p" --dport 53 -j REDIRECT --to-ports 53
    add OUTPUT -p "$p" --dport 53 ! -d 127.0.0.1 -j REDIRECT --to-ports 53
done
