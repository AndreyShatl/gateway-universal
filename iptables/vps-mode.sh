#!/usr/bin/env bash
# vps-mode.sh — переключатель "VPS+zapret" / "только zapret" (без VPS-туннеля).
# off: снимает редирект LAN TCP 80/443 -> xray (:12345) и авто-обход -> xray
# (:12347) — трафик, который zapret не десинхронизировал сам (per-domain
# brain-сущности через RETURN carve-out, T44-46 — их это не касается, они
# работают раньше в цепочке правил), идёт напрямую вместо VPS-туннеля.
# on: возвращает оба редиректа (как при обычной установке с VPS).
#
# Состояние — /etc/gateway/vps-mode.conf ("on"/"off"), читает и пишет тот же
# файл, что использует has_vps() в scripts/brain-apply.sh (мозг перестаёт
# предлагать VPS-фолбэк, пока режим off — иначе создал бы автообход в никуда).
set -uo pipefail

LAN="192.168.0.0/16"
STATE_FILE="/etc/gateway/vps-mode.conf"

usage() { echo "usage: $0 {on|off}" >&2; exit 2; }

main_redirect() {
  local op=$1
  iptables -t nat -"$op" PREROUTING -s "$LAN" -p tcp -m multiport --dports 80,443 -j REDIRECT --to-ports 12345 2>/dev/null
}
autoroute_redirect() {
  local op=$1
  iptables -t nat -"$op" PREROUTING -s "$LAN" -p tcp -m set --match-set gw_autoroute dst -j REDIRECT --to-ports 12347 2>/dev/null
}

mode="${1:-}"
case "$mode" in
  on)
    main_redirect C || main_redirect I
    autoroute_redirect C || autoroute_redirect I
    ;;
  off)
    main_redirect D || true
    autoroute_redirect D || true
    ;;
  *) usage ;;
esac

mkdir -p "$(dirname "$STATE_FILE")"
echo "$mode" > "$STATE_FILE"
echo "vps-mode: $mode применён"
