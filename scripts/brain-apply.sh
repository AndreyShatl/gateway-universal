#!/usr/bin/env bash
# brain-apply.sh — применить решение «мозга» как СУЩНОСТЬ «очередь = сервис».
# zapret: свой ipset + iptables (NFQUEUE + ACCEPT, изоляция от боевых 200/201) +
#         nfqws на своей очереди (>=210) со своей стратегией. TCP и UDP (T57) —
#         РАЗНАЯ модель обхода (см. svc_rules).
# vps:    в автообход (через тот же ipset-механизм autoroute).
#
#   brain-apply.sh zapret  <domain> <tcp|udp> <strategy-args...>
#   brain-apply.sh vps     <domain>
#   brain-apply.sh remove  <domain>
#   brain-apply.sh list
#   brain-apply.sh restore                 # пересоздать всё (после ребута)
set -uo pipefail

STATE=/etc/gateway/brain-services.json
ZAPRET=${ZAPRET:-/opt/zapret}; NFQWS=$ZAPRET/nfq/nfqws; FAKEDIR=$ZAPRET/files/fake
LAN=192.168.0.0/16; MARK=0x40000000; QBASE=210; QPOOL=500

san() { echo "$1" | tr -c 'a-z0-9' '_' | sed 's/_*$//'; }
jqget() { python3 -c "import json,sys;d=json.load(open('$STATE')) if __import__('os').path.exists('$STATE') else [];print($1)" 2>/dev/null; }

# alloc_queue — T52: пул расширен 51->500 слотов. При исчерпании (не должно
# случаться при QPOOL=500, но раньше молча ломало iptables пустым $q) падает
# явно в stderr, ничего не печатает в stdout — вызывающий обязан проверить exit.
alloc_queue() {
  local used q
  # занятые очереди: из iptables И из состояния (иначе коллизии при быстром подряд)
  used=$(iptables -t mangle -S POSTROUTING 2>/dev/null | grep -oE 'queue-num [0-9]+' | awk '{print $2}')
  used="$used $(python3 -c "import json,os;f='$STATE';print(' '.join(str(x['queue']) for x in (json.load(open(f)) if os.path.exists(f) else [])))" 2>/dev/null)"
  for q in $(seq $QBASE $((QBASE+QPOOL))); do echo "$used" | tr ' ' '\n' | grep -qx "$q" || { echo "$q"; return 0; }; done
  echo "alloc_queue: пул исчерпан ($QBASE..$((QBASE+QPOOL)))" >&2
  return 1
}

# правила сущности — модель зависит от протокола (T57):
#  TCP: nat PREROUTING RETURN обходит xray-redirect :12345 (иначе весь TCP 80/443
#       уходит в xray tproxy раньше, чем дойдёт до нашей сущности).
#  UDP: у UDP/443 нет глобального xray-redirect (только gw_autoroute ipset через
#       TPROXY) — зато у нас САМИХ глобальный DROP на UDP/443 кроме Meta-подсетей
#       (zapret/zapret.sh, "заставляет браузеры падать на TCP"). Сущности вместо
#       RETURN нужен ACCEPT в mangle PREROUTING (мимо возможных других mangle-правил,
#       как у Meta) + ACCEPT в filter FORWARD ПЕРЕД глобальным DROP (позиция 1).
# Общее для обоих: mangle POSTROUTING NFQUEUE (десинхрон) + ACCEPT после (изоляция
# от боевых 200/201/других сущностей) — ПРОПУСКАЕТСЯ, если q="" (accept-only, T57:
# UDP-домен, которому хватает самого ACCEPT без десинхронизации — nfqws не нужен).
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
    [ -n "$q" ] || return 0   # accept-only: без NFQUEUE/десинхрона
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

