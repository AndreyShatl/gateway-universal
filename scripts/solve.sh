#!/usr/bin/env bash
# solve.sh — ядро «мозга»: для домена ПЕРЕБРАТЬ ГОТОВЫЕ ПРЕСЕТЫ (не полный
# blockcheck!). На каждый пресет: поднять nfqws на изолированной очереди только
# для этой цели, проверить curl. Первый рабочий → ZAPRET. Ни один → VPS.
# Быстро: ~секунды на пресет, ~минута на цель.
#
#   solve.sh <domain>
# Вывод (последняя строка): "ZAPRET<TAB><name><TAB><tcp-args>"  или  "VPS"
set -uo pipefail

DOMAIN="${1:?usage: solve.sh <domain>}"
ZAPRET="${ZAPRET:-/opt/zapret}"
NFQWS="$ZAPRET/nfq/nfqws"
FAKEDIR="${FAKEDIR:-$ZAPRET/files/fake}"
PRESETS="${PRESETS:-/root/strategies.json}"
QNUM=59781
MARK=0x40000000
PORT=443

command -v jq >/dev/null || { echo "нужен jq" >&2; exit 2; }
[ -x "$NFQWS" ] || { echo "нет nfqws: $NFQWS" >&2; exit 2; }
[ -f "$PRESETS" ] || { echo "нет пресетов: $PRESETS" >&2; exit 2; }

IP=$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1{print $1}')
[ -n "$IP" ] || { echo "домен не резолвится: $DOMAIN" >&2; echo "VPS"; exit 0; }
echo "цель: $DOMAIN -> $IP"

# правило: исходящий TLS к цели (первые пакеты) в очередь, кроме пере-инжектнутых nfqws (по mark)
RULE=(OUTPUT -p tcp -d "$IP" --dport $PORT -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM --queue-bypass)
NPID=
kill_nfqws() { [ -n "$NPID" ] && kill "$NPID" 2>/dev/null; NPID=; }
cleanup() { kill_nfqws; while iptables -t mangle -D "${RULE[@]}" 2>/dev/null; do :; done; }
trap cleanup EXIT
cleanup           # снять возможные остатки прошлого запуска
iptables -t mangle -A "${RULE[@]}"

# базовая проверка (без обхода): реально ли блок? (без nfqws)
BASE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 "https://$DOMAIN/" 2>/dev/null); BASE=${BASE:-000}
echo "прямой доступ (без обхода): HTTP=$BASE"
if [ "$BASE" != "000" ]; then
  echo "не заблокировано напрямую — обход не нужен"; echo "DIRECT"; exit 0
fi

N=$(jq 'length' "$PRESETS")
echo "пробую $N пресетов..."
for i in $(seq 0 $((N-1))); do
  name=$(jq -r ".[$i].name" "$PRESETS")
  tcp=$(jq -r ".[$i].tcp // empty" "$PRESETS")
  [ -n "$tcp" ] || continue
  tcp=${tcp//\$FAKE/$FAKEDIR}          # подставить путь к fake-файлам
  # пропустить пресет, если ссылается на отсутствующий fake-bin
  miss=0; for f in $(echo "$tcp" | grep -oE "$FAKEDIR/[^ ]+"); do [ -f "$f" ] || miss=1; done
  [ $miss -eq 0 ] || { echo "  [$i] $name — пропуск (нет fake-файла)"; continue; }

  kill_nfqws
  $NFQWS --daemon --qnum=$QNUM --dpi-desync-fwmark=$MARK $tcp >/dev/null 2>&1 &
  NPID=$!
  sleep 0.5
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 "https://$DOMAIN/" 2>/dev/null); code=${code:-000}
  kill_nfqws
  if [ "$code" != "000" ]; then
    echo "  [$i] $name — РАБОТАЕТ (HTTP=$code) ✓"
    printf 'ZAPRET\t%s\t%s\n' "$name" "$tcp"
    exit 0
  fi
  echo "  [$i] $name — нет (HTTP=$code)"
done
echo "ни один пресет не пробил -> VPS"
echo "VPS"
