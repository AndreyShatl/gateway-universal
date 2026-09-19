#!/usr/bin/env bash
# brain-apply-ready.sh <service-id> — T-fast-apply (2026-09-19, идея владельца:
# «переход между подложками за секунду»). Применяет ВСЕ готовые DPI-стратегии
# доменов сервиса из ночного кэша (dpi-readiness.json) БЕЗ единой пробы —
# стратегии уже проверены прошлой ночью (TSPU-темп: месяцы). Не-готовые домены
# НЕ трогаются (остаются на текущем маршруте — обычно VPS-пол/autoroute).
# Массовое применение идёт последовательно (brain-apply перестраивает группы),
# без conntrack-flush для доменов с подложкой — переход незаметен.
set -u
APPLY=/opt/gateway-brain/brain-apply.sh
READY=/etc/gateway/observe/dpi-readiness.json
LOG=/var/log/gateway-brain.log
SID="${1:-}"

log() { echo "$(date '+%F %T') [apply-ready] $*" >> "$LOG"; }

python3 - "$SID" "$READY" <<'PY' > /tmp/ready-tasks.tsv
import json, sys
sid, ready_path = sys.argv[1:3]
svc_domains = set()
data = json.load(open("/etc/gateway/zapret-services.json"))
if not isinstance(data, list): data = data.get("services", data)
for svc in data:
    if not sid or svc.get("id") == sid:
        for d in svc.get("domains", []):
            svc_domains.add(d.lower())
in_group = set()
for f in ["/etc/gateway/brain-services.json","/etc/gateway/brain-services-ciadpi.json","/etc/gateway/brain-services-zapret2.json"]:
    for g in json.load(open(f)):
        for d in g.get("domains", []):
            in_group.add(d.lower())
ready = {}
try:
    for e in json.load(open(ready_path)).get("entries", []):
        if e.get("ready") and e.get("strategy"):
            ready[e["domain"]] = (e.get("engine","zapret"), e.get("proto","tcp"), e["strategy"])
except Exception:
    pass
for d in sorted(svc_domains):
    if d in in_group or d not in ready:
        continue
    eng, proto, strat = ready[d]
    print(f"{d}\t{eng}\t{proto}\t{strat}")
PY

applied=0; skipped=0
while IFS=$'\t' read -r d eng proto strat; do
  [ -n "$d" ] || continue
  if bash "$APPLY" "$eng" "$d" "$proto" "$strat" >/dev/null 2>&1; then
    applied=$((applied+1))
  else
    skipped=$((skipped+1))
  fi
done < /tmp/ready-tasks.tsv
rm -f /tmp/ready-tasks.tsv
log "быстрое применение (${SID:-все сервисы}): применено=$applied ошибок=$skipped (без проб, из ночного кэша)"
echo "apply-ready: применено=$applied ошибок=$skipped"
