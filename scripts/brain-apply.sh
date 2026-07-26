#!/usr/bin/env bash
# brain-apply.sh v2 (T-consolidate, 2026-07-23) — применить решение «мозга» как
# ГРУППУ доменов с ОБЩЕЙ стратегией (не сущность-на-домен, как было в T44-46/T52).
# Один ipset + одна очередь + один nfqws-демон обслуживают ВСЕ домены, для которых
# нашлась ОДНА И ТА ЖЕ рабочая стратегия (по proto+args) — резко меньше процессов,
# та же изоляция от боевых 200/201 (см. svc_rules, не изменилась).
#
# CLI ПРЕЖНИЙ (brain-worker.sh/brain-nightly.sh не меняются):
#   brain-apply.sh zapret  <domain> <tcp|udp> <strategy-args...>   — добавить домен
#     в группу с этой proto+strategy (создаёт группу, если такой ещё нет)
#   brain-apply.sh vps     <domain>
#   brain-apply.sh remove  <domain>
#   brain-apply.sh list
#   brain-apply.sh restore
# НОВОЕ (для миграции/консолидации, T-consolidate):
#   brain-apply.sh group-of <domain>            — group_id/proto/strategy/queue домена
#   brain-apply.sh groups                       — список групп (агрегировано)
#   brain-apply.sh move <domain> <group_id>     — перекинуть домен в СУЩЕСТВУЮЩУЮ группу
#     (без затрагивания её strategy — используется, когда nightly находит для домена
#     стратегию, СОВПАДАЮЩУЮ с уже существующей группой)
set -uo pipefail

STATE=/etc/gateway/brain-services.json
ZAPRET=${ZAPRET:-/opt/zapret}; NFQWS=$ZAPRET/nfq/nfqws; FAKEDIR=$ZAPRET/files/fake
LAN=192.168.0.0/16; MARK=0x40000000; QBASE=210; QPOOL=500

san() { echo "$1" | tr -c 'a-z0-9' '_' | sed 's/_*$//'; }

# group_id — стабильный id по (proto, strategy): md5 первые 10 hex. Пустая strategy
# (accept-only UDP, T57) тоже хешируется нормально — все accept-only домены одного
# proto естественно попадают в ОДНУ группу (стратегии дискриминировать нечем).
group_id() { echo -n "$1|$2" | md5sum | cut -c1-10 | sed 's/^/grp_/'; }

alloc_queue() {
  local used q
  # T-consolidate: помимо правил iptables и state — ЕЩЁ проверяем реально
  # запущенные nfqws (--qnum=N). Найдена реальная дыра: демон может остаться
  # живым без своего iptables-правила (напр. zapret.service однажды сфлашил
  # mangle POSTROUTING рестартом, демоны survived, правила — нет) — тогда
  # старый alloc_queue (только iptables) считал очередь свободной и коллизировал
  # с живым процессом, который её уже держит на уровне ядра (nfq_create_queue
  # EPERM). Проверка процессов закрывает этот класс коллизий независимо от причины.
  used=$(iptables -t mangle -S POSTROUTING 2>/dev/null | grep -oE 'queue-num [0-9]+' | awk '{print $2}')
  used="$used $(pgrep -a nfqws 2>/dev/null | grep -oE 'qnum=[0-9]+' | cut -d= -f2)"
  used="$used $(python3 -c "import json,os;f='$STATE';print(' '.join(str(x['queue']) for x in (json.load(open(f)) if os.path.exists(f) else []) if x.get('queue') is not None))" 2>/dev/null)"
  for q in $(seq $QBASE $((QBASE+QPOOL))); do echo "$used" | tr ' ' '\n' | grep -qx "$q" || { echo "$q"; return 0; }; done
  echo "alloc_queue: пул исчерпан ($QBASE..$((QBASE+QPOOL)))" >&2
  return 1
}

