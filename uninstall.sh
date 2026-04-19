#!/usr/bin/env bash
# =====================================================================
# gateway-universal — удаление
# Останавливает и удаляет xray, zapret, iptables правила, systemd units.
# Оставляет бинарники/конфиги если запустить без --purge.
# =====================================================================

set -euo pipefail

PURGE=no
YES=no
INSTALL_PREFIX="/opt"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --purge)            PURGE=yes; shift;;
        --yes|-y)           YES=yes; shift;;
        --install-prefix)   INSTALL_PREFIX="$2"; shift 2;;
        -h|--help)
            echo "Usage: $0 [--purge] [--yes]"
            echo "  --purge   также удалить /opt/xray и /opt/zapret"
            exit 0;;
        *) echo "Unknown: $1"; exit 1;;
    esac
done

[[ $EUID -eq 0 ]] || { echo "Run as root"; exit 1; }

if [[ $YES != yes ]]; then
    echo "Это остановит xray/zapret, уберёт systemd units, сбросит iptables."
    [[ $PURGE == yes ]] && echo "И удалит ${INSTALL_PREFIX}/xray + ${INSTALL_PREFIX}/zapret"
    read -r -p "Продолжить? [y/N] " yn
    [[ "${yn,,}" =~ ^(y|yes)$ ]] || { echo "Aborted"; exit 0; }
fi

echo "==> Stopping services…"
systemctl stop   xray.service   2>/dev/null || true
systemctl stop   zapret.service 2>/dev/null || true
systemctl disable xray.service   2>/dev/null || true
systemctl disable zapret.service 2>/dev/null || true
rm -f /etc/systemd/system/xray.service /etc/systemd/system/zapret.service
systemctl daemon-reload

echo "==> Killing leftover nfqws/xray…"
killall -q nfqws 2>/dev/null || true
killall -q xray  2>/dev/null || true

echo "==> Flushing iptables…"
iptables -t mangle -F POSTROUTING 2>/dev/null || true
iptables -t mangle -F PREROUTING  2>/dev/null || true
iptables -t nat    -F PREROUTING  2>/dev/null || true
iptables -t nat    -F POSTROUTING 2>/dev/null || true
iptables -F FORWARD 2>/dev/null || true

# Сохраним чистое состояние
if command -v iptables-save >/dev/null; then
    mkdir -p /etc/iptables
    iptables-save > /etc/iptables/rules.v4
fi

if [[ $PURGE == yes ]]; then
    echo "==> Purging binaries…"
    rm -rf "${INSTALL_PREFIX}/xray" "${INSTALL_PREFIX}/zapret" "${INSTALL_PREFIX}/zapret-config"
    rm -f /etc/sysctl.d/99-gateway.conf
fi

echo "✓ Uninstalled."
