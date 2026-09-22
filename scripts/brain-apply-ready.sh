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

# T-batch-apply (2026-09-21): вместо brain-apply на каждый домен (каждый
# вызов = полная пересборка группы, ~2 мин на слабом CPU) — вставляем домены
# напрямую в state-JSON подходящих групп (та же proto+strategy), затем ОДНА
# пересборка на движок (restore-*). Отдельным brain-apply — только домены,
# для которых готовой группы нет (редко).
applied=0; skipped=0; leftovers=""
while IFS=$'\t' read -r d eng proto strat; do
  [ -n "$d" ] || continue
  n=$(python3 - "$eng" "$proto" "$strat" "$d" <<'PYB'
import json, sys
eng, proto, strat, dom = sys.argv[1:5]
files = {"zapret":"/etc/gateway/brain-services.json",
         "ciadpi":"/etc/gateway/brain-services-ciadpi.json",
         "zapret2":"/etc/gateway/brain-services-zapret2.json"}
path = files[eng]
try: data = json.load(open(path))
except Exception: print(0); raise SystemExit
for g in data:
    gstrat = g.get("strategy","")
    if gstrat == strat and g.get("proto","tcp") == proto:
        doms = [x.lower() for x in g.get("domains",[])]
        if dom not in doms:
            g.setdefault("domains",[]).append(dom)
            json.dump(data, open(path,"w"), ensure_ascii=False, indent=2)
        print(1); raise SystemExit
print(0)
PYB
)
  if [ "$n" = "1" ]; then
    applied=$((applied+1))
  else
    leftovers="$leftovers$d\t$eng\t$proto\t$strat\n"
  fi
done < /tmp/ready-tasks.tsv
rm -f /tmp/ready-tasks.tsv

# одна пересборка на движок, где что-то добавили
bash "$APPLY" restore >/dev/null 2>&1 &
bash "$APPLY" restore-ciadpi >/dev/null 2>&1
bash "$APPLY" restore-zapret2 >/dev/null 2>&1

# хвост: домены без подходящей готовой группы — штатно поштучно
if [ -n "$leftovers" ]; then
  printf '%b' "$leftovers" | while IFS=$'\t' read -r d eng proto strat; do
    [ -n "$d" ] || continue
    bash "$APPLY" "$eng" "$d" "$proto" $strat >/dev/null 2>&1 && applied=$((applied+1)) || skipped=$((skipped+1))
  done
fi
log "быстрое применение (${SID:-все сервисы}): применено=$applied ошибок=$skipped (без проб, из ночного кэша)"
echo "apply-ready: применено=$applied ошибок=$skipped"