# правила группы — МОДЕЛЬ НЕ ИЗМЕНИЛАСЬ с T57 (см. историю в git/DECISIONS), просто
# $ipset теперь на ГРУППУ, а не на домен:
#  TCP: nat PREROUTING RETURN обходит xray-redirect :12345.
#  UDP: mangle PREROUTING + filter FORWARD ACCEPT (обход глобального DROP на UDP/443).
# POSTROUTING NFQUEUE+ACCEPT — пропускается, если q="" (accept-only).
svc_rules() { # <op:-A|-D> <proto:tcp|udp> <ipset> <qnum-or-empty>
  local op=$1 proto=$2 ipset=$3 q=$4
  local base=(-s $LAN -p $proto -m multiport --dports 443 -m addrtype ! --src-type LOCAL -m set --match-set $ipset dst)
  if [ "$proto" = "tcp" ]; then
    base=(-s $LAN -p tcp -m multiport --dports 80,443 -m addrtype ! --src-type LOCAL -m set --match-set $ipset dst)
  fi
  if [ "$op" = "-A" ]; then
    if [ "$proto" = "tcp" ]; then
      local nat_ret=(-s $LAN -p tcp -m set --match-set $ipset dst -j RETURN)
      iptables -t nat -C PREROUTING "${nat_ret[@]}" 2>/dev/null || iptables -t nat -I PREROUTING 1 "${nat_ret[@]}"
    else
      local pre_acc=(-s $LAN -p udp --dport 443 -m set --match-set $ipset dst -j ACCEPT)
      local fwd_acc=(-s $LAN -p udp --dport 443 -m set --match-set $ipset dst -j ACCEPT)
      iptables -t mangle -C PREROUTING "${pre_acc[@]}" 2>/dev/null || iptables -t mangle -I PREROUTING 1 "${pre_acc[@]}"
      iptables -C FORWARD "${fwd_acc[@]}" 2>/dev/null || iptables -I FORWARD 1 "${fwd_acc[@]}"
    fi
    [ -n "$q" ] || return 0
    iptables -t mangle -C POSTROUTING "${base[@]}" -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $q --queue-bypass 2>/dev/null || \
    iptables -t mangle -I POSTROUTING 1 "${base[@]}" -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $q --queue-bypass
    iptables -t mangle -C POSTROUTING "${base[@]}" -j ACCEPT 2>/dev/null || iptables -t mangle -I POSTROUTING 2 "${base[@]}" -j ACCEPT
  else
    if [ "$proto" = "tcp" ]; then
      local nat_ret=(-s $LAN -p tcp -m set --match-set $ipset dst -j RETURN)
      iptables -t nat -D PREROUTING "${nat_ret[@]}" 2>/dev/null
    else
      local pre_acc=(-s $LAN -p udp --dport 443 -m set --match-set $ipset dst -j ACCEPT)
      local fwd_acc=(-s $LAN -p udp --dport 443 -m set --match-set $ipset dst -j ACCEPT)
      iptables -t mangle -D PREROUTING "${pre_acc[@]}" 2>/dev/null
      iptables -D FORWARD "${fwd_acc[@]}" 2>/dev/null
    fi
    [ -n "$q" ] || return 0
    iptables -t mangle -D POSTROUTING "${base[@]}" -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $q --queue-bypass 2>/dev/null
    iptables -t mangle -D POSTROUTING "${base[@]}" -j ACCEPT 2>/dev/null
  fi
}

