#!/usr/bin/env bash
# =====================================================================
# build-domains.sh — генерирует JSON-фрагмент списка доменов для роутинга
# из файлов xray/domains/*.txt (единственный источник истины для роутинга).
#
# Каждая непустая строка не-комментарий превращается в элемент массива:
#   geosite:google   -> "geosite:google"
#   youtube.com      -> "domain:youtube.com"
#
# Вывод — содержимое JSON-массива (без внешних скобок), с отступом,
# готовое для подстановки в ${ROUTING_DOMAINS} в config.template.json.
#
# Читает .txt из всех переданных каталогов (дедуп между ними). Без аргументов —
# каталог рядом со скриптом (курируемые списки) + /etc/gateway/domains
# (пользовательские домены из UI, вне репо — переживают передеплой).
# Несуществующий каталог пропускается.
#
# Использование:
#   bash xray/build-domains.sh [DIR ...]
# =====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"

if [[ $# -gt 0 ]]; then DIRS=("$@"); else DIRS=("$SCRIPT_DIR/domains" "/etc/gateway/domains"); fi

# Собрать существующие .txt по всем каталогам
FILES=()
for d in "${DIRS[@]}"; do
    [[ -d "$d" ]] || continue
    for f in "$d"/*.txt; do [[ -e "$f" ]] && FILES+=("$f"); done
done
[[ ${#FILES[@]} -gt 0 ]] || { echo "build-domains: нет .txt в: ${DIRS[*]}" >&2; exit 1; }

# Исключения: домены, отданные в zapret (локальный DPI-обход), не должны идти
# в VPS-туннель — иначе zapret их не увидит. Вычитаем их из VPS-списка.
SERVICES_JSON="${GATEWAY_ZAPRET_SERVICES:-/etc/gateway/zapret-services.json}"
[[ -f "$SERVICES_JSON" ]] || SERVICES_JSON="$SCRIPT_DIR/../zapret/services.json"
EXCLUDE_FILE=""
if command -v jq >/dev/null 2>&1 && [[ -f "$SERVICES_JSON" ]]; then
    # mode=zapret → домены идут напрямую, исключаем из VPS.
    # mode=vps    → домены сервиса добавляем в VPS-список (источник — карточка сервиса).
    EXCLUDE_FILE="$(mktemp)"; INCLUDE_FILE="$(mktemp)"
    trap 'rm -f "$EXCLUDE_FILE" "$INCLUDE_FILE"' EXIT
    # exclude: всё, что НЕ vps (zapret и direct идут мимо туннеля).
    # include: только vps — домены сервиса добавляем в VPS-список.
    jq -r '.[] | select((.mode // "zapret")!="vps") | .domains[]?' "$SERVICES_JSON" 2>/dev/null > "$EXCLUDE_FILE" || true
    jq -r '.[] | select(.mode=="vps") | .domains[]?' "$SERVICES_JSON" 2>/dev/null > "$INCLUDE_FILE" || true
    [[ -s "$INCLUDE_FILE" ]] && FILES+=("$INCLUDE_FILE")
fi

# Нормализовать, дедуплицировать (сохраняя geosite:/domain: префикс), исключить
# zapret-домены и напечатать как элементы JSON-массива.
out="$(awk -v exf="$EXCLUDE_FILE" '
    BEGIN{ if(exf!=""){ while((getline d < exf)>0){ gsub(/[ \t\r]/,"",d); if(d!="") ex[tolower(d)]=1 } } }
    { line=$0; sub(/#.*/,"",line); gsub(/[ \t\r]/,"",line); if(line=="") next;
      if(line ~ /^geosite:/){ e="geosite:" substr(line,9) }
      else { if(tolower(line) in ex) next; e="domain:" line }
      if(!(e in seen)){ seen[e]=1; arr[n++]=e } }
    END{ for(i=0;i<n;i++) printf "          \"%s\"%s\n", arr[i], (i==n-1?"":",") }
' "${FILES[@]}")"

[[ -n "$out" ]] || { echo "build-domains: no domains found" >&2; exit 1; }
printf '%s\n' "$out"
