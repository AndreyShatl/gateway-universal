#!/usr/bin/env bash
# =====================================================================
# apply-fix-gateway.sh — (пере)установить fix-gateway.service с заданным
# IP роутера. Общий код для install.sh и веб-UI (T11): единственное место,
# где рендерится и применяется fix-gateway.
#
# Использование:
#   apply-fix-gateway.sh <ROUTER_IP>
# =====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
TEMPLATE="$SCRIPT_DIR/fix-gateway.service"

ROUTER_IP="${1:-}"
[[ -n "$ROUTER_IP" ]] || { echo "apply-fix-gateway: нужен ROUTER_IP" >&2; exit 2; }
[[ "$ROUTER_IP" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]] \
    || { echo "apply-fix-gateway: невалидный IPv4: $ROUTER_IP" >&2; exit 2; }
[[ -f "$TEMPLATE" ]] || { echo "apply-fix-gateway: нет шаблона $TEMPLATE" >&2; exit 2; }

sed "s|__ROUTER_IP__|$ROUTER_IP|g" "$TEMPLATE" > /etc/systemd/system/fix-gateway.service
systemctl daemon-reload
systemctl enable fix-gateway.service >/dev/null 2>&1 || true
systemctl restart fix-gateway.service
echo "fix-gateway.service applied (router=$ROUTER_IP)"