start_daemon() { # <group_id> <proto> <qnum> <strategy...>
  local gid=$1 proto=$2 q=$3; shift 3; local strat="$*"; strat=${strat//\$FAKE/$FAKEDIR}
  local unit=brain-nfqws-$gid
  systemctl reset-failed "$unit" 2>/dev/null
  systemd-run --unit="$unit" --collect $NFQWS --qnum=$q --dpi-desync-fwmark=$MARK --filter-$proto=443 $strat >/dev/null 2>&1
}
stop_daemon() { systemctl stop "brain-nfqws-$1" 2>/dev/null; systemctl reset-failed "brain-nfqws-$1" 2>/dev/null; }

# --- состояние: /etc/gateway/brain-services.json — МАССИВ ГРУПП (не доменов):
# [{"group_id","proto","strategy","queue","domains":[...],"packets","last_active"}]
state_load() { python3 -c "import json,os;f='$STATE';print(json.dumps(json.load(open(f)) if os.path.exists(f) else []))"; }

state_find_group_by_strategy() { # <proto> <strategy> -> group_id или пусто
  python3 - "$STATE" "$1" "$2" <<'PY'
import json,os,sys
f,proto,strat=sys.argv[1],sys.argv[2],sys.argv[3]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if g.get("proto")==proto and g.get("strategy","")==strat:
        print(g["group_id"]); break
PY
}

state_find_group_by_id() { # <group_id> -> "proto<TAB>strategy<TAB>queue"
  python3 - "$STATE" "$1" <<'PY'
import json,os,sys
f,gid=sys.argv[1],sys.argv[2]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if g["group_id"]==gid:
        q=g.get("queue")
        print(f"{g.get('proto','tcp')}\t{g.get('strategy','')}\t{q if q is not None else ''}")
        break
PY
}

state_find_group_for_domain() { # <domain> -> "group_id<TAB>proto<TAB>strategy<TAB>queue" или пусто
  python3 - "$STATE" "$1" <<'PY'
import json,os,sys
f,d=sys.argv[1],sys.argv[2]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if d in g.get("domains",[]):
        q=g.get("queue")
        print(f"{g['group_id']}\t{g.get('proto','tcp')}\t{g.get('strategy','')}\t{q if q is not None else ''}")
        break
PY
}

state_upsert_group() { # <group_id> <proto> <strategy> <queue-or-empty> <domains-csv>
  python3 - "$STATE" "$1" "$2" "$3" "$4" "$5" <<'PY'
import json,os,sys,time
f,gid,proto,strat,qraw,domains_csv=sys.argv[1:7]
q=int(qraw) if qraw else None
domains=[d for d in domains_csv.split(",") if d]
data=json.load(open(f)) if os.path.exists(f) else []
data=[g for g in data if g["group_id"]!=gid]
data.append({"group_id":gid,"proto":proto,"strategy":strat,"queue":q,"domains":domains,
             "packets":0,"last_active":time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime())})
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
}

state_remove_group() { python3 -c "import json,os;f='$STATE';d=[g for g in (json.load(open(f)) if os.path.exists(f) else []) if g['group_id']!='$1'];json.dump(d,open(f,'w'),ensure_ascii=False,indent=1)"; }

# rebuild_group_ipset — ПОЛНАЯ пересборка (flush+resolve+add) для списка доменов.
# Всегда полная, не инкрементальная — проще и безопаснее (CDN IP ротируются, нет
# риска оставить чужой устаревший IP в общем ipset группы).
rebuild_group_ipset() { # <ipset> <domain...>
  local ipset=$1; shift
  ipset create $ipset hash:ip family inet -exist
  ipset flush $ipset
  local d ip
  for d in "$@"; do
    for ip in $(getent ahostsv4 "$d" 2>/dev/null | awk '{print $1}' | sort -u); do
      ipset add $ipset $ip -exist
    done
  done
}

# ensure_group — найти группу с этой proto+strategy, иначе создать (очередь+правила+
# демон). Возвращает "group_id<TAB>queue".
ensure_group() { # <proto> <strategy>
  local proto=$1 strat=$2 gid q
  gid=$(group_id "$proto" "$strat")
  local existing; existing=$(state_find_group_by_id "$gid")
  if [ -n "$existing" ]; then
    q=$(echo "$existing" | cut -f3)
    echo -e "$gid\t$q"
    return 0
  fi
  local ipset=brain_$gid
  ipset create $ipset hash:ip family inet -exist   # ipset ДОЛЖЕН существовать до svc_rules -A (--match-set)
  if [ -n "$strat" ]; then
    q=$(alloc_queue) || { echo "❌ группа $gid: не удалось выделить очередь" >&2; return 1; }
  else
    q=""   # accept-only (T57)
  fi
  svc_rules -A "$proto" $ipset "$q"
  [ -n "$q" ] && start_daemon "$gid" "$proto" "$q" $strat
  state_upsert_group "$gid" "$proto" "$strat" "$q" ""
  echo -e "$gid\t$q"
}

