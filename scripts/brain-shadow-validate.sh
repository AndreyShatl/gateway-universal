#!/usr/bin/env bash
# brain-shadow-validate.sh (T-dpi-readiness, 2026-09-18, идея владельца:
# "VPS-пул не переключается, но DPI-подложка всегда на готове — ночная
# проверка как показатель", TSPU меняет блокировки раз в месяцы).
#
# Для каждого домена сервисов в mode=vps: если домен числится в DPI-группе —
# ПРОВЕРИТЬ его стратегию изолированной пробой (тот же --test-args, что
# использует воркер) и записать результат в /etc/gateway/observe/dpi-readiness.
# НИЧЕГО не переключает — это теневая валидация готовности: когда владелец
# щёлкнет dpi/auto (или решит переехать), путь уже проверен сегодняшней ночью.
#
# Двойная подложка в итоге: DPI-доменам VPS-фолбэк живёт в iptables (пакетный
# уровень), VPS-доменам DPI-готовность живёт здесь (проверенная стратегия +
# бесшовное flushless-переключение).

set -u
SOLVE=${SOLVE:-/root/solve.sh}
GWDB=${GWDB:-/root/gateway-universal/scripts/gwdb.py}
OUT=/etc/gateway/observe/dpi-readiness.json
LOG=/var/log/gateway-brain.log

log() { echo "$(date '+%F %T') [shadow-validate] $*" >> "$LOG"; }

python3 - <<'PYEOF' > /tmp/shadow-tasks.tsv
import json
data = json.load(open("/etc/gateway/zapret-services.json"))
if not isinstance(data, list): data = data.get("services", data)
vps_doms = set()
for svc in data:
    if svc.get("mode") == "vps":
        for d in svc.get("domains", []):
            vps_doms.add(d.lower())
groups = []
for f, eng in [("/etc/gateway/brain-services.json","zapret"),
               ("/etc/gateway/brain-services-ciadpi.json","ciadpi"),
               ("/etc/gateway/brain-services-zapret2.json","zapret2")]:
    for g in json.load(open(f)):
        for d in g.get("domains", []):
            if d.lower() in vps_doms:
                groups.append((d, eng, g.get("proto","tcp"), g.get("strategy","")))
seen=set()
for d,e,p,s in groups:
    if d in seen or not s: continue
    seen.add(d)
    print(f"{d}\t{e}\t{p}\t{s}")
PYEOF

total=0; ok=0; fail=0
: > /tmp/readiness.jsonl
while IFS=$'\t' read -r d eng proto strat; do
  [ -n "$d" ] || continue
  total=$((total+1))
  # стратегия передаётся БЕЗ кавычек — как в brain-worker (word-splitting
  # на отдельные аргументы пресета); кавычками я ломал разбор (0/230 в живом
  # прогоне 2026-09-18)
  case "$eng" in
    zapret)  res=$(bash "$SOLVE" --test-args "$d" "$proto" $strat 2>/dev/null | tail -1);;
    ciadpi)  res=$(bash "$SOLVE" --test-ciadpi-args "$d" $strat 2>/dev/null | tail -1);;
    zapret2) res=$(bash "$SOLVE" --test-zapret2-args "$d" "$proto" $strat 2>/dev/null | tail -1);;
  esac
  verdict=false
  case "$res" in OK|ok*|*"успех"*) verdict=true; ok=$((ok+1));; *) fail=$((fail+1));; esac
  printf '{"domain":"%s","engine":"%s","ready":%s,"verified_at":"%s"}\n' \
    "$d" "$eng" "$verdict" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> /tmp/readiness.jsonl
done < /tmp/shadow-tasks.tsv

python3 - <<'PYEOF'
import json, glob
rows = [json.loads(l) for l in open("/tmp/readiness.jsonl") if l.strip()]
json.dump({"generated": __import__("datetime").datetime.utcnow().isoformat()+"Z", "entries": rows},
          open("/etc/gateway/observe/dpi-readiness.json","w"), ensure_ascii=False, indent=1)
PYEOF
rm -f /tmp/readiness.jsonl /tmp/shadow-tasks.tsv
log "готовность DPI для VPS-пула: проверено=$total готово=$ok неготово=$fail (ничего не переключено)"
echo "shadow-validate: проверено=$total готово=$ok неготово=$fail; снапшот: $OUT"
