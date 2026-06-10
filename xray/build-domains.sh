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

# Нормализовать, дедуплицировать (сохраняя geosite:/domain: префикс) и
# напечатать как элементы JSON-массива с отступом 10 пробелов, через запятую.
out="$(awk '
    { line=$0; sub(/#.*/,"",line); gsub(/[ \t\r]/,"",line); if(line=="") next;
      if(line ~ /^geosite:/) e="geosite:" substr(line,9); else e="domain:" line;
      if(!(e in seen)){ seen[e]=1; arr[n++]=e } }
    END{ for(i=0;i<n;i++) printf "          \"%s\"%s\n", arr[i], (i==n-1?"":",") }
' "${FILES[@]}")"

[[ -n "$out" ]] || { echo "build-domains: no domains found" >&2; exit 1; }
printf '%s\n' "$out"
