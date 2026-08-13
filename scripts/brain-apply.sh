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

# CDN_CIDR_HINTS (2026-08-13) — googlevideo.com сам по себе резолвится в свои
# несколько IP, но реальное видео льётся с ДИНАМИЧЕСКИХ поддоменов вида
# rrN---sn-xxxxx.googlevideo.com — Google назначает их индивидуально под каждую
# видеосессию, IP разные и непредсказуемые, никаким getent их заранее не
# поймать. Найдено живым разбором: "youtube.com грузится, видео не играет" —
# страница/миниатюры используют youtube.com/ytimg.com (фиксированные IP,
# обход уже работал), а сам поток шёл мимо ipset совсем. Фикс — вместо
# точечных IP добавлять в ipset группы известные CIDR-блоки Google CDN
# (публичные, стабильные годами), тогда любой rrN---sn-xxxxx.googlevideo.com
# попадёт под перехват независимо от того, в какой конкретно IP его резолвит
# Google в этот момент.
CDN_CIDR_HINTS_googlevideo_com="172.217.0.0/16 172.253.0.0/16 173.194.0.0/16 216.58.0.0/16 142.250.0.0/15 74.125.0.0/16 108.177.8.0/21 64.233.160.0/19 209.85.128.0/17 216.239.32.0/19"

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
  ipset create $ipset hash:net family inet -exist
  ipset flush $ipset
  [ $# -eq 0 ] && return 0
  # T-restore-perf (2026-08-09): раньше getent по одному домену за раз —
  # на большом числе сущностей после ребута (найдено живьём: 645 сущностей
  # ~8 минут) держало gateway-brain-worker в простое (тот стартует только
  # ПОСЛЕ restore, см. Before= в gateway-brain-restore.service — иначе начал
  # бы применять стратегии к ещё не пересозданным группам). DNS-резолв
  # per-domain независим и без побочных эффектов — безопасно распараллелить,
  # ipset add сериализуется самим ipset (не гонка). -P 16 — не CPU-bound
  # работа (сеть/DNS-латентность), число воркеров больше числа ядер — норм.
  printf '%s\n' "$@" | xargs -P 16 -I{} getent ahostsv4 {} 2>/dev/null \
    | awk '{print $1}' | sort -u \
    | while read -r ip; do ipset add $ipset $ip -exist; done
  # CDN_CIDR_HINTS — см. определение выше: для доменов с известными широкими
  # CDN-диапазонами добавляем их в ТОТ ЖЕ ipset группы поверх точечных
  # резолвленных IP (ipset теперь hash:net — держит и /32, и настоящие сети).
  local d hintvar hint
  for d in "$@"; do
    hintvar="CDN_CIDR_HINTS_$(san "$d")"
    hint="${!hintvar:-}"
    [ -n "$hint" ] || continue
    for cidr in $hint; do ipset add $ipset "$cidr" -exist; done
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
  ipset create $ipset hash:net family inet -exist   # ipset ДОЛЖЕН существовать до svc_rules -A (--match-set)
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

# --- ciadpi (byedpi) — T-ciadpi: второй десинхронизирующий движок, параллельно
# zapret/nfqws, полностью изолирован от него (свой state-файл, свой ipset-префикс,
# свой пул портов) — нулевой риск для существующих zapret-групп при ошибке здесь.
# Модель — та же группа-по-стратегии, что и zapret, но вместо NFQUEUE+nfqws —
# REDIRECT в локальный ciadpi-инстанс с -E (transparent, читает SO_ORIGINAL_DST
# как обычный REDIRECT-таргет, см. хелп ciadpi). Пока только TCP — ciadpi transparent
# режим не задокументирован для UDP, а глобальный UDP/443 DROP уже покрыт zapret-веткой.
CIADPI_BIN=${CIADPI_BIN:-/opt/byedpi/ciadpi}
CSTATE=/etc/gateway/brain-services-ciadpi.json
CPORT_BASE=15000; CPORT_POOL=500
GWDB=${GWDB:-/root/gateway-universal/scripts/gwdb.py}
CIADPI_AUTO_N=${CIADPI_AUTO_N:-3}   # лидер + до (N-1) fallback-групп через -A

cgroup_id() { echo -n "$1|$2" | md5sum | cut -c1-10 | sed 's/^/grpc_/'; }

# build_auto_chain <leader-args> — собрать команду ciadpi из ЛИДЕРА (та стратегия,
# что реально выиграла в solve.sh/применена вручную) + до CIADPI_AUTO_N-1 следующих
# по score стратегий из БД, соединённых через -At,r,s,n (torst,redirect,ssl_err,none
# — см. хелп ciadpi). ciadpi сам переключается между группами на лету по IP
# (свой кеш ~28ч) — закрывает вариативность CDN-edge (T-ciadpi: разные edge одного
# домена иногда требуют разных флагов) без ожидания ночной переоценки мозгом.
# ВАЖНО: в CSTATE/score-учёте участвует только ЛИДЕР — группа/score считается на
# него, независимо от того, какой конкретно -A-блок реально сработал внутри
# ciadpi (осознанный компромисс — см. обсуждение с пользователем, вариант 3).
build_auto_chain() {
  local leader="$1"
  local combined="$leader"
  local extra
  extra=$(python3 "$GWDB" strategies-list --proto tcp --engine ciadpi 2>/dev/null \
    | awk -F'\t' -v lead="$leader" '$4!=lead' \
    | sort -t$'\t' -k9,9 -rn \
    | head -n $((CIADPI_AUTO_N-1)) \
    | cut -f4)
  while IFS= read -r s; do
    [ -n "$s" ] || continue
    combined="$combined -At,r,s,n $s"
  done <<< "$extra"
  echo "$combined"
}

alloc_port() {
  local used p
  used=$(iptables -t nat -S PREROUTING 2>/dev/null | grep -oE 'to-ports [0-9]+' | awk '{print $2}')
  used="$used $(pgrep -a ciadpi 2>/dev/null | grep -oE -- '-p ?[0-9]+' | grep -oE '[0-9]+')"
  used="$used $(python3 -c "import json,os;f='$CSTATE';print(' '.join(str(x['port']) for x in (json.load(open(f)) if os.path.exists(f) else []) if x.get('port') is not None))" 2>/dev/null)"
  for p in $(seq $CPORT_BASE $((CPORT_BASE+CPORT_POOL))); do echo "$used" | tr ' ' '\n' | grep -qx "$p" || { echo "$p"; return 0; }; done
  echo "alloc_port: пул исчерпан ($CPORT_BASE..$((CPORT_BASE+CPORT_POOL)))" >&2
  return 1
}

# REDIRECT — терминальный таргет в nat-таблице: если наше правило раньше в
# PREROUTING, чем боевой REDIRECT xray (:12345) или zapret-RETURN, пакет уйдёт
# к ciadpi и дальше цепочка для НЕГО не обходится (в отличие от zapret, RETURN
# тут не нужен — REDIRECT сам всё решает).
csvc_rules() { # <op:-A|-D> <proto:tcp> <ipset> <port>
  local op=$1 proto=$2 ipset=$3 port=$4
  if [ "$proto" != "tcp" ]; then
    echo "ciadpi: только tcp поддерживается на данный момент" >&2
    return 1
  fi
  local rule=(-s $LAN -p tcp -m multiport --dports 80,443 -m addrtype ! --src-type LOCAL -m set --match-set $ipset dst -j REDIRECT --to-ports $port)
  if [ "$op" = "-A" ]; then
    iptables -t nat -C PREROUTING "${rule[@]}" 2>/dev/null || iptables -t nat -I PREROUTING 1 "${rule[@]}"
  else
    iptables -t nat -D PREROUTING "${rule[@]}" 2>/dev/null
  fi
}

start_ciadpi_daemon() { # <group_id> <port> <strategy-args...>
  local gid=$1 port=$2; shift 2; local strat="$*"
  local unit=brain-ciadpi-$gid
  systemctl reset-failed "$unit" 2>/dev/null
  # -i 0.0.0.0 (не 127.0.0.1!) — REDIRECT в nat PREROUTING подменяет dst-адрес на
  # адрес ВХОДЯЩЕГО интерфейса (LAN-адрес шлюза для реальных клиентов), не на
  # loopback (та же причина, по которой xray-dokodemo слушает *:12345, не 127.0.0.1).
  systemd-run --unit="$unit" --collect "$CIADPI_BIN" -i 0.0.0.0 -p "$port" -E $strat >/dev/null 2>&1
}
stop_ciadpi_daemon() { systemctl stop "brain-ciadpi-$1" 2>/dev/null; systemctl reset-failed "brain-ciadpi-$1" 2>/dev/null; }

cstate_load() { python3 -c "import json,os;f='$CSTATE';print(json.dumps(json.load(open(f)) if os.path.exists(f) else []))"; }

cstate_find_group_by_id() { # <group_id> -> "proto<TAB>strategy<TAB>port"
  python3 - "$CSTATE" "$1" <<'PY'
import json,os,sys
f,gid=sys.argv[1],sys.argv[2]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if g["group_id"]==gid:
        p=g.get("port")
        print(f"{g.get('proto','tcp')}\t{g.get('strategy','')}\t{p if p is not None else ''}")
        break
PY
}

cstate_find_group_for_domain() { # <domain> -> "group_id<TAB>proto<TAB>strategy<TAB>port" или пусто
  python3 - "$CSTATE" "$1" <<'PY'
import json,os,sys
f,d=sys.argv[1],sys.argv[2]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if d in g.get("domains",[]):
        p=g.get("port")
        print(f"{g['group_id']}\t{g.get('proto','tcp')}\t{g.get('strategy','')}\t{p if p is not None else ''}")
        break
PY
}

cstate_upsert_group() { # <group_id> <proto> <strategy> <port>
  python3 - "$CSTATE" "$1" "$2" "$3" "$4" <<'PY'
import json,os,sys,time
f,gid,proto,strat,praw=sys.argv[1:6]
port=int(praw) if praw else None
data=json.load(open(f)) if os.path.exists(f) else []
data=[g for g in data if g["group_id"]!=gid]
data.append({"group_id":gid,"proto":proto,"strategy":strat,"port":port,"domains":[],
             "last_active":time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime())})
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
}

