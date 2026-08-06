#!/usr/bin/env bash
# zapret2-auto-update.sh — обновление движка zapret2 (nfqws2, T-zapret2) из
# апстрима. Полный аналог ciadpi-auto-update.sh: zapret2 тоже не имеет ОДНОГО
# системного демона — у каждой группы свой transient-юнит (brain-nfqws2-<gid>,
# см. brain-apply.sh), поэтому вместо "systemctl restart" — остановить активные
# группы, пересобрать, `brain-apply.sh restore-zapret2` поднимет их заново.
# Если сборка не удалась — откат на предыдущий commit, НЕ трогая zapret1/ciadpi.
set -uo pipefail

Z2DIR=${Z2DIR:-/opt/zapret2}
BRAIN_APPLY=${BRAIN_APPLY:-/opt/gateway-brain/brain-apply.sh}
LOG=${LOG:-/var/log/gateway-zapret2-update.log}
# Makefile-дефолт LUA_VER=5.5 не существует на шлюзе (стоят только liblua5.1-dev
# и liblua5.4-dev) — без явного override сборка падает "could not find lua lib name".
export LUA_VER=5.4

exec >> "$LOG" 2>&1
echo "=== $(date '+%F %T') автообновление zapret2 ==="

cd "$Z2DIR" || { echo "нет $Z2DIR"; exit 1; }

before=$(git rev-parse --short HEAD)
git fetch --depth 1 origin || { echo "git fetch не удался"; exit 1; }
after=$(git rev-parse --short FETCH_HEAD)

if [ "$before" = "$after" ]; then
  echo "уже актуально ($before) — пропуск"
  exit 0
fi

# остановить активные zapret2-группы ДО пересборки — иначе бинарник занят (ETXTBSY)
active_units=$(systemctl list-units --plain --no-legend 'brain-nfqws2-*' 2>/dev/null | awk '{print $1}')
for u in $active_units; do systemctl stop "$u" 2>/dev/null; done

git reset --hard FETCH_HEAD || { echo "git reset не удался"; exit 1; }
if ! make; then
  echo "сборка не удалась, откатываю на $before и пересобираю"
  git reset --hard "$before"
  make
  [ -n "$active_units" ] && bash "$BRAIN_APPLY" restore-zapret2
  exit 1
fi

bash "$BRAIN_APPLY" restore-zapret2

echo "готово: $before -> $after (групп перезапущено: $(echo "$active_units" | grep -c .))"
# Mission Timeline (T-shattl-gwui-feedback, 2026-08-06) — см. zapret-auto-update.sh.
python3 -c "
import json, datetime
line = json.dumps({'at': datetime.datetime.utcnow().isoformat()+'Z', 'kind': 'engine.updated', 'message': 'zapret2: $before -> $after'})
open('/etc/gateway/timeline.jsonl', 'a').write(line + '\n')
" 2>/dev/null || true
