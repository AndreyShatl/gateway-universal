#!/usr/bin/env bash
# brain-domain-actualize.sh — актуализация СПИСКОВ доменов (не стратегии) из
# внешнего источника — MetaCubeX/meta-rules-dat (community-зеркало v2fly
# geosite, живой проект, обновляется независимо от нас) для:
#   - youtube/discord/instagram: добавляет новые домены в zapret-services.json
#     (тот же файл, что и карточки в UI) — далее их стратегию (direct/VPS)
#     пересчитывает brain-static-reeval.sh, который запускается следующим по
#     расписанию и читает домены как раз из zapret-services.json
#   - ai-services: добавляет новые домены в xray/domains/ai-services.txt и
#     сразу перерендеривает конфиг xray (--test встроен в render-config.sh,
#     без валидного JSON замены не будет) — стратегия здесь не пересчитывается
#     вообще, ai-services всегда только VPS (гарантирует gwdb whitelist)
#
# Только ДОБАВЛЯЕТ домены, никогда не удаляет — ручные записи не из geosite
# (например explicit CDN-хосты) остаются нетронутыми.
set -uo pipefail

BASE_URL="https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite"
REPO_DIR=${REPO_DIR:-/root/gateway-universal}
SERVICES=/etc/gateway/zapret-services.json
AI_FILE=${AI_FILE:-"$REPO_DIR/xray/domains/ai-services.txt"}
LOG=/var/log/gateway-brain.log
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

log() { echo "$(date '+%F %T') [domain-actualize] $*" >> "$LOG"; }

fetch() {
  # $1 = имя geosite-списка на MetaCubeX. "+.x" -> "x" (суффиксный маппинг у
  # нас и так суффиксный по умолчанию для build-domains.sh), фильтр по IP/CIDR
  # (в geosite-списках изредка попадаются IP-диапазоны, не домены).
  curl -fsS --max-time 20 "$BASE_URL/$1.list" 2>/dev/null \
    | sed 's/^+\.//' \
    | grep -v '^$' \
    | grep -vE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' \
    | tr '[:upper:]' '[:lower:]' \
    | sort -u
}

# --- youtube/discord/instagram: добавляем в zapret-services.json ---
declare -A SVC_SOURCE=( [discord]=discord [instagram]=instagram [youtube]=youtube )
services_changed=0
for sid in "${!SVC_SOURCE[@]}"; do
  new_domains="$(fetch "${SVC_SOURCE[$sid]}")"
  if [ -z "$new_domains" ]; then
    log "$sid: источник (geosite:${SVC_SOURCE[$sid]}) недоступен или пуст, пропуск"
    continue
  fi
  printf '%s\n' "$new_domains" > "$TMP/$sid.new"
  count=$(python3 - "$SERVICES" "$sid" "$TMP/$sid.new" <<'PYEOF'
import json, sys
services_path, sid, new_file = sys.argv[1:4]
with open(new_file) as f:
    new = [d.strip() for d in f if d.strip()]
with open(services_path) as f:
    data = json.load(f)
added = []
for svc in data:
    if svc.get('id') != sid:
        continue
    existing = set(d.lower() for d in svc.get('domains', []))
    for d in new:
        if d not in existing:
            svc.setdefault('domains', []).append(d)
            existing.add(d)
            added.append(d)
if added:
    with open(services_path, 'w') as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
        f.write('\n')
print(len(added))
PYEOF
)
  if [ "${count:-0}" -gt 0 ] 2>/dev/null; then
    services_changed=1
    log "$sid: +$count новых доменов из geosite:${SVC_SOURCE[$sid]}"
  else
    log "$sid: новых доменов нет"
  fi
done

# --- ai-services: добавляем в xray/domains/ai-services.txt + рендер xray ---
ai_new="$(fetch "category-ai-chat-!cn")"
if [ -n "$ai_new" ]; then
  existing="$(grep -v '^#' "$AI_FILE" | grep -v '^geosite:' | grep -v '^$' | tr '[:upper:]' '[:lower:]' | sort -u)"
  to_add="$(comm -23 <(printf '%s\n' "$ai_new") <(printf '%s\n' "$existing"))"
  if [ -n "$to_add" ]; then
    {
      echo ""
      echo "# --- авто-добавлено brain-domain-actualize.sh $(date '+%F') из geosite:category-ai-chat-!cn ---"
      printf '%s\n' "$to_add"
    } >> "$AI_FILE"
    n=$(printf '%s\n' "$to_add" | grep -c .)
    log "ai-services: +$n новых доменов из geosite:category-ai-chat-!cn"

    # Перерендер конфига xray — render-config.sh сам валидирует через xray -test
    # и не тронет боевой config.json, если новый список ломает JSON/xray.
    if bash "$REPO_DIR/xray/render-config.sh" \
        --template "$REPO_DIR/xray/config.template.json" \
        --out /opt/xray/config.json \
        --config "$REPO_DIR/config.env" \
        --xray /opt/xray/xray \
        --user-domains-dir /etc/gateway/domains \
        >> "$LOG" 2>&1; then
      systemctl restart xray.service && log "ai-services: xray перезапущен с новым списком"
    else
      log "ai-services: render-config.sh упал — ai-services.txt изменён, но xray НЕ перезапущен (старый конфиг остаётся рабочим)"
    fi
  else
    log "ai-services: новых доменов нет"
  fi
else
  log "ai-services: источник (geosite:category-ai-chat-!cn) недоступен или пуст, пропуск"
fi

log "готово (services_changed=$services_changed)"