cstate_remove_group() { python3 -c "import json,os;f='$CSTATE';d=[g for g in (json.load(open(f)) if os.path.exists(f) else []) if g['group_id']!='$1'];json.dump(d,open(f,'w'),ensure_ascii=False,indent=1)"; }

ensure_cgroup() { # <proto> <strategy> -> "group_id<TAB>port"
  local proto=$1 strat=$2 gid port
  gid=$(cgroup_id "$proto" "$strat")
  local existing; existing=$(cstate_find_group_by_id "$gid")
  if [ -n "$existing" ]; then
    port=$(echo "$existing" | cut -f3)
    echo -e "$gid\t$port"
    return 0
  fi
  local ipset=brainc_$gid
  ipset create $ipset hash:net family inet -exist
  port=$(alloc_port) || { echo "❌ ciadpi-группа $gid: не удалось выделить порт" >&2; return 1; }
  csvc_rules -A "$proto" $ipset "$port" || return 1
  local chain; chain=$(build_auto_chain "$strat")
  start_ciadpi_daemon "$gid" "$port" $chain
  cstate_upsert_group "$gid" "$proto" "$strat" "$port"
  echo -e "$gid\t$port"
}

do_ciadpi() { # <domain> <proto> <strategy-args...>
  local d=$1 proto=$2; shift 2; local strat="$*"
  local res gid port
  res=$(ensure_cgroup "$proto" "$strat") || return 1
  gid=$(echo "$res" | cut -f1); port=$(echo "$res" | cut -f2)

  local prev; prev=$(cstate_find_group_for_domain "$d")
  if [ -n "$prev" ] && [ "$(echo "$prev" | cut -f1)" != "$gid" ]; then
    _detach_domain_from_cgroup "$d" "$(echo "$prev" | cut -f1)"
  fi

  python3 - "$CSTATE" "$gid" "$d" <<'PY'
import json,os,sys
f,gid,d=sys.argv[1],sys.argv[2],sys.argv[3]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if g["group_id"]==gid:
        if d not in g["domains"]: g["domains"].append(d)
        break
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
  local domains; domains=$(python3 -c "import json;print(' '.join(next(g['domains'] for g in json.load(open('$CSTATE')) if g['group_id']=='$gid')))")
  rebuild_group_ipset "brainc_$gid" $domains
  echo "✅ $d -> ciadpi-группа $gid ($proto, порт $port, доменов: $(echo $domains | wc -w))"
}

