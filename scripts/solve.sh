#!/usr/bin/env bash
# solve.sh v5 — ядро «мозга»: перебор ГОТОВЫХ пресетов через NETNS-фейк-клиента.
# Трафик netns форвардится+NAT через стенд как настоящий LAN-клиент, значит
# nfqws десинхронизирует его как боевой (локальный трафик стенда — не воспроизводит).
# Изоляция от живых qnum200/201/210+: ACCEPT для netns-трафика в mangle POSTROUTING
# (после нашей очереди) → на боевые правила не попадает.
#
#   solve.sh <domain> [source]                    — полный перебор (как раньше)
#   solve.sh --test-args <domain> <proto> <args>  — ПРОВЕРИТЬ ОДНУ конкретную
#     стратегию (T-consolidate): нужно для группировки доменов по общей стратегии —
#     "сработает ли для ЭТОГО домена стратегия, уже найденная для ДРУГОГО" — без
#     полного перебора тиров. Вывод последней строкой: "OK <code>" | "FAIL <code>".
#
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
# Вывод (последняя строка, обычный режим): "ZAPRET<TAB><proto>\t<name>\t<args>" | "VPS" | "DIRECT"
set -uo pipefail

ZAPRET="${ZAPRET:-/opt/zapret}"
NFQWS="$ZAPRET/nfq/nfqws"
FAKEDIR="${FAKEDIR:-$ZAPRET/files/fake}"
GWDB="${GWDB:-/root/gateway-universal/scripts/gwdb.py}"
WAN="${WAN:-enp2s0}"
NS=solvns
HOSTIP=10.99.99.1; NSIP=10.99.99.2; SUBNET=10.99.99.0/30
QNUM_TCP=59781; QNUM_UDP=59782; MARK=0x40000000; PORT=443

# --- zapret2 (bol-van/zapret2, nfqws2) — T-zapret2: третий движок, свои тестовые
# очереди (59786/59787 — не пересекаются ни с zapret1 (59781/82), ни с ciadpi-
# тест-портом 19999 выше). Тот же netns-фейк-клиент, что и zapret1, просто другой
# бинарник (Lua-desync вместо --dpi-desync).
Z2DIR="${Z2DIR:-/opt/zapret2}"
# путь "nfq2/" (не "nfq/") — реальная раскладка сборки zapret2, см. brain-apply.sh
# NFQWS2. Найдено 2026-08-12 при добавлении UDP: с опечаткой ($Z2DIR/nfq/...,
# каталога никогда не существовало) весь ПЕРЕБОР zapret2-стратегий через solve.sh
# был сломан с самого добавления движка — nfqws2 не мог даже запуститься, каждая
# проба молча падала на "не забиндил". Прикладной слой (brain-apply.sh, уже
# применённые группы) путь всегда имел верный — не задет.
NFQWS2="$Z2DIR/nfq2/nfqws2"
Z2LUA="--lua-init=@$Z2DIR/lua/zapret-lib.lua --lua-init=@$Z2DIR/lua/zapret-antidpi.lua --lua-init=@$Z2DIR/lua/zapret-auto.lua"
QNUM2_TCP=59786; QNUM2_UDP=59787

[ -x "$NFQWS" ] || { echo "нет nfqws" >&2; exit 2; }

