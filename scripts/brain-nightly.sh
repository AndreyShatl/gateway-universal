#!/usr/bin/env bash
# brain-nightly.sh v2 (T-consolidate, 2026-07-23) — ночной проход: закинуть все
# управляемые домены в очередь воркера на ПЕРЕОЦЕНКУ. Вся логика "сначала своя
# группа, потом другие существующие, потом полный перебор" теперь в
# brain-worker.sh (process_domain) — nightly просто собирает список доменов и
# кладёт в очередь, как раньше. State теперь ГРУППОВОЙ (T-consolidate):
# [{group_id, proto, strategy, queue, domains:[...]}], не {domain: ...} — читаем
# домены из ВСЕХ групп, а не x['domain'] (иначе KeyError на новом формате).
# Discord (services.json mode=vps) НЕ в списках — не трогаем.
set -uo pipefail

QUEUE=/etc/gateway/brain-queue
LOCK=/etc/gateway/brain-queue.lock
STATE=/etc/gateway/brain-services.json
AR=/etc/gateway/autoroute.json
LOG=/var/log/gateway-brain.log
GWDB=${GWDB:-/root/gateway-universal/scripts/gwdb.py}
PROGRESS=/etc/gateway/brain-progress.json

# bump_progress N — прибавить N к общему счётчику "поставлено сегодня" (T-progress-ui,
# для прогресс-бара в gateway-ui). Сбрасывается в 0 в brain-worker.sh, когда очередь
# опустела (см. main loop) — новый цикл enqueue снова стартует с чистого total.
bump_progress() {
  python3 -c "
import json, time
f='$PROGRESS'
try:
    d = json.load(open(f))
except Exception:
    d = {}
if d.get('total', 0) <= 0:
    d = {'total': 0, 'started_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
d['total'] = d.get('total', 0) + $1
json.dump(d, open(f, 'w'))
"
}

# --- T-are: угасание score стратегий (раз в сутки, см. cmd_strategies_decay
# в gwdb.py) — без этого давно неподтверждённая стратегия навсегда котируется
# выше свежепроверенной, мозг никогда не "забывает" устаревшее.
decay_out=$(python3 "$GWDB" strategies-decay --factor 0.995 2>&1)
echo "$(date '+%F %T') 📉 $decay_out" >> "$LOG"

# домены brain-групп — тоже переоцениваем (T-consolidate: состояние теперь
# групповое, читаем domains[] из КАЖДОЙ группы, не x['domain']).
mapfile -t ents < <(python3 -c "
import json,os
data=json.load(open('$STATE')) if os.path.exists('$STATE') else []
print('\n'.join(d for g in data for d in g.get('domains',[])))
" 2>/dev/null)
# домены VPS-автообхода (не IP, не geosite, НЕ покрытые zapret-сервисом) —
# пробуем перевести на zapret. Покрытые сервисом (instagram/youtube CDN) пропускаем.
mapfile -t vps < <(python3 -c "
import json,os,re,glob
hosts=set()
for hf in glob.glob('/opt/zapret-config/domains.gen/*.txt'):
  for ln in open(hf):
    ln=ln.strip()
    if ln and not ln.startswith('#'): hosts.add(ln)
def covered(d):
  p=d.split('.'); return any('.'.join(p[i:]) in hosts for i in range(len(p)-1))
f='$AR'
if os.path.exists(f):
  for e in json.load(open(f)).get('entries',[]):
    a=(e.get('addr') if isinstance(e,dict) else e) or ''
    if a and not re.match(r'^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+',a) and not a.startswith('geosite:') and not covered(a):
      print(a)
" 2>/dev/null)

# --- T-are: confidence-гистерезис — домены с ДАВНО стабильно работающей
# стратегией (service-touch копит streak успехов в history) можно сегодня не
# трогать вообще, не гоняя даже дешёвый --test-args. Применяется ТОЛЬКО к ents[]
# (домены УЖЕ в рабочей zapret/ciadpi-группе) — vps[] всегда проверяем заново,
# там как раз хотим поймать момент, когда обход наконец-то заработает.
declare -A skip
mapfile -t skip_list < <(python3 "$GWDB" service-skip-list 2>/dev/null)
for d in "${skip_list[@]}"; do [ -n "$d" ] && skip["$d"]=1; done

exec 9>"$LOCK"; flock 9
n=0; skipped=0
for d in "${ents[@]}"; do
  [ -n "$d" ] || continue
  if [ -n "${skip[$d]:-}" ]; then skipped=$((skipped+1)); continue; fi
  # дедуп по домену (первое поле до табуляции, T50 — строки вида "domain\tsource");
  # source=reeval — переоценка идёт без сигнатуры детектора, полный перебор пресетов.
  grep -qE "^${d//./\\.}($|	)" "$QUEUE" 2>/dev/null || { printf '%s\treeval\n' "$d" >> "$QUEUE"; n=$((n+1)); }
done
for d in "${vps[@]}"; do
  [ -n "$d" ] || continue
  grep -qE "^${d//./\\.}($|	)" "$QUEUE" 2>/dev/null || { printf '%s\treeval\n' "$d" >> "$QUEUE"; n=$((n+1)); }
done
flock -u 9
[ "$n" -gt 0 ] && bump_progress "$n"
echo "$(date '+%F %T') 🌙 ночной проход: в очередь $n доменов (сущности=${#ents[@]}, vps=${#vps[@]}, пропущено по confidence=$skipped)" >> "$LOG"

# --- T55: no_bypass-статус для VPS-доменов + чистка устаревших записей ---
# «Ничего не работает» (ни напрямую, ни через VPS) — раньше такого состояния не
# было вообще, воркер всегда держал домен на VPS. Проверяем те же ${vps[@]}
# (не покрытые zapret-сервисом) прямой prober'ом детектора (verdict=down у него
# уже ровно значит «direct FAIL + vps FAIL» — переиспользуем, не дублируем).
GWDET=${GWDET:-/opt/gateway-detector}
SOCKS=${SOCKS:-127.0.0.1:1081}
NO_BYPASS_CLEANUP_DAYS=${NO_BYPASS_CLEANUP_DAYS:-30}

if [ -x "$GWDET" ]; then
  nobypass=(); recovered=()
  for d in "${vps[@]}"; do
    [ -n "$d" ] || continue
    verdict=$("$GWDET" probe "$d" --socks "$SOCKS" 2>/dev/null | python3 -c "import json,sys;print(json.load(sys.stdin).get('verdict',''))" 2>/dev/null)
    case "$verdict" in
      down)    nobypass+=("$d") ;;
      blocked) recovered+=("$d") ;;  # работает через VPS — снять no_bypass, если была
    esac
  done
  nb_joined=$(IFS=$'\x1f'; echo "${nobypass[*]:-}")
  rec_joined=$(IFS=$'\x1f'; echo "${recovered[*]:-}")
  python3 - "$AR" "$nb_joined" "$rec_joined" "$NO_BYPASS_CLEANUP_DAYS" <<'PY' >> "$LOG" 2>&1
import json, os, sys, time
from datetime import datetime, timezone

f, nb_raw, rec_raw, cleanup_days = sys.argv[1], sys.argv[2], sys.argv[3], float(sys.argv[4])
if not os.path.exists(f):
    sys.exit()
nobypass = set(nb_raw.split("\x1f")) if nb_raw else set()
recovered = set(rec_raw.split("\x1f")) if rec_raw else set()
now = datetime.now(timezone.utc)
now_s = now.strftime("%Y-%m-%dT%H:%M:%SZ")

data = json.load(open(f))
kept, removed, marked, cleared = [], 0, 0, 0
for e in data.get("entries", []):
    if not isinstance(e, dict):
        e = {"addr": e}
    addr = e.get("addr", "")
    if addr in nobypass:
        if e.get("status") != "no_bypass":
            marked += 1
        e["status"] = "no_bypass"
        e["checked_at"] = now_s
    elif addr in recovered:
        if e.get("status") == "no_bypass":
            cleared += 1
        e.pop("status", None)
        e["checked_at"] = now_s
    if e.get("status") == "no_bypass" and e.get("checked_at"):
        try:
            checked = datetime.strptime(e["checked_at"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
            if (now - checked).total_seconds() / 86400 >= cleanup_days:
                removed += 1
                continue
        except ValueError:
            pass
    kept.append(e)
data["entries"] = kept
json.dump(data, open(f, "w"), ensure_ascii=False, indent=1)
print(f"{time.strftime('%F %T')} 🚫 no_bypass: помечено {marked}, снято {cleared}, проверено VPS-рабочих {len(recovered)}, удалено устаревших {removed}")
PY
fi