_detach_domain_from_cgroup() { # <domain> <group_id>
  local d=$1 gid=$2
  local info; info=$(cstate_find_group_by_id "$gid")
  [ -n "$info" ] || return 0
  local proto port; proto=$(echo "$info" | cut -f1); port=$(echo "$info" | cut -f3)
  python3 - "$CSTATE" "$gid" "$d" <<'PY'
import json,os,sys
f,gid,d=sys.argv[1],sys.argv[2],sys.argv[3]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if g["group_id"]==gid and d in g["domains"]:
        g["domains"].remove(d)
        break
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
  local remaining; remaining=$(python3 -c "import json;g=next((x for x in json.load(open('$CSTATE')) if x['group_id']=='$gid'),None);print(' '.join(g['domains']) if g else '')")
  if [ -z "$remaining" ]; then
    stop_ciadpi_daemon "$gid"
    csvc_rules -D "$proto" "brainc_$gid" "$port"
    ipset destroy "brainc_$gid" 2>/dev/null
    cstate_remove_group "$gid"
    echo "🗑 ciadpi-группа $gid опустела — снесена"
  else
    rebuild_group_ipset "brainc_$gid" $remaining
  fi
}

do_remove_ciadpi() {
  local d=$1
  local prev; prev=$(cstate_find_group_for_domain "$d")
  [ -n "$prev" ] || { echo "…$d не найден ни в одной ciadpi-группе"; return 0; }
  _detach_domain_from_cgroup "$d" "$(echo "$prev" | cut -f1)"
  echo "🗑 удалено из ciadpi-группы: $d"
}

