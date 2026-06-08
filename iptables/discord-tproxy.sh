#!/usr/bin/env bash
# Discord-голос (UDP 50000-65535) через xray-туннель.
# TPROXY -> xray dokodemo :12346 (tproxy-udp) -> outbound proxy-mux (gRPC).
# Нужно, т.к. РФ DPI глушит UDP голосовых серверов Discord на прямом пути,
# а через тоннель (gRPC :2083) — стабильно. UDP идёт ТОЛЬКО через тоннель
# (обычный TCP-роутинг это не покрывает — там REDIRECT только 80/443).
set -e
VPS_IP="__VPS_ADDR__"
TPROXY_PORT="12346"
LAN="192.168.0.0/16"

# policy routing для TPROXY (метка 1 -> локальная таблица 100 -> lo)
ip rule show | grep -q "fwmark 0x1 lookup 100" || ip rule add fwmark 1 table 100
ip route show table 100 2>/dev/null | grep -q "local default" || ip route add local default dev lo table 100

# цепочка: исключаем локалку и сам VPS, остальной UDP -> TPROXY
iptables -t mangle -N DISCORD_TPROXY 2>/dev/null || iptables -t mangle -F DISCORD_TPROXY
iptables -t mangle -A DISCORD_TPROXY -d 192.168.0.0/16 -j RETURN
iptables -t mangle -A DISCORD_TPROXY -d 10.0.0.0/8 -j RETURN
iptables -t mangle -A DISCORD_TPROXY -d 172.16.0.0/12 -j RETURN
iptables -t mangle -A DISCORD_TPROXY -d "${VPS_IP}" -j RETURN
iptables -t mangle -A DISCORD_TPROXY -p udp -j TPROXY --on-port "${TPROXY_PORT}" --tproxy-mark 1

# хук в PREROUTING ПЕРВЫМ (перед zapret-NFQUEUE), идемпотентно
iptables -t mangle -C PREROUTING -s "${LAN}" -p udp --dport 50000:65535 -j DISCORD_TPROXY 2>/dev/null || \
  iptables -t mangle -I PREROUTING 1 -s "${LAN}" -p udp --dport 50000:65535 -j DISCORD_TPROXY

echo "discord-tproxy применён (VPS=${VPS_IP}, TPROXY :${TPROXY_PORT})"
