#!/usr/bin/env bash
# direct-fastpath.sh — снижение CPU-нагрузки xray на тяжёлом direct-трафике
# (T-shattl-feedback-3, 2026-08-09): весь LAN TCP 80/443 редиректится на
# xray:12345 ради решения маршрута (direct/proxy), даже когда решение
# заведомо "direct" (Steam, торренты через HTTP-трекеры, видео и т.п.) —
# xray продолжает эту оценку на КАЖДОЕ новое TCP-соединение, а их при
# закачке — сотни. Слушаем лог xray: как только видим
# "taking detour [direct]" с последующим резолвом IP, заносим этот IP в
# ipset с коротким TTL (5 мин) — следующие соединения к нему минуют xray
# совсем (RETURN в PREROUTING раньше основного REDIRECT).
#
# Риск CDN (Fastly/Cloudflare/Akamai раздают разные домены с одного IP по
# SNI) — сознательно ограничен коротким TTL: даже если CDN подменит IP под
# другой (возможно заблокированный) домен, окно ошибочного пропуска — не
# больше 5 минут, а не постоянно.
set -uo pipefail

IPSET=gw_direct_fastpath
TIMEOUT=300 # 5 минут — короткий TTL, см. риск CDN выше

ipset create "$IPSET" hash:ip family inet timeout "$TIMEOUT" -exist

# RETURN должен стоять РАНЬШЕ основного REDIRECT (см. vps-mode.sh
# main_redirect) — иначе решение "уже известно как direct" не успеет
# сработать. Позиция 1 — самая ранняя, безопасно: match только по ipset,
# который заполняется исключительно из подтверждённых xray-решений.
ensure_rule() {
  iptables -t nat -C PREROUTING -m set --match-set "$IPSET" dst -j RETURN 2>/dev/null \
    || iptables -t nat -I PREROUTING 1 -m set --match-set "$IPSET" dst -j RETURN
}
ensure_rule

log() { echo "$(date '+%F %T') $*"; }
log "direct-fastpath: запущен, ipset=$IPSET timeout=${TIMEOUT}s"

declare -A pending
count=0

journalctl -u xray -f -n0 -o cat --no-pager | while IFS= read -r line; do
  if [[ "$line" =~ \[([0-9]+)\][^[]*taking\ detour\ \[direct\] ]]; then
    pending["${BASH_REMATCH[1]}"]=1
    continue
  fi
  if [[ "$line" =~ \[([0-9]+)\][^[]*dialing\ TCP\ to\ tcp:([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+): ]]; then
    id="${BASH_REMATCH[1]}"
    ip="${BASH_REMATCH[2]}"
    if [[ -n "${pending[$id]:-}" ]]; then
      ipset add "$IPSET" "$ip" timeout "$TIMEOUT" -exist 2>/dev/null
      unset 'pending[$id]'
    fi
  fi
  # Защита от утечки памяти: если xray когда-либо залогирует "direct" без
  # последующего "dialing" (ошибка/таймаут на стороне xray) — запись в
  # pending останется навсегда. Раз в ~10000 строк грубо сбрасываем.
  count=$((count + 1))
  if (( count % 10000 == 0 )); then
    pending=()
  fi
done