do_restore_ciadpi() {
  [ -f "$CSTATE" ] || return 0
  python3 -c "
import json
for g in json.load(open('$CSTATE')):
    p=g.get('port')
    print(g['group_id']+'\t'+g.get('proto','tcp')+'\t'+(str(p) if p is not None else '')+'\t'+g.get('strategy','')+'\t'+','.join(g['domains']))
" | while IFS=$'\t' read -r gid proto port strat domains_csv; do
    [ -n "$gid" ] || continue
    IFS=',' read -ra domains <<< "$domains_csv"
    rebuild_group_ipset "brainc_$gid" "${domains[@]}"
    csvc_rules -A "$proto" "brainc_$gid" "$port"
    local chain; chain=$(build_auto_chain "$strat")
    start_ciadpi_daemon "$gid" "$port" $chain
    echo "↻ восстановлена ciadpi-группа: $gid ($proto, порт $port, доменов: ${#domains[@]})"
  done
}

# --- zapret2 (bol-van/zapret2, nfqws2) — T-zapret2: третий движок, параллельно
# zapret1/ciadpi, полностью изолирован (свой state-файл, свой ipset-префикс, свой
# пул очередей 800-1099 — не пересекается с zapret1: 210-710). В отличие от ciadpi
# (REDIRECT) — тот же механизм, что zapret1: NFQUEUE + mangle POSTROUTING, просто
# другой бинарник (nfqws2, Lua-desync вместо --dpi-desync=...) — поэтому модель
# groups/queue/svc_rules почти буквально копия zapret1-веток выше, не ciadpi.
Z2DIR=${Z2DIR:-/opt/zapret2}; NFQWS2=$Z2DIR/nfq2/nfqws2
Z2LUA="--lua-init=@$Z2DIR/lua/zapret-lib.lua --lua-init=@$Z2DIR/lua/zapret-antidpi.lua --lua-init=@$Z2DIR/lua/zapret-auto.lua"
Z2STATE=/etc/gateway/brain-services-zapret2.json
Z2BASE=800; Z2POOL=300

z2group_id() { echo -n "z2|$1|$2" | md5sum | cut -c1-10 | sed 's/^/grpz2_/'; }