# --- ciadpi (byedpi) — T-ciadpi: тест кандидата в SOCKS-режиме (без -E), не
# через netns/REDIRECT — десинхронизация ClientHello у ciadpi не зависит от
# того, как он получил соединение (SOCKS-хендшейк или SO_ORIGINAL_DST), так что
# SOCKS-тест на реальном демоне даёт тот же вердикт, что и боевой transparent-режим,
# но на порядок дешевле (не трогает iptables/ipset стенда). Живёт своей парой
# порт+pid, полностью изолирован от zapret-очередей ($QNUM_TCP/$QNUM_UDP выше).
CIADPI_BIN=${CIADPI_BIN:-/opt/byedpi/ciadpi}
CIADPI_TEST_PORT=${CIADPI_TEST_PORT:-19999}
ciadpi_test_one() { # <domain> <strategy-args...> -> "HTTP-код время_мс" (000 при провале)
  local dom=$1; shift
  [ -x "$CIADPI_BIN" ] || { echo "000 0"; return; }
  pkill -f "ciadpi -i 127.0.0.1 -p $CIADPI_TEST_PORT " 2>/dev/null
  setsid "$CIADPI_BIN" -i 127.0.0.1 -p "$CIADPI_TEST_PORT" "$@" >/dev/null 2>&1 &
  local cpid=$! i bound=0
  for i in $(seq 1 30); do
    ss -tln 2>/dev/null | grep -q ":$CIADPI_TEST_PORT " && { bound=1; break; }
    sleep 0.1
  done
  local code=000 t=0
  if [ "$bound" = 1 ]; then
    read -r code t < <(curl -s -o /dev/null -w '%{http_code} %{time_total}' --socks5-hostname "127.0.0.1:$CIADPI_TEST_PORT" --max-time 6 "https://$dom/" 2>/dev/null)
  fi
  kill "$cpid" 2>/dev/null
  # time_total в секундах с плавающей точкой (curl) -> целые мс (T-are: utility по задержке)
  echo "${code:-000} $(awk -v s="${t:-0}" 'BEGIN{printf "%d", s*1000}')"
}

TEST_MODE=0
TEST_MODE2=0
if [ "${1:-}" = "--test-args" ]; then
  TEST_MODE=1
  DOMAIN="${2:?usage: solve.sh --test-args <domain> <proto> <args>}"
  PROTO="${3:?usage: solve.sh --test-args <domain> <proto> <args>}"
  shift 3
  TEST_ARGS="$*"
  [ -n "$TEST_ARGS" ] || { echo "нужны args" >&2; exit 2; }
  SOURCE=unknown
elif [ "${1:-}" = "--test-zapret2-args" ]; then
  TEST_MODE2=1
  DOMAIN="${2:?usage: solve.sh --test-zapret2-args <domain> <proto> <args>}"
  PROTO="${3:?usage: solve.sh --test-zapret2-args <domain> <proto> <args>}"
  shift 3
  TEST_ARGS2="$*"
  [ -n "$TEST_ARGS2" ] || { echo "нужны args" >&2; exit 2; }
  SOURCE=unknown
elif [ "${1:-}" = "--test-ciadpi-args" ]; then
  DOMAIN="${2:?usage: solve.sh --test-ciadpi-args <domain> <args>}"
  shift 2
  [ -n "$*" ] || { echo "нужны args" >&2; exit 2; }
  read -r code lat_ms < <(ciadpi_test_one "$DOMAIN" "$@")
  cid=$(python3 "$GWDB" strategy-find tcp ciadpi "$*" 2>/dev/null)
  if [ "$code" != "000" ]; then
    echo "РАБОТАЕТ (HTTP=$code)"; echo "OK $code"
    [ -n "$cid" ] && python3 "$GWDB" history-add "$cid" "$DOMAIN" success "$lat_ms" >/dev/null 2>&1
  else
    echo "не работает"; echo "FAIL $code"
    [ -n "$cid" ] && python3 "$GWDB" history-add "$cid" "$DOMAIN" fail >/dev/null 2>&1
  fi
  exit 0
else
  [ -f "$GWDB" ] || { echo "нет gwdb.py: $GWDB" >&2; exit 2; }
  DOMAIN="${1:?usage: solve.sh <domain> [source]}"
  SOURCE="${2:-unknown}"
  if [ "$SOURCE" = "quic-no-response" ]; then PROTO=udp; else PROTO=tcp; fi
fi
if [ "$PROTO" = "udp" ]; then QNUM=$QNUM_UDP; else QNUM=$QNUM_TCP; fi

