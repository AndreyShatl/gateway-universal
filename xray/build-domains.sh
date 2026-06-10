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
# Использование:
#   bash xray/build-domains.sh [DOMAINS_DIR]
# =====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
DOMAINS_DIR="${1:-$SCRIPT_DIR/domains}"

[[ -d "$DOMAINS_DIR" ]] || { echo "build-domains: no dir $DOMAINS_DIR" >&2; exit 1; }

# Нормализовать, дедуплицировать (сохраняя geosite:/domain: префикс) и
# напечатать как элементы JSON-массива с отступом 10 пробелов, через запятую.
out="$(awk '
    { line=$0; sub(/#.*/,"",line); gsub(/[ \t\r]/,"",line); if(line=="") next;
      if(line ~ /^geosite:/) e="geosite:" substr(line,9); else e="domain:" line;
      if(!(e in seen)){ seen[e]=1; arr[n++]=e } }
    END{ for(i=0;i<n;i++) printf "          \"%s\"%s\n", arr[i], (i==n-1?"":",") }
' "$DOMAINS_DIR"/*.txt)"

[[ -n "$out" ]] || { echo "build-domains: no domains found" >&2; exit 1; }
printf '%s\n' "$out"