alloc_queue2() {
  local used q
  used=$(iptables -t mangle -S POSTROUTING 2>/dev/null | grep -oE 'queue-num [0-9]+' | awk '{print $2}')
  used="$used $(pgrep -a nfqws2 2>/dev/null | grep -oE 'qnum=[0-9]+' | cut -d= -f2)"
  used="$used $(python3 -c "import json,os;f='$Z2STATE';print(' '.join(str(x['queue']) for x in (json.load(open(f)) if os.path.exists(f) else []) if x.get('queue') is not None))" 2>/dev/null)"
  for q in $(seq $Z2BASE $((Z2BASE+Z2POOL))); do echo "$used" | tr ' ' '\n' | grep -qx "$q" || { echo "$q"; return 0; }; done
  echo "alloc_queue2: пул исчерпан ($Z2BASE..$((Z2BASE+Z2POOL)))" >&2
  return 1
}

# svc_rules2 — тот же RETURN-перед-xray-REDIRECT + NFQUEUE-в-POSTROUTING паттерн,
# что и zapret1's svc_rules (см. выше), но заведён отдельно (свой ipset, ничего
# общего в правилах с zapret1-группами) — проще держать функции раздельно, чем
# городить общий helper ради экономии 20 строк.
svc_rules2() { # <op:-A|-D> <proto:tcp|udp> <ipset> <qnum>
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
    iptables -t mangle -D POSTROUTING "${base[@]}" -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $q --queue-bypass 2>/dev/null
    iptables -t mangle -D POSTROUTING "${base[@]}" -j ACCEPT 2>/dev/null
  fi
}

start_daemon2() { # <group_id> <proto> <qnum> <strategy...>
  local gid=$1 proto=$2 q=$3; shift 3; local strat="$*"
  local unit=brain-nfqws2-$gid
  systemctl reset-failed "$unit" 2>/dev/null
  systemd-run --unit="$unit" --collect $NFQWS2 --qnum=$q --fwmark=$MARK $Z2LUA --filter-$proto=443 $strat >/dev/null 2>&1
}
stop_daemon2() { systemctl stop "brain-nfqws2-$1" 2>/dev/null; systemctl reset-failed "brain-nfqws2-$1" 2>/dev/null; }

z2state_load() { python3 -c "import json,os;f='$Z2STATE';print(json.dumps(json.load(open(f)) if os.path.exists(f) else []))"; }

