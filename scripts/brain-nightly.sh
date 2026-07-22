#!/usr/bin/env bash
# brain-nightly.sh — ночной проход: закинуть все управляемые домены в очередь
# воркера на ПЕРЕОЦЕНКУ. Воркер: solve -> zapret/VPS; DIRECT -> убрать (GC).
# Так список сам мигрирует: разблокированные уходят, VPS-домены пробуются на
# zapret (низкий пинг). Discord (services.json mode=vps) НЕ в списках — не трогаем.
set -uo pipefail

QUEUE=/etc/gateway/brain-queue
LOCK=/etc/gateway/brain-queue.lock
STATE=/etc/gateway/brain-services.json
AR=/etc/gateway/autoroute.json
LOG=/var/log/gateway-brain.log

# домены zapret-сущностей — тоже переоцениваем (solve теперь детерминирован после
# фикса veth): вдруг разблокировали (DIRECT->GC) или стратегия перестала пробивать.
mapfile -t ents < <(python3 -c "import json,os;print('\n'.join(x['domain'] for x in (json.load(open('$STATE')) if os.path.exists('$STATE') else [])))" 2>/dev/null)
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

exec 9>"$LOCK"; flock 9
n=0
for d in "${ents[@]}" "${vps[@]}"; do
  [ -n "$d" ] || continue
  # дедуп по домену (первое поле до табуляции, T50 — строки вида "domain\tsource");
  # source=reeval — переоценка идёт без сигнатуры детектора, полный перебор пресетов.
  grep -qE "^${d//./\\.}($|	)" "$QUEUE" 2>/dev/null || { printf '%s\treeval\n' "$d" >> "$QUEUE"; n=$((n+1)); }
done
flock -u 9
echo "$(date '+%F %T') 🌙 ночной проход: в очередь $n доменов (сущности=${#ents[@]}, vps=${#vps[@]})" >> "$LOG"

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
