#!/usr/bin/env bash
# Проверка регрессии T-ggc-local-cache без iptables, ipset и сети.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORKER="$ROOT/scripts/brain-worker.sh"
APPLY="$ROOT/scripts/brain-apply.sh"

# Извлекаем ровно pure helper, не source'им worker целиком: у него есть
# боевые пути /etc/gateway и бесконечный loop.
prefix=$(mktemp)
trap 'rm -f "$prefix"' EXIT
sed -n '/^ggc_delivery_host()/,/^}/p' "$WORKER" > "$prefix"
# shellcheck disable=SC1090
source "$prefix"

ggc_delivery_host 'rr1---sn-8ph2xajvh-gufl.googlevideo.com'
ggc_delivery_host 'rr3---sn-4g5lznsl.gvt1.com'
! ggc_delivery_host 'youtube.com'
! ggc_delivery_host 'evilgooglevideo.com'

# Оба защитных механизма должны оставаться в боевом коде: /24 только для GGC
# и постановка failover-домена в единственную brain-очередь.
grep -Fq 'googlevideo.com|*.googlevideo.com|gvt1.com|*.gvt1.com' "$APPLY"
grep -Fq '".0/24"' "$APPLY"
grep -Fq 'enqueueBrainForRecheck(domain, source)' "$ROOT/detector/live_retrigger.go"
grep -Fq 'confirm-local) shift; ar_del "$1"' "$APPLY"
grep -Fq 'vps-fallback) shift' "$APPLY"
grep -Fq 'vps-fallback "$domain"' "$WORKER"
grep -Fq 'confirm_local_at_nightly "$domain" "$source"' "$WORKER"

echo 'ok: GGC LOCAL recovery guards'
