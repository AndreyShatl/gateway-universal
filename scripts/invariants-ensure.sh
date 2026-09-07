#!/usr/bin/env bash
# invariants-ensure.sh (2026-09-07, после инцидента "видео зависли после
# ребута") — периодический самовосстановитель сетевых инвариантов шлюза.
# Правила ниже критичны для работы LAN, но исторически ставились "один раз"
# (install.sh / rules.v4) и тихо терялись при пересборках цепочек. Этот
# скрипт — тот же паттерн, что brain-refresh-ips для IP: каждые N минут
# идемпотентно проверить и вернуть пропавшее. Молчит, когда всё на месте
# (пишет в лог только когда реально починил).
#
# Инварианты:
#   1. Meta QUIC ACCEPT (полный ASN 32934, 10 диапазонов) в FORWARD и
#      mangle PREROUTING — без них телефонный Instagram зависает
#      (Meta-приложения плохо откатываются с QUIC на TCP).
#   2. Глобальный DROP UDP/443 в конце FORWARD — без него QUIC уходит
#      напрямую в ТСПУ и умирает молча (живой инцидент 2026-09-07).
set -uo pipefail

LAN=192.168.0.0/16
LOG=/var/log/gateway-brain.log
META_IPS="31.13.24.0/21 31.13.64.0/18 69.171.224.0/19 102.132.96.0/20 129.134.0.0/17 157.240.0.0/16 173.252.64.0/18 179.60.192.0/22 185.60.216.0/22 204.15.20.0/22"

log() { echo "$(date '+%F %T') [invariants] $*" >> "$LOG"; }
fixed=0

for cidr in $META_IPS; do
  if ! iptables -C FORWARD -s $LAN -d "$cidr" -p udp --dport 443 -j ACCEPT 2>/dev/null; then
    # вставляем в голову (до глобального DROP в хвосте)
    iptables -I FORWARD 1 -s $LAN -d "$cidr" -p udp --dport 443 -j ACCEPT
    fixed=$((fixed+1)); log "вернул Meta QUIC ACCEPT (FORWARD): $cidr"
  fi
  if ! iptables -t mangle -C PREROUTING -s $LAN -d "$cidr" -p udp --dport 443 -j ACCEPT 2>/dev/null; then
    iptables -t mangle -I PREROUTING 1 -s $LAN -d "$cidr" -p udp --dport 443 -j ACCEPT
    fixed=$((fixed+1)); log "вернул Meta QUIC ACCEPT (mangle): $cidr"
  fi
done

if ! iptables -C FORWARD -s $LAN -p udp --dport 443 -j DROP 2>/dev/null; then
  iptables -A FORWARD -s $LAN -p udp --dport 443 -j DROP
  fixed=$((fixed+1)); log "вернул глобальный QUIC DROP (UDP/443)"
fi

# НЕ пересохраняем rules.v4 на каждом прогоне (чистые дубли в живых правилах
# не копим: -C перед -I). Персистентный файл обновит ближайший
# netfilter-persistent save / install.sh; при ребуте юниты сами ensure-ят.

[ "$fixed" -gt 0 ] && log "итог: восстановлено правил=$fixed" || true
exit 0
