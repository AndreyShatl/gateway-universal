#!/usr/bin/env bash
# nm-dispatcher-dnscrypt-restart.sh — устанавливается в
# /etc/NetworkManager/dispatcher.d/, перезапускает dnscrypt-proxy при
# восстановлении линка на WAN-интерфейсе.
#
# Зачем: обнаружено на практике (2026-08-11) — при горячей замене LAN-кабеля
# (несколько циклов link down/up подряд) NetworkManager сам восстанавливает
# IP и маршруты за секунды, но dnscrypt-proxy — нет: его исходящее
# DoH/DNSCrypt-соединение к апстримам зависает намертво (AdGuardHome начинает
# получать i/o timeout на 127.0.0.1:5353 — чисто локальный вызов, к кабелю
# отношения не имеющий) и НЕ восстанавливается сам, требуя ребута всего
# шлюза. Простой systemctl restart dnscrypt-proxy при событии "up" чинит это
# за секунду, без полного ребута.
#
# NetworkManager вызывает dispatcher-скрипты с аргументами: $1=интерфейс,
# $2=действие (up|down|dhcp4-change|...). Реагируем только на "up" нашего
# WAN-интерфейса (полная успешная активация — NM уже сам фильтрует частые
# промежуточные carrier-моргания, "up" приходит только после реального
# восстановления соединения).

IFACE="$1"
ACTION="$2"
WAN_IFACE="${GATEWAY_WAN_IFACE:-enp2s0}"

[ "$IFACE" = "$WAN_IFACE" ] || exit 0
[ "$ACTION" = "up" ] || exit 0

logger -t nm-dispatcher-dnscrypt "интерфейс $IFACE поднят — перезапускаю dnscrypt-proxy (анти-залипание после скачков линка)"
systemctl restart dnscrypt-proxy.service 2>&1 | logger -t nm-dispatcher-dnscrypt
