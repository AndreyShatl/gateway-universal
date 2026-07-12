#!/usr/bin/env bash
# =====================================================================
# setup-dnscrypt.sh — шифрованный DNS на шлюзе (dnscrypt-proxy, DoH).
#
# Зачем: провайдер/ТСПУ видит и подделывает открытый DNS (:53) — блокируемые
# домены резолвятся в фейковые IP, и zapret/VPS не помогают (соединение уходит
# не туда), а сами запросы «палят» активность. Этот скрипт:
#   1) ставит dnscrypt-proxy, апстрим — DoH (Cloudflare + Quad9), DNSSEC,
#   2) слушает 127.0.0.1:53 и <LAN-IP шлюза>:53,
#   3) форсирует ВЕСЬ DNS (клиенты + сам шлюз) в него (iptables REDIRECT),
#   4) resolv.conf -> 127.0.0.1 и делает его immutable (dhclient не перезапишет).
# Итог: анти-поизонинг + без утечек открытого DNS.
#
# Переменные: LAN (по умолч. 192.168.0.0/16), GW_IP (авто), DNSCRYPT_SERVERS.
# =====================================================================
set -euo pipefail
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"

LAN="${LAN:-192.168.0.0/16}"
GW_IP="${GW_IP:-$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')}"
DNSCRYPT_SERVERS="${DNSCRYPT_SERVERS:-['cloudflare', 'quad9-doh-ip4-port443-nofilter-pri']}"

echo "== dnscrypt-proxy: установка =="
export DEBIAN_FRONTEND=noninteractive
apt-get install -y dnscrypt-proxy

TOML=/etc/dnscrypt-proxy/dnscrypt-proxy.toml
sed -i "s|^listen_addresses = .*|listen_addresses = []|" "$TOML"          # адреса задаёт socket
sed -i "s|^server_names = .*|server_names = ${DNSCRYPT_SERVERS}|" "$TOML"

echo "== socket -> 127.0.0.1:53 + ${GW_IP}:53 =="
mkdir -p /etc/systemd/system/dnscrypt-proxy.socket.d
cat > /etc/systemd/system/dnscrypt-proxy.socket.d/override.conf <<OVR
[Socket]
ListenStream=
ListenDatagram=
ListenStream=127.0.0.1:53
ListenDatagram=127.0.0.1:53
ListenStream=${GW_IP}:53
ListenDatagram=${GW_IP}:53
OVR
systemctl daemon-reload
systemctl enable dnscrypt-proxy.socket dnscrypt-proxy.service >/dev/null 2>&1 || true
systemctl restart dnscrypt-proxy.socket
systemctl restart dnscrypt-proxy.service

echo "== редирект всего :53 в dnscrypt (persistent) =="
install -m755 "$SCRIPT_DIR/gateway-dns-redirect.sh" /opt/gateway-dns-redirect.sh
sed "s|__LAN__|$LAN|g" "$SCRIPT_DIR/../systemd/gateway-dns.service" > /etc/systemd/system/gateway-dns.service
systemctl daemon-reload
systemctl enable --now gateway-dns.service

echo "== resolv.conf -> 127.0.0.1 (immutable) =="
chattr -i /etc/resolv.conf 2>/dev/null || true
printf "nameserver 127.0.0.1\noptions edns0\n" > /etc/resolv.conf
chattr +i /etc/resolv.conf 2>/dev/null || true

echo "== готово =="
echo "  live-серверы: $(journalctl -u dnscrypt-proxy -n 30 --no-pager 2>/dev/null | grep -o 'live servers: [0-9]*' | tail -1)"
getent hosts cloudflare.com >/dev/null && echo "  резолв работает ✓"