z2state_find_group_by_id() { # <group_id> -> "proto<TAB>strategy<TAB>queue"
  python3 - "$Z2STATE" "$1" <<'PY'
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

z2state_find_group_for_domain() { # <domain> -> "group_id<TAB>proto<TAB>strategy<TAB>queue" или пусто
  python3 - "$Z2STATE" "$1" <<'PY'
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

z2state_upsert_group() { # <group_id> <proto> <strategy> <queue> <domains-csv>
  python3 - "$Z2STATE" "$1" "$2" "$3" "$4" "$5" <<'PY'
import json,os,sys,time
f,gid,proto,strat,qraw,domains_csv=sys.argv[1:7]
q=int(qraw) if qraw else None
domains=[d for d in domains_csv.split(",") if d]
data=json.load(open(f)) if os.path.exists(f) else []
data=[g for g in data if g["group_id"]!=gid]
data.append({"group_id":gid,"proto":proto,"strategy":strat,"queue":q,"domains":domains,
             "last_active":time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime())})
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
}

z2state_remove_group() { python3 -c "import json,os;f='$Z2STATE';d=[g for g in (json.load(open(f)) if os.path.exists(f) else []) if g['group_id']!='$1'];json.dump(d,open(f,'w'),ensure_ascii=False,indent=1)"; }

ensure_group2() { # <proto> <strategy> -> "group_id<TAB>queue"
  local proto=$1 strat=$2 gid q
  gid=$(z2group_id "$proto" "$strat")
  local existing; existing=$(z2state_find_group_by_id "$gid")
  if [ -n "$existing" ]; then
    q=$(echo "$existing" | cut -f3)
    echo -e "$gid\t$q"
    return 0
  fi
  local ipset=brainz2_$gid
  ipset create $ipset hash:net family inet -exist
  q=$(alloc_queue2) || { echo "❌ zapret2-группа $gid: не удалось выделить очередь" >&2; return 1; }
  svc_rules2 -A "$proto" $ipset "$q"
  start_daemon2 "$gid" "$proto" "$q" $strat
  z2state_upsert_group "$gid" "$proto" "$strat" "$q" ""
  echo -e "$gid\t$q"
}

do_zapret2() { # <domain> <proto> <strategy-args...>
  local d=$1 proto=$2; shift 2; local strat="$*"
  local res gid q
  res=$(ensure_group2 "$proto" "$strat") || return 1
  gid=$(echo "$res" | cut -f1); q=$(echo "$res" | cut -f2)

  local prev; prev=$(z2state_find_group_for_domain "$d")
  if [ -n "$prev" ] && [ "$(echo "$prev" | cut -f1)" != "$gid" ]; then
    _detach_domain_from_z2group "$d" "$(echo "$prev" | cut -f1)"
  fi

  python3 - "$Z2STATE" "$gid" "$d" <<'PY'
import json,os,sys
f,gid,d=sys.argv[1],sys.argv[2],sys.argv[3]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if g["group_id"]==gid:
        if d not in g["domains"]: g["domains"].append(d)
        break
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
  local domains; domains=$(python3 -c "import json;print(' '.join(next(g['domains'] for g in json.load(open('$Z2STATE')) if g['group_id']=='$gid')))")
  rebuild_group_ipset "brainz2_$gid" $domains
  echo "✅ $d -> zapret2-группа $gid ($proto, очередь $q, доменов: $(echo $domains | wc -w))"
}

_detach_domain_from_z2group() { # <domain> <group_id>
  local d=$1 gid=$2
  local info; info=$(z2state_find_group_by_id "$gid")
  [ -n "$info" ] || return 0
  local proto q; proto=$(echo "$info" | cut -f1); q=$(echo "$info" | cut -f3)
  python3 - "$Z2STATE" "$gid" "$d" <<'PY'
import json,os,sys
f,gid,d=sys.argv[1],sys.argv[2],sys.argv[3]
data=json.load(open(f)) if os.path.exists(f) else []
for g in data:
    if g["group_id"]==gid and d in g["domains"]:
        g["domains"].remove(d)
        break
json.dump(data,open(f,"w"),ensure_ascii=False,indent=1)
PY
  local remaining; remaining=$(python3 -c "import json;g=next((x for x in json.load(open('$Z2STATE')) if x['group_id']=='$gid'),None);print(' '.join(g['domains']) if g else '')")
  if [ -z "$remaining" ]; then
    stop_daemon2 "$gid"
    svc_rules2 -D "$proto" "brainz2_$gid" "$q"
    ipset destroy "brainz2_$gid" 2>/dev/null
    z2state_remove_group "$gid"
    echo "🗑 zapret2-группа $gid опустела — снесена"
  else
    rebuild_group_ipset "brainz2_$gid" $remaining
  fi
}

do_remove_zapret2() {
  local d=$1
  local prev; prev=$(z2state_find_group_for_domain "$d")
  [ -n "$prev" ] || { echo "…$d не найден ни в одной zapret2-группе"; return 0; }
  _detach_domain_from_z2group "$d" "$(echo "$prev" | cut -f1)"
  echo "🗑 удалено из zapret2-группы: $d"
}

do_restore_zapret2() {
  [ -f "$Z2STATE" ] || return 0
  python3 -c "
import json
for g in json.load(open('$Z2STATE')):
    q=g.get('queue')
    print(g['group_id']+'\t'+g.get('proto','tcp')+'\t'+(str(q) if q is not None else '')+'\t'+g.get('strategy','')+'\t'+','.join(g['domains']))
" | while IFS=$'\t' read -r gid proto q strat domains_csv; do
    [ -n "$gid" ] || continue
    IFS=',' read -ra domains <<< "$domains_csv"
    rebuild_group_ipset "brainz2_$gid" "${domains[@]}"
    svc_rules2 -A "$proto" "brainz2_$gid" "$q"
    start_daemon2 "$gid" "$proto" "$q" $strat
    echo "↻ восстановлена zapret2-группа: $gid ($proto, очередь $q, доменов: ${#domains[@]})"
  done
}

# --- VPS-автообход (без изменений) ---
AR_JSON=/etc/gateway/autoroute.json; AR_IPSET=gw_autoroute
GWCFG=${GWCFG:-/root/gateway-universal/config.env}

VPS_MODE_FILE=${VPS_MODE_FILE:-/etc/gateway/vps-mode.conf}

# has_vps — есть ли вообще настроенный VPS-туннель И включён ли режим VPS+zapret
# (не "только zapret" — install.sh спросил при установке; UI может переключить
# и после, gateway-ui/vpsmode.go). Без этой проверки ar_add создавал бы
# автообход на несуществующий/выключенный xray-туннель — трафик просто
# зависал бы вместо явного "не пробилось, остаётся заблокирован".
has_vps() {
  [ -f "$GWCFG" ] && grep -q '^VPS_ADDR=.\+' "$GWCFG" 2>/dev/null || return 1
  [ "$(cat "$VPS_MODE_FILE" 2>/dev/null)" != "off" ]
}

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
  zapret) shift; d=$1; shift; proto=$1; shift; ar_del "$d"; do_remove_ciadpi "$d" >/dev/null 2>&1; do_remove_zapret2 "$d" >/dev/null 2>&1; do_zapret "$d" "$proto" "$@" ;;
  ciadpi) shift; d=$1; shift; proto=$1; shift; ar_del "$d"; do_remove "$d" >/dev/null 2>&1; do_remove_zapret2 "$d" >/dev/null 2>&1; do_ciadpi "$d" "$proto" "$@" ;;
  zapret2) shift; d=$1; shift; proto=$1; shift; ar_del "$d"; do_remove "$d" >/dev/null 2>&1; do_remove_ciadpi "$d" >/dev/null 2>&1; do_zapret2 "$d" "$proto" "$@" ;;
  vps)    shift; do_remove "$1" >/dev/null 2>&1; do_remove_ciadpi "$1" >/dev/null 2>&1; do_remove_zapret2 "$1" >/dev/null 2>&1
          if has_vps; then ar_add "$1"; echo "🔵 vps: $1 в автообходе"
          else echo "⚪ $1: ни одна стратегия не пробила, VPS не настроен — остаётся заблокирован"; fi ;;
  remove-entity) shift; do_remove "$1" ;;
  remove) shift; do_remove "$1"; do_remove_ciadpi "$1"; do_remove_zapret2 "$1"; ar_del "$1" ;;
  list)   cat "$STATE" 2>/dev/null || echo "[]" ;;
  list-ciadpi) cat "$CSTATE" 2>/dev/null || echo "[]" ;;
  list-zapret2) cat "$Z2STATE" 2>/dev/null || echo "[]" ;;
  cgroup-of) shift; cstate_find_group_for_domain "$1" ;;
  z2group-of) shift; z2state_find_group_for_domain "$1" ;;
  groups) state_load ;;
  group-of) shift; state_find_group_for_domain "$1" ;;
  move)   shift; do_move "$1" "$2" ;;
  # T-restore-perf (2026-08-09): три движка полностью изолированы (свои
  # state-файлы, свои ipset-префиксы brain_/brainc_/brainz2_, непересекающиеся
  # диапазоны очередей — см. комментарий у do_restore_zapret2 выше) — раньше
  # шли строго последовательно, хотя ничем друг друга не блокируют. Теперь
  # параллельно, wait ждёт все три перед выходом (иначе restore.service
  # завершился бы раньше, чем реально всё восстановлено).
  restore) do_restore & do_restore_ciadpi & do_restore_zapret2 & wait ;;
  restore-ciadpi) do_restore_ciadpi ;;
  restore-zapret2) do_restore_zapret2 ;;
  *) echo "usage: brain-apply.sh {zapret <d> <tcp|udp> <strat>|ciadpi <d> <tcp> <strat>|zapret2 <d> <tcp|udp> <strat>|vps <d>|remove <d>|list|list-ciadpi|list-zapret2|groups|group-of <d>|move <d> <gid>|restore|restore-ciadpi|restore-zapret2}" >&2; exit 2 ;;
esac
