#!/usr/bin/env bash
# brain-idle-stop.sh — T54: остановить демоны сущностей, простаивающих
# >IDLE_STOP_HOURS (default 24, по last_active из brain-activity.sh, T53).
# ipset и iptables-правила НЕ трогаем (ТЗ 8.4) — только systemctl stop юнита,
# трафик на неактивный demon идёт через --queue-bypass не десинхронизированным.
#
# Реактивация: следующий ночной проход brain-nightly.sh пере-solve'ит домен и
# start_daemon его поднимет заново (nightly ничего не знает про idle-статус —
# осознанное упрощение v1, см. TASKS T54). Поэтому таймер этого скрипта стоит
# ЗАМЕТНО позже 04:00 (nightly), напр. полдень — иначе стоп тут же отменится
# тем же ночным проходом.
set -uo pipefail

STATE=/etc/gateway/brain-services.json
LOG=/var/log/gateway-brain.log
IDLE_STOP_HOURS=${IDLE_STOP_HOURS:-24}

[ -f "$STATE" ] || exit 0

san() { echo "$1" | tr -c 'a-z0-9' '_' | sed 's/_*$//'; }

idle_domains=$(python3 -c "
import json
from datetime import datetime, timezone

data = json.load(open('$STATE'))
now = datetime.now(timezone.utc)
for e in data:
    la = e.get('last_active')
    if not la:
        continue
    try:
        t = datetime.strptime(la, '%Y-%m-%dT%H:%M:%SZ').replace(tzinfo=timezone.utc)
    except ValueError:
        continue
    if (now - t).total_seconds() / 3600 >= $IDLE_STOP_HOURS:
        print(e['domain'])
")

n=0
while IFS= read -r domain; do
  [ -n "$domain" ] || continue
  unit="brain-nfqws-$(san "$domain")"
  if systemctl is-active --quiet "$unit"; then
    systemctl stop "$unit"
    echo "$(date '+%F %T') 💤 $domain — демон остановлен (idle >=${IDLE_STOP_HOURS}ч)" >> "$LOG"
    n=$((n+1))
  fi
done <<< "$idle_domains"

echo "$(date '+%F %T') 💤 idle-стоп: остановлено $n демонов" >> "$LOG"
