#!/usr/bin/env bash
# Discord-голос (UDP $PORT_RANGE) через xray-туннель.
# TPROXY -> xray dokodemo :12346 (tproxy-udp) -> outbound proxy-mux-voice (gRPC).
# Нужно, т.к. РФ DPI глушит UDP голосовых серверов Discord на прямом пути,
# а через тоннель (gRPC :2083) — стабильно. UDP идёт ТОЛЬКО через тоннель
# (обычный TCP-роутинг это не покрывает — там REDIRECT только 80/443).
#
# T-discord-port-range (2026-08-16): диапазон был 50000-65535 (офиц.
# документация Discord) — живой звонок показал, что реальный порт медиа-
# сервера сейчас 19328/19338, СОВСЕМ вне этого диапазона (голос шёл мимо
# TPROXY, напрямую, под троттлинг ISP — 5000мс задержки при 0% потерь,
# классический паттерн шейпинга). Расширено до 10000-65535. Порт 443
# явно исключён (--dport в правиле ниже это разрешает) — глобальный
# UDP/443-DROP (кроме Meta, см. zapret/zapret.sh) не должен конфликтовать
# с этим TPROXY. Побочный эффект: другой UDP-трафик в 10000-65535
# (например, торренты) теперь тоже уйдёт в VPS-туннель — компромисс
# принят сознательно ради голоса Discord.
set -e
VPS_IP="__VPS_ADDR__"
TPROXY_PORT="12346"
LAN="192.168.0.0/16"
PORT_RANGE="10000:65535"

# policy routing для TPROXY (метка 1 -> локальная таблица 100 -> lo)
ip rule show | grep -q "fwmark 0x1 lookup 100" || ip rule add fwmark 1 table 100
ip route show table 100 2>/dev/null | grep -q "local default" || ip route add local default dev lo table 100

# цепочка: исключаем локалку, сам VPS и UDP/443 (QUIC — своя политика), остальной UDP -> TPROXY
iptables -t mangle -N DISCORD_TPROXY 2>/dev/null || iptables -t mangle -F DISCORD_TPROXY
iptables -t mangle -A DISCORD_TPROXY -d 192.168.0.0/16 -j RETURN
iptables -t mangle -A DISCORD_TPROXY -d 10.0.0.0/8 -j RETURN
iptables -t mangle -A DISCORD_TPROXY -d 172.16.0.0/12 -j RETURN
iptables -t mangle -A DISCORD_TPROXY -d "${VPS_IP}" -j RETURN
iptables -t mangle -A DISCORD_TPROXY -p udp --dport 443 -j RETURN
iptables -t mangle -A DISCORD_TPROXY -p udp -j TPROXY --on-port "${TPROXY_PORT}" --tproxy-mark 1

# хук в PREROUTING ПЕРВЫМ (перед zapret-NFQUEUE), идемпотентно
iptables -t mangle -C PREROUTING -s "${LAN}" -p udp --dport "${PORT_RANGE}" -j DISCORD_TPROXY 2>/dev/null || \
  iptables -t mangle -I PREROUTING 1 -s "${LAN}" -p udp --dport "${PORT_RANGE}" -j DISCORD_TPROXY

echo "discord-tproxy применён (VPS=${VPS_IP}, TPROXY :${TPROXY_PORT})"
