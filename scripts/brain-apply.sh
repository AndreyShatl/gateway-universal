#!/usr/bin/env bash
# brain-apply.sh — применить решение «мозга» как СУЩНОСТЬ «очередь = сервис».
# zapret: свой ipset + iptables (NFQUEUE + ACCEPT, изоляция от боевых 200/201) +
#         nfqws на своей очереди (>=210) со своей стратегией.
# vps:    в автообход (через тот же ipset-механизм autoroute).
#
#   brain-apply.sh zapret  <domain> <strategy-args...>
#   brain-apply.sh vps     <domain>
#   brain-apply.sh remove  <domain>
#   brain-apply.sh list
#   brain-apply.sh restore                 # пересоздать всё (после ребута)
set -uo pipefail

STATE=/etc/gateway/brain-services.json
ZAPRET=${ZAPRET:-/opt/zapret}; NFQWS=$ZAPRET/nfq/nfqws; FAKEDIR=$ZAPRET/files/fake
LAN=192.168.0.0/16; MARK=0x40000000; QBASE=210

san() { echo "$1" | tr -c 'a-z0-9' '_' | sed 's/_*$//'; }
jqget() { python3 -c "import json,sys;d=json.load(open('$STATE')) if __import__('os').path.exists('$STATE') else [];print($1)" 2>/dev/null; }

alloc_queue() {
  local used q
  # занятые очереди: из iptables И из состояния (иначе коллизии при быстром подряд)
  used=$(iptables -t mangle -S POSTROUTING 2>/dev/null | grep -oE 'queue-num [0-9]+' | awk '{print $2}')
  used="$used $(python3 -c "import json,os;f='$STATE';print(' '.join(str(x['queue']) for x in (json.load(open(f)) if os.path.exists(f) else [])))" 2>/dev/null)"
  for q in $(seq $QBASE $((QBASE+50))); do echo "$used" | tr ' ' '\n' | grep -qx "$q" || { echo "$q"; return; }; done
}

# правила сервиса:
#  1) nat PREROUTING RETURN — обойти xray-tproxy (:12345), чтобы трафик пошёл
#     прямым форвардом на nfqws сущности (иначе xray уводит его как LOCAL).
#  2) mangle POSTROUTING NFQUEUE — десинхрон первых пакетов на своей очереди.
#  3) mangle POSTROUTING ACCEPT — не пускать на боевые 200/201 (изоляция).
svc_rules() { # <op:-A|-D> <ipset> <qnum>
  local op=$1 ipset=$2 q=$3
  local nat_ret=(-s $LAN -p tcp -m set --match-set $ipset dst -j RETURN)
  local base=(-s $LAN -p tcp -m multiport --dports 80,443 -m addrtype ! --src-type LOCAL -m set --match-set $ipset dst)
  if [ "$op" = "-A" ]; then
    iptables -t nat -C PREROUTING "${nat_ret[@]}" 2>/dev/null || iptables -t nat -I PREROUTING 1 "${nat_ret[@]}"
    iptables -t mangle -C POSTROUTING "${base[@]}" -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $q --queue-bypass 2>/dev/null || \
    iptables -t mangle -I POSTROUTING 1 "${base[@]}" -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $q --queue-bypass
    iptables -t mangle -C POSTROUTING "${base[@]}" -j ACCEPT 2>/dev/null || iptables -t mangle -I POSTROUTING 2 "${base[@]}" -j ACCEPT
  else
    iptables -t nat -D PREROUTING "${nat_ret[@]}" 2>/dev/null
    iptables -t mangle -D POSTROUTING "${base[@]}" -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $q --queue-bypass 2>/dev/null
    iptables -t mangle -D POSTROUTING "${base[@]}" -j ACCEPT 2>/dev/null
  fi
}

start_daemon() { # <domain> <qnum> <strategy...>
  local d=$1 q=$2; shift 2; local strat="$*"; strat=${strat//\$FAKE/$FAKEDIR}
  local unit=brain-nfqws-$(san "$d")
  systemctl reset-failed "$unit" 2>/dev/null
  # без --daemon: systemd держит процесс (реальный PID, чистое вкл/выкл)
  systemd-run --unit="$unit" --collect $NFQWS --qnum=$q --dpi-desync-fwmark=$MARK --filter-tcp=443 $strat >/dev/null 2>&1
}

state_put() { # <domain> <mode> <qnum> <strategy...>
  local d=$1 mode=$2 q=$3; shift 3; local strat="$*"
  python3 - "$d" "$mode" "$q" "$strat" <<'PY'
import json,os,sys
d,mode,q,strat=sys.argv[1],sys.argv[2],int(sys.argv[3]),sys.argv[4]
f="/etc/gateway/brain-services.json"
data=json.load(open(f)) if os.path.exists(f) else []
data=[x for x in data if x.get("domain")!=d]
data.append({"domain":d,"mode":mode,"queue":q,"strategy":strat})
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
}
state_del() { python3 -c "import json,os;f='$STATE';d=[x for x in (json.load(open(f)) if os.path.exists(f) else []) if x.get('domain')!='$1'];json.dump(d,open(f,'w'),ensure_ascii=False,indent=1)"; }

do_zapret() {
  local d=$1; shift; local strat="$*"
  local ipset=brain_$(san "$d") q; q=$(alloc_queue)
  ipset create $ipset hash:ip family inet -exist
  ipset flush $ipset
  for ip in $(getent ahostsv4 "$d" | awk '{print $1}' | sort -u); do ipset add $ipset $ip -exist; done
  svc_rules -A $ipset $q
  start_daemon "$d" $q $strat
  state_put "$d" zapret $q "$strat"
  echo "✅ zapret-сущность: $d -> очередь $q, ipset $ipset"
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
  local q; q=$(python3 -c "import json,os;f='$STATE';print(next((x['queue'] for x in (json.load(open(f)) if os.path.exists(f) else []) if x.get('domain')=='$d'),''))" 2>/dev/null)
  systemctl stop brain-nfqws-$(san "$d") 2>/dev/null; systemctl reset-failed brain-nfqws-$(san "$d") 2>/dev/null
  [ -n "$q" ] && svc_rules -D $ipset "$q"
  ipset destroy $ipset 2>/dev/null
  state_del "$d"
  echo "🗑 удалено: $d"
}

case "${1:-}" in
  zapret) shift; d=$1; shift; ar_del "$d"; do_zapret "$d" "$@" ;;  # zapret => убрать из VPS
  vps)    shift; ar_add "$1"; echo "🔵 vps: $1 добавлен в автообход" ;;
  remove) shift; do_remove "$1"; ar_del "$1" ;;
  list)   cat "$STATE" 2>/dev/null || echo "[]" ;;
  restore)
    python3 -c "import json,os;f='$STATE';[print(x['domain'],x['queue'],x['strategy']) for x in (json.load(open(f)) if os.path.exists(f) else [])]" 2>/dev/null | \
    while read -r d q s; do :; done
    echo "restore: пересоздание — TODO (systemd-run не переживает ребут; нужен boot-хук)" ;;
  *) echo "usage: brain-apply.sh {zapret <d> <strat>|vps <d>|remove <d>|list|restore}" >&2; exit 2 ;;
esac
