#!/usr/bin/env bash
# solve.sh v4 — ядро «мозга»: перебор ГОТОВЫХ пресетов через NETNS-фейк-клиента.
# Трафик netns форвардится+NAT через стенд как настоящий LAN-клиент, значит
# nfqws десинхронизирует его как боевой (локальный трафик стенда — не воспроизводит).
# Изоляция от живых qnum200/201/210+: ACCEPT для netns-трафика в mangle POSTROUTING
# (после нашей очереди) → на боевые правила не попадает.
#
#   solve.sh <domain> [source]
# source (T50) — сигнатура детектора (syn-timeout/rst-after-clienthello/quic-no-response/...).
# Определяет и протокол, и стратегию проверки (T57):
#   - "quic-no-response" → УЖЕ значит "клиент видит нет ответа на UDP/443 QUIC" — тестируем
#     UDP-пресеты (curl --http3-only). ВАЖНО: UDP/443 у нас САМИ дропаем глобально для
#     всех кроме Meta ("заставляет браузеры падать на TCP", см. zapret/zapret.sh) — для
#     НЕ-Meta доменов "no response" чаще всего просто наш же DROP, не внешний DPI-блок.
#     Раз юзер решил строить настоящий UDP-обход — пробуем ACCEPT+desync вместо DROP.
#   - "syn-timeout" — похоже на IP-уровневую TCP-блокировку — перебор TCP-пресетов её
#     не чинит, пропускаем и сразу отдаём VPS.
#   - всё остальное — обычный TCP-перебор.
# Вывод (последняя строка): "ZAPRET<TAB><proto>\t<name>\t<args>" | "VPS" | "DIRECT"
set -uo pipefail

DOMAIN="${1:?usage: solve.sh <domain> [source]}"
SOURCE="${2:-unknown}"
ZAPRET="${ZAPRET:-/opt/zapret}"
NFQWS="$ZAPRET/nfq/nfqws"
FAKEDIR="${FAKEDIR:-$ZAPRET/files/fake}"
GWDB="${GWDB:-/root/gateway-universal/scripts/gwdb.py}"
WAN="${WAN:-enp2s0}"
NS=solvns
HOSTIP=10.99.99.1; NSIP=10.99.99.2; SUBNET=10.99.99.0/30
QNUM_TCP=59781; QNUM_UDP=59782; MARK=0x40000000; PORT=443

[ -x "$NFQWS" ] || { echo "нет nfqws" >&2; exit 2; }
[ -f "$GWDB" ] || { echo "нет gwdb.py: $GWDB" >&2; exit 2; }

if [ "$SOURCE" = "quic-no-response" ]; then PROTO=udp; QNUM=$QNUM_UDP; else PROTO=tcp; QNUM=$QNUM_TCP; fi

IP=$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1{print $1}')
[ -n "$IP" ] || { echo "не резолвится: $DOMAIN" >&2; echo "VPS"; exit 0; }
echo "цель: $DOMAIN -> $IP (proto=$PROTO)"

NPID=
teardown() {
  [ -n "$NPID" ] && kill "$NPID" 2>/dev/null
  pkill -f "qnum=$QNUM_TCP" 2>/dev/null; pkill -f "qnum=$QNUM_UDP" 2>/dev/null   # сироты на обеих тест-очередях
  ip netns del $NS 2>/dev/null
  ip link del veth-s 2>/dev/null      # ГЛАВНОЕ: остаток veth ломает следующий netns (был флак!)
  iptables -t nat -D POSTROUTING -s $SUBNET -o $WAN -j MASQUERADE 2>/dev/null
  iptables -D FORWARD -s $NSIP -p udp --dport $PORT -j ACCEPT 2>/dev/null   # T57: обход глобального UDP/443 DROP на время теста
  iptables -t mangle -D POSTROUTING -s $NSIP -p tcp -d "$IP" --dport $PORT -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM_TCP --queue-bypass 2>/dev/null
  iptables -t mangle -D POSTROUTING -s $NSIP -p udp -d "$IP" --dport $PORT -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM_UDP --queue-bypass 2>/dev/null
  iptables -t mangle -D POSTROUTING -s $NSIP -j ACCEPT 2>/dev/null
  conntrack -D -d "$IP" 2>/dev/null >/dev/null  # сбросить conntrack цели (иначе connbytes «залипает»)
}
trap teardown EXIT
teardown 2>/dev/null   # снять остатки прошлого запуска

# --- netns-фейк-клиент ---
ip netns add $NS
ip link add veth-s type veth peer name veth-ns
ip link set veth-ns netns $NS
ip addr add $HOSTIP/30 dev veth-s; ip link set veth-s up
ip netns exec $NS ip addr add $NSIP/30 dev veth-ns
ip netns exec $NS ip link set veth-ns up
ip netns exec $NS ip link set lo up
ip netns exec $NS ip route add default via $HOSTIP
iptables -t nat -A POSTROUTING -s $SUBNET -o $WAN -j MASQUERADE
if [ "$PROTO" = "udp" ]; then
  # T57: UDP/443 у нас глобально DROP (FORWARD) кроме Meta — тестовому netns-трафику
  # нужен точечный обход, иначе даже нетронутый (не заблокированный внешне) домен
  # даст "нет ответа" из-за нашего же DROP.
  iptables -I FORWARD 1 -s $NSIP -p udp --dport $PORT -j ACCEPT
  iptables -t mangle -I POSTROUTING 1 -s $NSIP -p udp -d "$IP" --dport $PORT -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM --queue-bypass
