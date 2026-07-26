#!/usr/bin/env bash
# brain-static-reeval.sh — ночная проверка доменов youtube/discord/instagram
# (статические "featured"-сервисы zapret-services.json, ВСЕГДА на VPS/geosite-
# роутинге по умолчанию, не проходят через пассивный детектор потому что VPS-путь
# и так работает — детектор реагирует только на реальный сбой соединения).
#
# Идея: не трогаем их mode/статический VPS-роутинг вообще (остаётся страховкой) —
# просто докидываем каждый домен в ту же очередь /etc/gateway/brain-queue, что и
# пассивный детектор, и existing brain-worker.sh делает всё остальное сам:
#   ZAPRET -> сущность (RETURN в nat PREROUTING ставит её ВЫШЕ статического VPS-
#             роутинга, см. brain-apply.sh do_zapret) — реально уходит с VPS
#   VPS    -> ничего не меняется, домен как был на статическом VPS, так и остаётся
#   DIRECT -> сущность (если была) снимается — не нужна
#
# Дедуп: те же проверки, что делает detector/main.go enqueueBrain (isBrainEntity/
# inBrainQueue) — под тем же flock, чтобы не гоняться с пассивным детектором.
set -uo pipefail

SERVICES=/etc/gateway/zapret-services.json
BRAIN_SERVICES=/etc/gateway/brain-services.json
QUEUE=/etc/gateway/brain-queue
LOCK=/etc/gateway/brain-queue.lock
GWDB=${GWDB:-/root/gateway-universal/scripts/gwdb.py}
LOG=/var/log/gateway-brain.log

log() { echo "$(date '+%F %T') [static-reeval] $*" >> "$LOG"; }

is_entity() {
  python3 -c "
import json,sys
try:
    d = json.load(open('$BRAIN_SERVICES'))
except Exception:
    sys.exit(1)
sys.exit(0 if any(x.get('domain','').lower()=='$1'.lower() for x in d) else 1)
" 2>/dev/null
}

in_queue() { grep -qi "^$1"$'\t' "$QUEUE" 2>/dev/null || grep -qxi "$1" "$QUEUE" 2>/dev/null; }

enqueue() {
  local domain="$1"
  exec 9>"$LOCK"; flock 9
  if ! in_queue "$domain"; then
    echo -e "${domain}\treeval" >> "$QUEUE"
    log "→ $domain в очередь (static-reeval)"
  fi
  flock -u 9
}

n=0
for sid in youtube discord instagram; do
  domains=$(jq -r --arg id "$sid" '.[] | select(.id==$id) | .domains[]?' "$SERVICES" 2>/dev/null)
  [ -n "$domains" ] || continue
  while IFS= read -r domain; do
    [ -n "$domain" ] || continue
    domain=$(echo "$domain" | tr '[:upper:]' '[:lower:]')
    if [ "$(python3 "$GWDB" whitelisted "$domain" 2>/dev/null)" = "1" ]; then
      continue
    fi
    if is_entity "$domain"; then
      continue # уже сущность zapret — brain-nightly.sh её переоценит отдельно
    fi
    enqueue "$domain"
    n=$((n+1))
  done <<< "$domains"
done

log "готово: $n доменов поставлено в очередь (youtube/discord/instagram)"