# do_zapret — добавить домен в группу с этой proto+strategy (создав её при
# необходимости). Тот же CLI-вызов, что и раньше (brain-worker.sh не меняется).
do_zapret() {
  local d=$1 proto=$2; shift 2; local strat="$*"
  local res gid q
  res=$(ensure_group "$proto" "$strat") || return 1
  gid=$(echo "$res" | cut -f1); q=$(echo "$res" | cut -f2)

  # если домен уже был в ДРУГОЙ группе — убрать его оттуда (без разрушения группы,
  # если в ней остались другие домены)
  local prev; prev=$(state_find_group_for_domain "$d")
  if [ -n "$prev" ] && [ "$(echo "$prev" | cut -f1)" != "$gid" ]; then
    _detach_domain_from_group "$d" "$(echo "$prev" | cut -f1)"
  fi

  python3 - "$STATE" "$gid" "$d" <<'PY'
import json,os,sys
f,gid,d=sys.argv[1],sys.argv[2],sys.argv[3]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if g["group_id"]==gid:
        if d not in g["domains"]: g["domains"].append(d)
        break
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
  local domains; domains=$(python3 -c "import json;print(' '.join(next(g['domains'] for g in json.load(open('$STATE')) if g['group_id']=='$gid')))")
  rebuild_group_ipset "brain_$gid" $domains
  if [ -n "$q" ]; then
    echo "✅ $d -> группа $gid ($proto, очередь $q, доменов: $(echo $domains | wc -w))"
  else
    echo "✅ $d -> группа $gid ($proto, accept-only, доменов: $(echo $domains | wc -w))"
  fi
}

# _detach_domain_from_group — убрать домен из группы БЕЗ ar_add/ar_del (внутренний
# помощник для do_zapret/do_remove/move). Если группа опустела — сносим её целиком.
_detach_domain_from_group() { # <domain> <group_id>
  local d=$1 gid=$2
  local info; info=$(state_find_group_by_id "$gid")
  [ -n "$info" ] || return 0
  local proto q; proto=$(echo "$info" | cut -f1); q=$(echo "$info" | cut -f3)
  python3 - "$STATE" "$gid" "$d" <<'PY'
import json,os,sys
f,gid,d=sys.argv[1],sys.argv[2],sys.argv[3]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if g["group_id"]==gid and d in g["domains"]:
        g["domains"].remove(d)
        break
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
  local remaining; remaining=$(python3 -c "import json;g=next((x for x in json.load(open('$STATE')) if x['group_id']=='$gid'),None);print(' '.join(g['domains']) if g else '')")
  if [ -z "$remaining" ]; then
    [ -n "$q" ] && stop_daemon "$gid"
    svc_rules -D "$proto" "brain_$gid" "$q"
    ipset destroy "brain_$gid" 2>/dev/null
    state_remove_group "$gid"
    echo "🗑 группа $gid опустела — снесена"
  else
    rebuild_group_ipset "brain_$gid" $remaining
  fi
}

# --- VPS-автообход (без изменений) ---
AR_JSON=/etc/gateway/autoroute.json; AR_IPSET=gw_autoroute
ar_add() {
  local d=$1
  python3 - "$d" <<'PY'
import json,os,sys,time,fcntl
d=sys.argv[1]; f="/etc/gateway/autoroute.json"
fh=open(f,"a+"); fcntl.flock(fh,fcntl.LOCK_EX); fh.seek(0)
try: data=json.load(fh)
except: data={"enabled":True,"detect":True,"route":True,"entries":[]}
ents=data.get("entries",[])
if not any((e.get("addr") if isinstance(e,dict) else e)==d for e in ents):
    ents.append({"addr":d,"added":time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime()),"source":"brain"})
    data["entries"]=ents
    fh.seek(0); fh.truncate(); json.dump(data,fh,ensure_ascii=False,indent=1)