IP=$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1{print $1}')
if [ -z "$IP" ]; then
  echo "не резолвится: $DOMAIN" >&2
  if [ "$TEST_MODE" = 1 ]; then echo "FAIL 000"; else echo "VPS"; fi
  exit 0
fi
echo "цель: $DOMAIN -> $IP (proto=$PROTO)"

# purge_test_mangle_rules — устойчивая замена точечным "iptables -D <точная
# спецификация>": та требовала точного совпадения "-d $IP", а на "снять
# остатки ПРОШЛОГО запуска" (вызов ниже) $IP — это IP ТЕКУЩЕГО домена, а
# висячее правило осталось от ДРУГОГО (прошлого) домена с ДРУГИМ IP —
# спецификации никогда не совпадали, мусор копился бесконечно (найдено
# вживую 2026-08-12: 6 висячих правил после серии тестов). Ищет по номеру
# NFQUEUE (уникален для наших тестовых очередей, не зависит от домена/IP)
# и удаляет по номеру строки — повторяет, пока совпадения не кончатся
# (может быть несколько дублей от разных прошлых доменов).
purge_test_mangle_rules() {
  local q
  for q in "$@"; do
    while true; do
      local ln
      # вывод `-L --line-numbers` рисует флаг --queue-num как "NFQUEUE num N
      # bypass" (НЕ "queue-num N" — это название CLI-флага при добавлении
      # правила, не то, что показывает listing; перепутал в первой версии
      # фикса — проверено вживую, без этого исправления не матчилось НИЧЕГО).
      ln=$(iptables -t mangle -L POSTROUTING -n --line-numbers 2>/dev/null | awk -v q="NFQUEUE num $q " '$0 ~ q {print $1; exit}')
      [ -n "$ln" ] || break
      iptables -t mangle -D POSTROUTING "$ln" 2>/dev/null || break
    done
  done
}

NPID=
teardown() {
  [ -n "$NPID" ] && kill "$NPID" 2>/dev/null
  pkill -f "qnum=$QNUM_TCP" 2>/dev/null; pkill -f "qnum=$QNUM_UDP" 2>/dev/null   # сироты на обеих тест-очередях
  pkill -f "qnum=$QNUM2_TCP" 2>/dev/null; pkill -f "qnum=$QNUM2_UDP" 2>/dev/null   # и на тест-очередях zapret2 (T-zapret2/T-zapret2-udp)
  ip netns del $NS 2>/dev/null
  ip link del veth-s 2>/dev/null      # ГЛАВНОЕ: остаток veth ломает следующий netns (был флак!)
  iptables -t nat -D POSTROUTING -s $SUBNET -o $WAN -j MASQUERADE 2>/dev/null
  iptables -D FORWARD -s $NSIP -p udp --dport $PORT -j ACCEPT 2>/dev/null   # T57: обход глобального UDP/443 DROP на время теста
  purge_test_mangle_rules $QNUM_TCP $QNUM_UDP $QNUM2_TCP $QNUM2_UDP
  iptables -t mangle -D POSTROUTING -s $NSIP -j ACCEPT 2>/dev/null
  [ -n "${IP:-}" ] && conntrack -D -d "$IP" 2>/dev/null >/dev/null  # сбросить conntrack цели (иначе connbytes «залипает»)
}
trap teardown EXIT
teardown 2>/dev/null   # снять остатки прошлого запуска (IP ещё не определён — purge_test_mangle_rules это переживает, старый способ — нет)

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
  # zapret2-tier (T-zapret2-udp): своя очередь, та же цепочка, что и её TCP-версия
  # ниже — --queue-bypass пропускает дальше, если nfqws2 сейчас не запущен.
  iptables -t mangle -I POSTROUTING 2 -s $NSIP -p udp -d "$IP" --dport $PORT -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM2_UDP --queue-bypass
  # ACCEPT СРАЗУ ПОСЛЕ обеих тест-очередей — не пускать на боевые правила
  iptables -t mangle -I POSTROUTING 3 -s $NSIP -j ACCEPT
