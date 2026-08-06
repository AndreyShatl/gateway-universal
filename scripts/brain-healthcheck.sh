#!/usr/bin/env bash
# brain-healthcheck.sh — дежурная проверка, что фоновые демоны brain-групп
# (nfqws/ciadpi/nfqws2) реально живы, а не только числятся в iptables/ipset.
#
# Найдено вживую 2026-08-02: все 5 nfqws-групп (включая ту, где сидели
# Instagram/YouTube/Discord) умерли молча где-то в середине дня — iptables-
# правила остались (трафик по-прежнему НЕ шёл через VPS), но реального
# DPI-обхода уже не происходило, домены просто резались провайдером "голыми".
# gateway-brain-restore.service чинит это только ПОСЛЕ ребута шлюза — на
# "процесс тихо умер посреди работы" автолечения не было вообще.
#
# Логика: только ЧТЕНИЕ (systemctl is-active) — ничего не трогаем, пока не
# нашли реальную дыру. brain-apply.sh restore вызывается ЦЕЛИКОМ (не по одной
# группе) только если хоть один демон не активен — сам restore идемпотентен
# для уже живых групп (systemd-run на уже активный unit просто вернёт ошибку
# и ничего не сломает, см. reset-failed+--collect во всех start_*_daemon()).
set -uo pipefail

STATE=/etc/gateway/brain-services.json
CSTATE=/etc/gateway/brain-ciadpi-services.json
Z2STATE=/etc/gateway/brain-zapret2-services.json
LOG=/var/log/gateway-brain.log
BRAIN_APPLY=/opt/gateway-brain/brain-apply.sh

log() { echo "$(date '+%F %T') [healthcheck] $*" >> "$LOG"; }

missing=0

check_units() {
  local statefile="$1" prefix="$2" label="$3"
  [ -f "$statefile" ] || return 0
  local gids
  gids=$(python3 -c "
import json
try:
    d = json.load(open('$statefile'))
except Exception:
    d = []
for g in d:
    print(g['group_id'])
" 2>/dev/null)
  [ -n "$gids" ] || return 0
  while IFS= read -r gid; do
    [ -n "$gid" ] || continue
    unit="${prefix}${gid}"
    if ! systemctl is-active --quiet "$unit"; then
      log "⚠ $label демон не активен: $unit"
      missing=1
    fi
  done <<< "$gids"
}

check_units "$STATE" "brain-nfqws-" "zapret"
check_units "$CSTATE" "brain-ciadpi-" "ciadpi"
check_units "$Z2STATE" "brain-nfqws2-" "zapret2"

if [ "$missing" -eq 1 ]; then
  log "обнаружены мёртвые демоны — вызываю restore (restore уже сам делает zapret+ciadpi+zapret2, идемпотентно для уже живых групп)"
  "$BRAIN_APPLY" restore >> "$LOG" 2>&1
  log "restore завершён"
else
  log "все демоны живы, ничего не трогаю"
fi
