#!/usr/bin/env bash
# solve.sh v3 — ядро «мозга»: перебор ГОТОВЫХ пресетов через NETNS-фейк-клиента.
# Трафик netns форвардится+NAT через стенд как настоящий LAN-клиент, значит
# nfqws десинхронизирует его как боевой (локальный трафик стенда — не воспроизводит).
# Изоляция от живых qnum200/201: ACCEPT для netns-трафика в mangle POSTROUTING
# (после нашей очереди) → на боевые правила не попадает.
#
#   solve.sh <domain>
# Вывод (последняя строка): "ZAPRET<TAB><name><TAB><tcp-args>" | "VPS" | "DIRECT"
set -uo pipefail

DOMAIN="${1:?usage: solve.sh <domain>}"
ZAPRET="${ZAPRET:-/opt/zapret}"
NFQWS="$ZAPRET/nfq/nfqws"
FAKEDIR="${FAKEDIR:-$ZAPRET/files/fake}"
PRESETS="${PRESETS:-/root/strategies.json}"
WAN="${WAN:-enp2s0}"
NS=solvns
HOSTIP=10.99.99.1; NSIP=10.99.99.2; SUBNET=10.99.99.0/30
QNUM=59781; MARK=0x40000000; PORT=443

command -v jq >/dev/null || { echo "нужен jq" >&2; exit 2; }
[ -x "$NFQWS" ] || { echo "нет nfqws" >&2; exit 2; }
[ -f "$PRESETS" ] || { echo "нет пресетов" >&2; exit 2; }

IP=$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1{print $1}')
[ -n "$IP" ] || { echo "не резолвится: $DOMAIN" >&2; echo "VPS"; exit 0; }
echo "цель: $DOMAIN -> $IP"

NPID=
teardown() {
  [ -n "$NPID" ] && kill "$NPID" 2>/dev/null
  pkill -f "qnum=$QNUM" 2>/dev/null   # добить возможных сирот на тест-очереди
  ip netns del $NS 2>/dev/null
  iptables -t nat -D POSTROUTING -s $SUBNET -o $WAN -j MASQUERADE 2>/dev/null
  iptables -t mangle -D POSTROUTING -s $NSIP -p tcp -d "$IP" --dport $PORT -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM --queue-bypass 2>/dev/null
  iptables -t mangle -D POSTROUTING -s $NSIP -j ACCEPT 2>/dev/null
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
# наша тест-очередь (первые пакеты TLS к цели), потом ACCEPT (не пускать на боевые правила)
iptables -t mangle -I POSTROUTING 1 -s $NSIP -p tcp -d "$IP" --dport $PORT -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM --queue-bypass
iptables -t mangle -I POSTROUTING 2 -s $NSIP -j ACCEPT

nscurl() { ip netns exec $NS curl -s -o /dev/null -w '%{http_code}' --resolve "$DOMAIN:$PORT:$IP" --max-time 4 "https://$DOMAIN/" 2>/dev/null; }

BASE=$(nscurl); BASE=${BASE:-000}
echo "прямой доступ клиента (без обхода): HTTP=$BASE"
if [ "$BASE" != "000" ]; then echo "не заблокировано — обход не нужен"; echo "DIRECT"; exit 0; fi

N=$(jq 'length' "$PRESETS")
echo "пробую $N пресетов через netns-клиента..."
for i in $(seq 0 $((N-1))); do
  name=$(jq -r ".[$i].name" "$PRESETS")
  tcp=$(jq -r ".[$i].tcp // empty" "$PRESETS"); [ -n "$tcp" ] || continue
  tcp=${tcp//\$FAKE/$FAKEDIR}
  miss=0; for f in $(echo "$tcp" | grep -oE "$FAKEDIR/[^ ]+"); do [ -f "$f" ] || miss=1; done
  [ $miss -eq 0 ] || { echo "  [$i] $name — пропуск (нет fake)"; continue; }

  [ -n "$NPID" ] && kill "$NPID" 2>/dev/null; NPID=
  # без --daemon: $! = реальный процесс (убиваемо, без сирот); --filter-tcp — как боевой
  $NFQWS --qnum=$QNUM --dpi-desync-fwmark=$MARK --filter-tcp=$PORT $tcp >/dev/null 2>&1 &
  NPID=$!; sleep 1.2
  code=$(nscurl); code=${code:-000}
  kill "$NPID" 2>/dev/null; NPID=
  if [ "$code" != "000" ]; then
    echo "  [$i] $name — РАБОТАЕТ (HTTP=$code) ✓"
    printf 'ZAPRET\t%s\t%s\n' "$name" "$tcp"; exit 0
  fi
  echo "  [$i] $name — нет (HTTP=$code)"
done
echo "ни один пресет не пробил -> VPS"; echo "VPS"