else
  iptables -t mangle -I POSTROUTING 1 -s $NSIP -p tcp -d "$IP" --dport $PORT -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM --queue-bypass
fi
# ACCEPT ПОСЛЕ тест-очереди — не пускать на боевые правила (200/201/210+)
iptables -t mangle -I POSTROUTING 2 -s $NSIP -j ACCEPT

nscurl() { ip netns exec $NS curl -s -o /dev/null -w '%{http_code}' --resolve "$DOMAIN:$PORT:$IP" --max-time 6 "https://$DOMAIN/" 2>/dev/null; }
nscurl_udp() { ip netns exec $NS curl --http3-only -s -o /dev/null -w '%{http_code}' --resolve "$DOMAIN:$PORT:$IP" --max-time 6 "https://$DOMAIN/" 2>/dev/null; }

# wait_bound — ждать, пока nfqws реально ЗАБИНДИТ очередь QNUM (появится в
# /proc/net/netfilter/nfnetlink_queue). Иначе ClientHello/QUIC-Initial проскакивает
# по --queue-bypass без обхода и пресет ложно «фейлится» (источник флака).
wait_bound() {
  local i
  for i in $(seq 1 40); do
    grep -qE "^[[:space:]]*$QNUM[[:space:]]" /proc/net/netfilter/nfnetlink_queue 2>/dev/null && return 0
    sleep 0.05
  done
  return 1
}

if [ "$PROTO" = "udp" ]; then BASE=$(nscurl_udp); else BASE=$(nscurl); fi
BASE=${BASE:-000}
echo "прямой доступ клиента (без обхода): HTTP=$BASE"
if [ "$BASE" != "000" ]; then
  if [ "$PROTO" = "udp" ]; then
    # UDP/443 у нас САМИХ глобально DROP (кроме Meta) — тест уже идёт с точечным ACCEPT
    # (см. выше), и раз это единственное, что понадобилось — постоянная сущность нужна
    # ВСЁ РАВНО (иначе прод-трафик по-прежнему дропается), но БЕЗ nfqws/десинхронизации.
    echo "не заблокировано ВНЕШНЕ — но у нас самих глобальный DROP, нужен постоянный ACCEPT (без десинхронизации)"
    printf 'ZAPRET\tudp\taccept-only\t\n'; exit 0
  fi
  echo "не заблокировано — обход не нужен"; echo "DIRECT"; exit 0
fi

if [ "$SOURCE" = "syn-timeout" ]; then
  echo "source=syn-timeout — похоже на IP-блокировку, перебор пресетов пропущен"
  echo "VPS"; exit 0
fi

# try_tier <standard|custom> — перебор пресетов данного тира и текущего $PROTO (T51,
# протокол — T57). Источник — gwdb.py presets-list (TSV: id name proto args source
# trusted success_count); custom уже приходит отсортированным доверенные+успешные
# первыми (ORDER BY в gwdb.py). При успехе custom-пресета помечаем его trusted навсегда.
try_tier() {
  local tier=$1 pid name proto args psource trusted sc a miss f code
  while IFS=$'\t' read -r pid name proto args psource trusted sc; do
    [ -n "$args" ] || continue
    a=${args//\$FAKE/$FAKEDIR}
    miss=0; for f in $(echo "$a" | grep -oE "$FAKEDIR/[^ ]+"); do [ -f "$f" ] || miss=1; done
    [ $miss -eq 0 ] || { echo "  [$tier:$pid] $name — пропуск (нет fake)"; continue; }

    [ -n "$NPID" ] && kill "$NPID" 2>/dev/null; NPID=
    # без --daemon: $! = реальный процесс (убиваемо, без сирот); --filter-tcp/udp — как боевой
    $NFQWS --qnum=$QNUM --dpi-desync-fwmark=$MARK --filter-$PROTO=$PORT $a >/dev/null 2>&1 &
    NPID=$!
    wait_bound || { kill "$NPID" 2>/dev/null; NPID=; echo "  [$tier:$pid] $name — nfqws не забиндил, пропуск"; continue; }
    if [ "$PROTO" = "udp" ]; then code=$(nscurl_udp); else code=$(nscurl); fi
    code=${code:-000}
    kill "$NPID" 2>/dev/null; NPID=
    if [ "$code" != "000" ]; then
      echo "  [$tier:$pid] $name — РАБОТАЕТ (HTTP=$code) ✓"
      [ "$tier" = "custom" ] && python3 "$GWDB" preset-mark-success "$pid" >/dev/null 2>&1
      printf 'ZAPRET\t%s\t%s\t%s\n' "$PROTO" "$name" "$a"
      return 0
    fi
    echo "  [$tier:$pid] $name — нет (HTTP=$code)"
  done < <(python3 "$GWDB" presets-list --tier "$tier" --proto "$PROTO" 2>/dev/null)
  return 1
}

echo "пробую стандартные пресеты ($PROTO) через netns-клиента..."
try_tier standard && exit 0
echo "стандартные не пробили — пробую custom-пресеты (доверенные первыми)..."
try_tier custom && exit 0
echo "ни один пресет не пробил -> VPS"; echo "VPS"