start_daemon() { # <domain> <proto> <qnum> <strategy...>
  local d=$1 proto=$2 q=$3; shift 3; local strat="$*"; strat=${strat//\$FAKE/$FAKEDIR}
  local unit=brain-nfqws-$(san "$d")
  systemctl reset-failed "$unit" 2>/dev/null
  # без --daemon: systemd держит процесс (реальный PID, чистое вкл/выкл)
  systemd-run --unit="$unit" --collect $NFQWS --qnum=$q --dpi-desync-fwmark=$MARK --filter-$proto=443 $strat >/dev/null 2>&1
}

state_put() { # <domain> <mode> <proto> <qnum> <strategy...>
  local d=$1 mode=$2 proto=$3 q=$4; shift 4; local strat="$*"
  python3 - "$d" "$mode" "$proto" "$q" "$strat" <<'PY'
import json,os,sys
d,mode,proto,qraw,strat=sys.argv[1],sys.argv[2],sys.argv[3],sys.argv[4],sys.argv[5]
q=int(qraw) if qraw else None   # accept-only (T57): без очереди/nfqws
f="/etc/gateway/brain-services.json"
data=json.load(open(f)) if os.path.exists(f) else []
data=[x for x in data if x.get("domain")!=d]
data.append({"domain":d,"mode":mode,"proto":proto,"queue":q,"strategy":strat})
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
}
state_del() { python3 -c "import json,os;f='$STATE';d=[x for x in (json.load(open(f)) if os.path.exists(f) else []) if x.get('domain')!='$1'];json.dump(d,open(f,'w'),ensure_ascii=False,indent=1)"; }
state_get_proto() { python3 -c "import json,os;f='$STATE';print(next((x.get('proto','tcp') for x in (json.load(open(f)) if os.path.exists(f) else []) if x.get('domain')=='$1'),'tcp'))" 2>/dev/null; }

do_zapret() {
  local d=$1 proto=$2; shift 2; local strat="$*"
  local ipset=brain_$(san "$d") q=""
  ipset create $ipset hash:ip family inet -exist
  ipset flush $ipset
  for ip in $(getent ahostsv4 "$d" | awk '{print $1}' | sort -u); do ipset add $ipset $ip -exist; done
  if [ -n "$strat" ]; then
    q=$(alloc_queue) || { echo "❌ $d: не удалось выделить очередь (пул исчерпан)" >&2; return 1; }
    svc_rules -A "$proto" $ipset $q
    start_daemon "$d" "$proto" $q $strat
    echo "✅ zapret-сущность: $d ($proto) -> очередь $q, ipset $ipset"
  else
    # accept-only (T57): UDP/443 у нас глобально DROP, домену хватает ACCEPT без
    # десинхронизации — nfqws/очередь не нужны, только правила PREROUTING+FORWARD.
    svc_rules -A "$proto" $ipset ""
    echo "✅ accept-only сущность: $d ($proto), ipset $ipset (без десинхронизации)"
  fi
  state_put "$d" zapret "$proto" "$q" "$strat"
}

# --- управление VPS-автообходом (gw_autoroute + autoroute.json) ---
AR_JSON=/etc/gateway/autoroute.json; AR_IPSET=gw_autoroute
ar_add() { # <domain> — добавить в VPS
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
ar_del() { # <domain> — убрать из VPS
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
  local ipset=brain_$(san "$d")
  local q proto
  q=$(python3 -c "import json,os;f='$STATE';v=next((x['queue'] for x in (json.load(open(f)) if os.path.exists(f) else []) if x.get('domain')=='$d'),'');print(v if v is not None else '')" 2>/dev/null)
  proto=$(state_get_proto "$d")
  systemctl stop brain-nfqws-$(san "$d") 2>/dev/null; systemctl reset-failed brain-nfqws-$(san "$d") 2>/dev/null
  # ВСЕГДА зовём svc_rules -D (не только когда есть очередь) — accept-only (T57)
  # сущностям тоже нужно снять PREROUTING/FORWARD ACCEPT, даже без NFQUEUE/демона.
  svc_rules -D "$proto" $ipset "$q"
  ipset destroy $ipset 2>/dev/null
  state_del "$d"
  echo "🗑 удалено: $d"
}

# do_restore — пересоздать все сущности из состояния (после ребута): ipset(resolve)
# + правила (proto-специфичные, T57) + демон. Наблюдаемые IP клиента само-восстановит
# детектор при заходе. Старые записи без "proto" в state — считаем tcp (обратная совместимость).
do_restore() {
  [ -f "$STATE" ] || { echo "нет состояния — нечего восстанавливать"; return; }
  python3 -c "import json;print('\n'.join(x['domain']+'\t'+x.get('proto','tcp')+'\t'+(str(x['queue']) if x.get('queue') is not None else '')+'\t'+x['strategy'] for x in json.load(open('$STATE'))))" 2>/dev/null | \
  while IFS=$'\t' read -r d proto q strat; do
    [ -n "$d" ] || continue
    local ipset=brain_$(san "$d")
    ipset create $ipset hash:ip family inet -exist
    for ip in $(getent ahostsv4 "$d" | awk '{print $1}' | sort -u); do ipset add $ipset $ip -exist; done
    svc_rules -A "$proto" $ipset "$q"
    if [ -n "$q" ]; then
      start_daemon "$d" "$proto" "$q" $strat
      echo "↻ восстановлен: $d ($proto, очередь $q)"
    else
      echo "↻ восстановлен: $d ($proto, accept-only)"
    fi
  done
}

case "${1:-}" in
  zapret) shift; d=$1; shift; proto=$1; shift; ar_del "$d"; do_zapret "$d" "$proto" "$@" ;;  # zapret => убрать из VPS
  vps)    shift; do_remove "$1" >/dev/null 2>&1; ar_add "$1"; echo "🔵 vps: $1 в автообходе" ;; # vps => убрать сущность
  remove-entity) shift; do_remove "$1" ;;  # только сущность, не трогать VPS
  remove) shift; do_remove "$1"; ar_del "$1" ;;
  list)   cat "$STATE" 2>/dev/null || echo "[]" ;;
  restore) do_restore ;;
  *) echo "usage: brain-apply.sh {zapret <d> <tcp|udp> <strat>|vps <d>|remove <d>|list|restore}" >&2; exit 2 ;;
esac