PY
  ipset create $AR_IPSET hash:net family inet -exist
  for ip in $(getent ahostsv4 "$d" | awk '{print $1}' | sort -u); do ipset add $AR_IPSET $ip -exist; done
}
ar_del() {
  local d=$1
  python3 - "$d" <<'PY'
import json,os,sys,fcntl
d=sys.argv[1]; f="/etc/gateway/autoroute.json"
if not os.path.exists(f): sys.exit()
fh=open(f,"r+"); fcntl.flock(fh,fcntl.LOCK_EX)
try: data=json.load(fh)
except: sys.exit()
data["entries"]=[e for e in data.get("entries",[]) if (e.get("addr") if isinstance(e,dict) else e)!=d]
fh.seek(0); fh.truncate(); json.dump(data,fh,ensure_ascii=False,indent=1)
PY
  for ip in $(getent ahostsv4 "$d" | awk '{print $1}' | sort -u); do ipset del $AR_IPSET $ip 2>/dev/null; done
}

do_remove() {
  local d=$1
  local prev; prev=$(state_find_group_for_domain "$d")
  [ -n "$prev" ] || { echo "…$d не найден ни в одной группе"; return 0; }
  _detach_domain_from_group "$d" "$(echo "$prev" | cut -f1)"
  echo "🗑 удалено из группы: $d"
}

# move — перекинуть домен в СУЩЕСТВУЮЩУЮ группу (используется nightly/миграцией,
# когда для домена нашлась стратегия, совпадающая с уже существующей группой —
# без необходимости пересоздавать группу).
do_move() {
  local d=$1 gid=$2
  local target; target=$(state_find_group_by_id "$gid")
  [ -n "$target" ] || { echo "нет такой группы: $gid" >&2; return 1; }
  local prev; prev=$(state_find_group_for_domain "$d")
  if [ -n "$prev" ]; then
    [ "$(echo "$prev" | cut -f1)" = "$gid" ] && { echo "уже в группе $gid"; return 0; }
    _detach_domain_from_group "$d" "$(echo "$prev" | cut -f1)"
  fi
  python3 - "$STATE" "$gid" "$d" <<'PY'
import json,os,sys
f,gid,d=sys.argv[1],sys.argv[2],sys.argv[3]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if g["group_id"]==gid:
        if d not in g["domains"]: g["domains"].append(d)
        break
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
  local domains; domains=$(python3 -c "import json;print(' '.join(next(g['domains'] for g in json.load(open('$STATE')) if g['group_id']=='$gid')))")
  rebuild_group_ipset "brain_$gid" $domains
  echo "✅ $d -> группа $gid (перемещён)"
}

do_restore() {
  [ -f "$STATE" ] || { echo "нет состояния — нечего восстанавливать"; return; }
  python3 -c "
import json
for g in json.load(open('$STATE')):
    q=g.get('queue')
    print(g['group_id']+'\t'+g.get('proto','tcp')+'\t'+(str(q) if q is not None else '')+'\t'+g.get('strategy','')+'\t'+','.join(g['domains']))
" | while IFS=$'\t' read -r gid proto q strat domains_csv; do
    [ -n "$gid" ] || continue
    IFS=',' read -ra domains <<< "$domains_csv"
    rebuild_group_ipset "brain_$gid" "${domains[@]}"
    svc_rules -A "$proto" "brain_$gid" "$q"
    if [ -n "$q" ]; then
      start_daemon "$gid" "$proto" "$q" $strat
      echo "↻ восстановлена группа: $gid ($proto, очередь $q, доменов: ${#domains[@]})"
    else
      echo "↻ восстановлена группа: $gid ($proto, accept-only, доменов: ${#domains[@]})"
    fi
  done
}

case "${1:-}" in
  zapret) shift; d=$1; shift; proto=$1; shift; ar_del "$d"; do_zapret "$d" "$proto" "$@" ;;
  vps)    shift; do_remove "$1" >/dev/null 2>&1; ar_add "$1"; echo "🔵 vps: $1 в автообходе" ;;
  remove-entity) shift; do_remove "$1" ;;
  remove) shift; do_remove "$1"; ar_del "$1" ;;
  list)   cat "$STATE" 2>/dev/null || echo "[]" ;;
  groups) state_load ;;
  group-of) shift; state_find_group_for_domain "$1" ;;
  move)   shift; do_move "$1" "$2" ;;
  restore) do_restore ;;
  *) echo "usage: brain-apply.sh {zapret <d> <tcp|udp> <strat>|vps <d>|remove <d>|list|groups|group-of <d>|move <d> <gid>|restore}" >&2; exit 2 ;;
esac