else
  iptables -t mangle -I POSTROUTING 1 -s $NSIP -p tcp -d "$IP" --dport $PORT -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM --queue-bypass
  # zapret2-tier — своя очередь, та же цепочка (--queue-bypass пропускает дальше,
  # если nfqws2 сейчас не запущен, т.е. пока идёт zapret1-tier выше по очереди).
  iptables -t mangle -I POSTROUTING 2 -s $NSIP -p tcp -d "$IP" --dport $PORT -m connbytes --connbytes 1:6 --connbytes-mode packets --connbytes-dir original -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM2_TCP --queue-bypass
  # ACCEPT СРАЗУ ПОСЛЕ обеих тест-очередей — не пускать на боевые правила
  iptables -t mangle -I POSTROUTING 3 -s $NSIP -j ACCEPT
fi

# вывод "HTTP-код время_мс" (T-are: latency для utility-функции)
nscurl() {
  local code t; read -r code t < <(ip netns exec $NS curl -s -o /dev/null -w '%{http_code} %{time_total}' --resolve "$DOMAIN:$PORT:$IP" --max-time 6 "https://$DOMAIN/" 2>/dev/null)
  echo "${code:-000} $(awk -v s="${t:-0}" 'BEGIN{printf "%d", s*1000}')"
}
nscurl_udp() {
  local code t; read -r code t < <(ip netns exec $NS curl --http3-only -s -o /dev/null -w '%{http_code} %{time_total}' --resolve "$DOMAIN:$PORT:$IP" --max-time 6 "https://$DOMAIN/" 2>/dev/null)
  echo "${code:-000} $(awk -v s="${t:-0}" 'BEGIN{printf "%d", s*1000}')"
}

# wait_bound [qnum] — ждать, пока nfqws/nfqws2 реально ЗАБИНДИТ очередь (появится
# в /proc/net/netfilter/nfnetlink_queue). Иначе ClientHello/QUIC-Initial проскакивает
# по --queue-bypass без обхода и пресет ложно «фейлится» (источник флака).
# Параметр опционален (дефолт $QNUM) — zapret2-tier использует свою очередь.
wait_bound() {
  local q=${1:-$QNUM} i
  for i in $(seq 1 40); do
    grep -qE "^[[:space:]]*$q[[:space:]]" /proc/net/netfilter/nfnetlink_queue 2>/dev/null && return 0
    sleep 0.05
  done
  return 1
}

