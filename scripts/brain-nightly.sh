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

# РАБОТАЮЩИЕ zapret-сущности НЕ трогаем: re-solve флакует (Cloudflare-IP/тайминги)
# и может сломать рабочую сущность. Они работают — оставляем. GC разблокированных
# сущностей — отдельно/вручную (редко нужно).
ents=()
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
  grep -qxF "$d" "$QUEUE" 2>/dev/null || { echo "$d" >> "$QUEUE"; n=$((n+1)); }
done
flock -u 9
echo "$(date '+%F %T') 🌙 ночной проход: в очередь $n доменов (сущности=${#ents[@]}, vps=${#vps[@]})" >> "$LOG"
