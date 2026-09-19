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
  case "$res" in OK|ok*|*"успех"*) verdict=true; ok=$((ok+1));; *)
    fail=$((fail+1))
    # мёртвое членство — снять (домен остаётся на VPS-полу пина; снимает
    # только из группы). Живой прогон 2026-09-18: 231/231 мертвы — наставлены
    # во время DNS-поломки, когда пробы шли через дохлый резолвер.
    bash /opt/gateway-brain/brain-apply.sh vps "$d" >/dev/null 2>&1 || true
    ;; esac
  printf '{"domain":"%s","engine":"%s","ready":%s,"strategy":%s,"verified_at":"%s"}\n' \
    "$d" "$eng" "$verdict" \
    "$(python3 -c "import json,sys; print(json.dumps(sys.argv[1]))" "$strat")" \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> /tmp/readiness.jsonl
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

# ===== Phase B: T-shadow-solve (2026-09-19, идея владельца: «переход за секунду»)
# Для доменов сервисов (не direct) БЕЗ стратегии: полный перебор В ТЕНИ —
# результаты в dpi-readiness.json. Ночь готовит меню, кнопка днём применяет
# готовое мгновенно. Бюджет: не более SHADOW_SOLVE_BUDGET доменов за ночь
# (перебор тяжёлый, 2 ядра; продолжение — следующей ночью, приоритет —
# никогда не искомые, затем самые старые проверки).
SHADOW_SOLVE_BUDGET="${SHADOW_SOLVE_BUDGET:-100}"
SOLVE_LOCK=/tmp/solve-global.lock

python3 - <<'PYB' > /tmp/shadow-solve-tasks.txt
import json
data = json.load(open("/etc/gateway/zapret-services.json"))
if not isinstance(data, list): data = data.get("services", data)
pool = set()
for svc in data:
    if svc.get("mode") != "direct":
        for d in svc.get("domains", []):
            pool.add(d.lower())
in_group = set()
for f in ["/etc/gateway/brain-services.json","/etc/gateway/brain-services-ciadpi.json","/etc/gateway/brain-services-zapret2.json"]:
    for g in json.load(open(f)):
        for d in g.get("domains", []):
            in_group.add(d.lower())
ready = {}
try:
    ready = {e["domain"]: e for e in json.load(open("/etc/gateway/observe/dpi-readiness.json")).get("entries", [])}
except Exception:
    pass
import datetime
now = datetime.datetime.utcnow()
todo = []
for d in pool:
    if d in in_group:
        continue                      # уже в DPI — Phase A проверяет
    e = ready.get(d)
    if e and e.get("strategy") is not None and e.get("verified_at","") > (now - datetime.timedelta(days=2)).isoformat():
        continue                      # свежий вердикт есть (в т.ч. «не пробивается»)
    todo.append((e is None, e.get("verified_at","") if e else "", d))
todo.sort(reverse=True)               # новые сверху, потом самые старые
for _,_,d in todo:
    print(d)
PYB

solved=0; found=0
while read -r d; do
  [ -s /tmp/stop-shadow-solve ] && break          # внешний тормоз
  [ "$solved" -ge "$SHADOW_SOLVE_BUDGET" ] && break
  [ -n "$d" ] || continue
  solved=$((solved+1))
  out=$(ZAPRET=/opt/zapret GWDB="$GWDB" flock "$SOLVE_LOCK" bash "$SOLVE" "$d" shadow 2>/dev/null)
  verdict=$(echo "$out" | grep -E '^(ZAPRET2|ZAPRET|CIADPI|VPS|DIRECT)' | tail -1)
  eng=""; proto="tcp"; strat=""
  case "$verdict" in
    ZAPRET*)  eng="zapret";  proto=$(echo "$verdict" | cut -f2); strat=$(echo "$verdict" | cut -f4-);;
    CIADPI*)  eng="ciadpi";  strat=$(echo "$verdict" | cut -f4-);;
    ZAPRET2*) eng="zapret2"; proto=$(echo "$verdict" | cut -f2); strat=$(echo "$verdict" | cut -f4-);;
    *)        eng=""; strat="";;   # VPS/DIRECT/пусто = не пробивается (тоже вердикт)
  esac
  [ -n "$eng" ] && found=$((found+1))
  printf '{"domain":"%s","engine":"%s","ready":%s,"strategy":%s,"verified_at":"%s"}\n' \
    "$d" "$eng" "$([ -n "$eng" ] && echo true || echo false)" \
    "$(python3 -c "import json,sys; print(json.dumps(sys.argv[1]))" "$strat")" \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> /tmp/readiness.jsonl
done < /tmp/shadow-solve-tasks.txt
rm -f /tmp/shadow-solve-tasks.txt
[ "$solved" -gt 0 ] && log "теневой поиск: решено=$solved найдено_стратегий=$found (бюджет $SHADOW_SOLVE_BUDGET/ночь; хвост — следующей ночью)"