# --- режим --test-args (T-consolidate): одна стратегия, без базовой прямой
# проверки и без перебора тиров — просто "работает ли ЭТА строка для ЭТОГО домена".
if [ "$TEST_MODE" = 1 ]; then
  a=${TEST_ARGS//\$FAKE/$FAKEDIR}
  miss=0; for f in $(echo "$a" | grep -oE "$FAKEDIR/[^ ]+"); do [ -f "$f" ] || miss=1; done
  if [ $miss -eq 1 ]; then echo "нет fake-файла для args" >&2; echo "FAIL 000"; exit 0; fi
  $NFQWS --qnum=$QNUM --dpi-desync-fwmark=$MARK --filter-$PROTO=$PORT $a >/dev/null 2>&1 &
  NPID=$!
  if ! wait_bound; then kill "$NPID" 2>/dev/null; NPID=; echo "nfqws не забиндил" >&2; echo "FAIL 000"; exit 0; fi
  if [ "$PROTO" = "udp" ]; then read -r code lat_ms < <(nscurl_udp); else read -r code lat_ms < <(nscurl); fi
  code=${code:-000}
  kill "$NPID" 2>/dev/null; NPID=
  # нормализуем к канонической форме БД (литеральный $FAKE), независимо от того,
  # пришёл ли TEST_ARGS уже подставленным (brain-worker.sh, group.strategy) или
  # с плейсхолдером — $a уже полностью подставлен (строка 159), реверсим обратно.
  pid_db=$(python3 "$GWDB" strategy-find "$PROTO" zapret "${a//$FAKEDIR/\$FAKE}" 2>/dev/null)
  if [ "$code" != "000" ]; then
    echo "РАБОТАЕТ (HTTP=$code)"; echo "OK $code"
    [ -n "$pid_db" ] && python3 "$GWDB" history-add "$pid_db" "$DOMAIN" success "$lat_ms" >/dev/null 2>&1
  else
    echo "не работает"; echo "FAIL $code"
    [ -n "$pid_db" ] && python3 "$GWDB" history-add "$pid_db" "$DOMAIN" fail >/dev/null 2>&1
  fi
  exit 0
fi

# --- режим --test-zapret2-args (T-zapret2/T-zapret2-udp): аналог --test-args,
# но nfqws2 на своей очереди (QNUM2_TCP/QNUM2_UDP, в зависимости от $PROTO).
if [ "$TEST_MODE2" = 1 ]; then
  if [ "$PROTO" = "udp" ]; then Z2QNUM=$QNUM2_UDP; else Z2QNUM=$QNUM2_TCP; fi
  $NFQWS2 --qnum=$Z2QNUM --fwmark=$MARK $Z2LUA --filter-$PROTO=$PORT $TEST_ARGS2 >/dev/null 2>&1 &
  NPID=$!
  if ! wait_bound "$Z2QNUM"; then kill "$NPID" 2>/dev/null; NPID=; echo "nfqws2 не забиндил" >&2; echo "FAIL 000"; exit 0; fi
  if [ "$PROTO" = "udp" ]; then read -r code lat_ms < <(nscurl_udp); else read -r code lat_ms < <(nscurl); fi
  code=${code:-000}
  kill "$NPID" 2>/dev/null; NPID=
  pid_db=$(python3 "$GWDB" strategy-find "$PROTO" zapret2 "$TEST_ARGS2" 2>/dev/null)
  if [ "$code" != "000" ]; then
    echo "РАБОТАЕТ (HTTP=$code)"; echo "OK $code"
    [ -n "$pid_db" ] && python3 "$GWDB" history-add "$pid_db" "$DOMAIN" success "$lat_ms" >/dev/null 2>&1
  else
    echo "не работает"; echo "FAIL $code"
    [ -n "$pid_db" ] && python3 "$GWDB" history-add "$pid_db" "$DOMAIN" fail >/dev/null 2>&1
  fi
  exit 0
fi

if [ "$PROTO" = "udp" ]; then read -r BASE _ < <(nscurl_udp); else read -r BASE _ < <(nscurl); fi
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
# протокол — T57). Источник — gwdb.py strategies-list --engine zapret (TSV: id name
# proto args source trusted success_count engine score confidence); custom уже
# приходит отсортированным доверенные+successful первыми (ORDER BY в gwdb.py).
# --engine zapret — solve.sh исполняет ТОЛЬКО nfqws-стратегии, ciadpi/vps-строки
# в той же таблице ему не подходят (нет соответствующего исполнителя ниже).
try_tier() {
  local tier=$1 pid name proto args psource trusted sc engine score confidence a miss f code
  while IFS=$'\t' read -r pid name proto args psource trusted sc engine score confidence; do
    [ -n "$args" ] || continue
    a=${args//\$FAKE/$FAKEDIR}
    miss=0; for f in $(echo "$a" | grep -oE "$FAKEDIR/[^ ]+"); do [ -f "$f" ] || miss=1; done
    [ $miss -eq 0 ] || { echo "  [$tier:$pid] $name — пропуск (нет fake)"; continue; }

    [ -n "$NPID" ] && kill "$NPID" 2>/dev/null; NPID=
    # без --daemon: $! = реальный процесс (убиваемо, без сирот); --filter-tcp/udp — как боевой
    $NFQWS --qnum=$QNUM --dpi-desync-fwmark=$MARK --filter-$PROTO=$PORT $a >/dev/null 2>&1 &
    NPID=$!
    wait_bound || { kill "$NPID" 2>/dev/null; NPID=; echo "  [$tier:$pid] $name — nfqws не забиндил, пропуск"; continue; }
    local lat_ms
    if [ "$PROTO" = "udp" ]; then read -r code lat_ms < <(nscurl_udp); else read -r code lat_ms < <(nscurl); fi
    code=${code:-000}
    kill "$NPID" 2>/dev/null; NPID=
    if [ "$code" != "000" ]; then
      echo "  [$tier:$pid] $name — РАБОТАЕТ (HTTP=$code, ${lat_ms}мс) ✓"
      [ "$tier" = "custom" ] && python3 "$GWDB" strategy-mark-success "$pid" >/dev/null 2>&1
      python3 "$GWDB" history-add "$pid" "$DOMAIN" success "$lat_ms" >/dev/null 2>&1
      printf 'ZAPRET\t%s\t%s\t%s\n' "$PROTO" "$name" "$a"
      return 0
    fi
    echo "  [$tier:$pid] $name — нет (HTTP=$code)"
    python3 "$GWDB" history-add "$pid" "$DOMAIN" fail >/dev/null 2>&1
  done < <(python3 "$GWDB" strategies-list --tier "$tier" --proto "$PROTO" --engine zapret 2>/dev/null)
  return 1
}

echo "пробую стандартные пресеты ($PROTO) через netns-клиента..."
try_tier standard && exit 0
echo "стандартные не пробили — пробую custom-пресеты (доверенные первыми)..."
try_tier custom && exit 0

# pick_candidates <engine> <max> — половина топ-N лучших (по score/utility, для
# скорости — вероятные победители пробуются первыми) + половина топ-N дольше
# всего НЕ тестировавшихся (T-explore, гарантия что рано или поздно попробуется
# КАЖДАЯ стратегия, не только те, что случайно попали в топ по score). Дедуп по
# id — если стратегия попала в оба списка (напр. новая, ещё не тестированная,
# но с высоким стартовым score), не тестируем её дважды за один перебор.
pick_candidates() { # <engine> <max> [proto=tcp]
  local engine=$1 max=$2 proto=${3:-tcp}
  local half=$(( (max+1) / 2 ))
  { python3 "$GWDB" strategies-list --proto "$proto" --engine "$engine" 2>/dev/null | head -n "$half"
    python3 "$GWDB" strategies-explore --proto "$proto" --engine "$engine" -n "$half" 2>/dev/null
  } | awk -F'\t' '!seen[$1]++'
}

# ciadpi — только tcp (адаптер в brain-apply.sh пока не поддерживает udp REDIRECT,
# см. csvc_rules). Пробуем ПОСЛЕ zapret и ДО VPS-fallback — второй движок дешевле
# постоянного VPS-туннеля, если справляется своими силами.
# CIADPI_MAX_TRY — потолок кандидатов за один полный перебор (2026-07-29: пул
# вырос до 75 после импорта стратегий из APK ByeByeDPI, полный перебор одного
# домена стал занимать 5-8 минут вместо секунд — недопустимо для ночной очереди
# на сотни доменов).
CIADPI_MAX_TRY=${CIADPI_MAX_TRY:-20}
if [ "$PROTO" = "tcp" ] && [ -x "$CIADPI_BIN" ]; then
  echo "zapret не пробил — пробую ciadpi-стратегии (топ-$CIADPI_MAX_TRY по score/utility)..."
  while IFS=$'\t' read -r cid cname cproto cargs csource ctrusted csc cengine cscore cconf; do
    [ -n "$cargs" ] || continue
    read -r code lat_ms < <(ciadpi_test_one "$DOMAIN" $cargs)
    if [ "$code" != "000" ]; then
      echo "  [ciadpi:$cid] $cname — РАБОТАЕТ (HTTP=$code, ${lat_ms}мс) ✓"
      python3 "$GWDB" strategy-mark-success "$cid" >/dev/null 2>&1
      python3 "$GWDB" history-add "$cid" "$DOMAIN" success "$lat_ms" >/dev/null 2>&1
      printf 'CIADPI\ttcp\t%s\t%s\n' "$cname" "$cargs"
      exit 0
    fi
    echo "  [ciadpi:$cid] $cname — нет (HTTP=$code)"
    python3 "$GWDB" history-add "$cid" "$DOMAIN" fail >/dev/null 2>&1
  done < <(pick_candidates ciadpi "$CIADPI_MAX_TRY")
fi

# zapret2 (bol-van/zapret2, Lua-desync, T-zapret2) — третий движок, ПОСЛЕ ciadpi и
# ДО VPS. С T-zapret2-udp поддерживает оба протокола — своя очередь на каждый
# (QNUM2_TCP/QNUM2_UDP, см. константы выше), прикладной слой (brain-apply.sh
# svc_rules2/start_daemon2) уже был proto-осознанным с самого начала, не хватало
# только perebor'а здесь. 4 готовые UDP-стратегии в базе (QUIC/STUN/Discord-
# voice/WireGuard) ждали этого момента с добавления zapret2. Тот же потолок
# кандидатов, что у ciadpi — по тем же причинам (пул стратегий будет расти).
ZAPRET2_MAX_TRY=${ZAPRET2_MAX_TRY:-20}
if [ -x "$NFQWS2" ]; then
  if [ "$PROTO" = "udp" ]; then Z2QNUM=$QNUM2_UDP; else Z2QNUM=$QNUM2_TCP; fi
  echo "пробую zapret2-стратегии ($PROTO, топ-$ZAPRET2_MAX_TRY по score/utility)..."
  while IFS=$'\t' read -r zid zname zproto zargs zsource ztrusted zsc zengine zscore zconf; do
    [ -n "$zargs" ] || continue
    [ -n "$NPID" ] && kill "$NPID" 2>/dev/null; NPID=
    $NFQWS2 --qnum=$Z2QNUM --fwmark=$MARK $Z2LUA --filter-$PROTO=$PORT $zargs >/dev/null 2>&1 &
    NPID=$!
    wait_bound "$Z2QNUM" || { kill "$NPID" 2>/dev/null; NPID=; echo "  [zapret2:$zid] $zname — nfqws2 не забиндил, пропуск"; continue; }
    if [ "$PROTO" = "udp" ]; then read -r code lat_ms < <(nscurl_udp); else read -r code lat_ms < <(nscurl); fi
    code=${code:-000}
    kill "$NPID" 2>/dev/null; NPID=
    if [ "$code" != "000" ]; then
      echo "  [zapret2:$zid] $zname — РАБОТАЕТ (HTTP=$code, ${lat_ms}мс) ✓"
      python3 "$GWDB" strategy-mark-success "$zid" >/dev/null 2>&1
      python3 "$GWDB" history-add "$zid" "$DOMAIN" success "$lat_ms" >/dev/null 2>&1
      printf 'ZAPRET2\t%s\t%s\t%s\n' "$PROTO" "$zname" "$zargs"
      exit 0
    fi
    echo "  [zapret2:$zid] $zname — нет (HTTP=$code)"
    python3 "$GWDB" history-add "$zid" "$DOMAIN" fail >/dev/null 2>&1
  done < <(pick_candidates zapret2 "$ZAPRET2_MAX_TRY" "$PROTO")
fi

echo "ни один пресет не пробил -> VPS"; echo "VPS"
