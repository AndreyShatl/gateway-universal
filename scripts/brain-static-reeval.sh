#!/usr/bin/env bash
# brain-static-reeval.sh — ночная проверка доменов, статически зашитых на VPS
# (не проходят через пассивный детектор, потому что VPS-путь и так работает —
# детектор реагирует только на реальный сбой соединения).
#
# Идея: не трогаем их mode/статический VPS-роутинг вообще (остаётся страховкой) —
# просто докидываем каждый домен в ту же очередь /etc/gateway/brain-queue, что и
# пассивный детектор, и existing brain-worker.sh делает всё остальное сам:
#   ZAPRET -> сущность (RETURN в nat PREROUTING ставит её ВЫШЕ статического VPS-
#             роутинга, см. brain-apply.sh do_zapret) — реально уходит с VPS
#   VPS    -> ничего не меняется, домен как был на статическом VPS, так и остаётся
#   DIRECT -> сущность (если была) снимается — не нужна
#
# Два независимых источника доменов:
#   1) youtube/discord/instagram — "featured"-сервисы zapret-services.json
#      (карточки в UI, отдельный переключатель режима)
#   2) остальной статический список xray/domains/*.txt (2026-08-01) — КРОМЕ
#      ai-services.txt. AI-сервисы (ChatGPT/Gemini/Claude/Grok и т.п.)
#      НАМЕРЕННО никогда сюда не попадают и никогда не проверяются на zapret/
#      ciadpi/zapret2 — риск детекта/бана аккаунта важнее экономии VPS-трафика,
#      это осознанное решение, не забытый TODO. См. EXCLUDED_CATEGORIES ниже.
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
PROGRESS=/etc/gateway/brain-progress.json
XRAY_DOMAINS_DIR=${XRAY_DOMAINS_DIR:-/root/gateway-universal/xray/domains}

# Категории, которые НИКОГДА не идут в переоценку — сейчас только AI. Список
# сознательно отдельной переменной (не хардкод внутри цикла), чтобы будущее
# расширение исключений было одной строкой, а не правкой логики.
EXCLUDED_CATEGORIES=(ai-services.txt)

bump_progress() {
  python3 -c "
import json, time
f='$PROGRESS'
try:
    d = json.load(open(f))
except Exception:
    d = {}
if d.get('total', 0) <= 0:
    d = {'total': 0, 'started_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
d['total'] = d.get('total', 0) + $1
json.dump(d, open(f, 'w'))
"
}

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

# Общая проверка + постановка в очередь для домена из ЛЮБОГО источника ниже —
# одна точка правды вместо дублирования тела цикла дважды.
try_enqueue() {
  local domain
  domain=$(echo "$1" | tr '[:upper:]' '[:lower:]')
  [ -n "$domain" ] || return 1
  if [ "$(python3 "$GWDB" whitelisted "$domain" 2>/dev/null)" = "1" ]; then
    return 1
  fi
  if is_entity "$domain"; then
    return 1 # уже сущность zapret — brain-nightly.sh её переоценит отдельно
  fi
  enqueue "$domain"
  return 0
}

n=0
for sid in youtube discord instagram; do
  # T-vps-pin (2026-08-16): сервис закреплён на VPS кнопкой в UI — пользователь
  # явно попросил полную независимость от ночной переоценки, не только "не
  # применять DPI" (это уже гарантирует process_domain() в brain-worker.sh),
  # но и вообще не ставить в очередь и не трогать. Раньше закреплённый домен
  # всё равно докидывался сюда каждую ночь и process_domain() мгновенно
  # возвращал его на VPS — лишняя работа без всякого смысла при закреплении.
  mode=$(jq -r --arg id "$sid" '.[] | select(.id==$id) | .mode // ""' "$SERVICES" 2>/dev/null)
  if [ "$mode" = "vps" ]; then
    log "$sid: закреплён на VPS — пропуск (не ставим в очередь)"
    continue
  fi
  domains=$(jq -r --arg id "$sid" '.[] | select(.id==$id) | .domains[]?' "$SERVICES" 2>/dev/null)
  [ -n "$domains" ] || continue
  while IFS= read -r domain; do
    [ -n "$domain" ] || continue
    try_enqueue "$domain" && n=$((n+1))
  done <<< "$domains"
done
log "готово: $n доменов поставлено в очередь (youtube/discord/instagram)"

is_excluded_category() {
  local base
  base="$(basename "$1")"
  local ex
  for ex in "${EXCLUDED_CATEGORIES[@]}"; do
    [[ "$base" == "$ex" ]] && return 0
  done
  return 1
}

m=0
if [[ -d "$XRAY_DOMAINS_DIR" ]]; then
  for f in "$XRAY_DOMAINS_DIR"/*.txt; do
    [[ -e "$f" ]] || continue
    [[ "$f" == *.bak* ]] && continue
    is_excluded_category "$f" && continue
    while IFS= read -r line; do
      line="${line%%#*}"
      line="$(echo "$line" | tr -d '[:space:]')"
      [[ -z "$line" ]] && continue
      [[ "$line" == geosite:* ]] && continue
      try_enqueue "$line" && m=$((m+1))
    done < "$f"
  done
fi

n=$((n+m))
[ "$n" -gt 0 ] && bump_progress "$n"
log "готово: $m доменов поставлено в очередь (статический xray-список, кроме: ${EXCLUDED_CATEGORIES[*]})"
