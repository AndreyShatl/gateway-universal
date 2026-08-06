#!/usr/bin/env bash
# ciadpi-auto-update.sh — T-ciadpi: еженедельное автообновление движка ciadpi
# (hufrea/byedpi) из апстрима. Аналог zapret-auto-update.sh, но ciadpi не имеет
# ОДНОГО системного демона — у каждой группы свой transient-юнит
# (brain-ciadpi-<group_id>, см. brain-apply.sh) — поэтому вместо
# "systemctl restart" тут: остановить все активные группы, пересобрать бинарник,
# `brain-apply.sh restore-ciadpi` поднимет их заново (пересоздаёт ipset+iptables+
# демон из /etc/gateway/brain-services-ciadpi.json, включая auto-chain) — НЕ трогая
# zapret-группы (в отличие от общего "restore").
set -uo pipefail

CIADPI_DIR=${CIADPI_DIR:-/opt/byedpi}
BRAIN_APPLY=${BRAIN_APPLY:-/opt/gateway-brain/brain-apply.sh}
LOG=${LOG:-/var/log/gateway-ciadpi-update.log}

exec >> "$LOG" 2>&1
echo "=== $(date '+%F %T') автообновление ciadpi ==="

cd "$CIADPI_DIR" || { echo "нет $CIADPI_DIR"; exit 1; }

before=$(git rev-parse --short HEAD)
git fetch --depth 1 origin || { echo "git fetch не удался"; exit 1; }
after=$(git rev-parse --short FETCH_HEAD)

if [ "$before" = "$after" ]; then
  echo "уже актуально ($before) — пропуск"
  exit 0
fi

# остановить активные ciadpi-группы ДО пересборки — иначе бинарник занят (ETXTBSY)
active_units=$(systemctl list-units --plain --no-legend 'brain-ciadpi-*' 2>/dev/null | awk '{print $1}')
for u in $active_units; do systemctl stop "$u" 2>/dev/null; done

git reset --hard FETCH_HEAD || { echo "git reset не удался"; exit 1; }
if ! make; then
  echo "сборка не удалась, откатываю на $before и пересобираю"
  git reset --hard "$before"
  make
  [ -n "$active_units" ] && bash "$BRAIN_APPLY" restore-ciadpi
  exit 1
fi

bash "$BRAIN_APPLY" restore-ciadpi

echo "готово: $before -> $after (групп перезапущено: $(echo "$active_units" | grep -c .))"
# Mission Timeline (T-shattl-gwui-feedback, 2026-08-06) — см. zapret-auto-update.sh.
python3 -c "
import json, datetime
line = json.dumps({'at': datetime.datetime.utcnow().isoformat()+'Z', 'kind': 'engine.updated', 'message': 'ciadpi: $before -> $after'})
open('/etc/gateway/timeline.jsonl', 'a').write(line + '\n')
" 2>/dev/null || true
