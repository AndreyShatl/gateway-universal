#!/usr/bin/env bash
# discord-voice-test.sh — T-discord-voice: ручной инструмент для живого теста
# кандидатов на десинхронизацию голоса Discord (UDP 50000-65535). НЕ трогает
# боевой путь (discord-tproxy.sh, TPROXY -> VPS) — временно ставит СВОЁ правило
# на NFQUEUE ПЕРЕД TPROXY-хуком (iptables -I 1), значит на время теста трафик
# идёт через nfqws вместо VPS; discord-tproxy.sh остаётся нетронутым и полностью
# восстанавливается командой `stop` (снимает только наше правило).
#
# Использование во время реального звонка в Discord:
#   discord-voice-test.sh start <candidate-name>   — применить кандидата
#   discord-voice-test.sh stop                      — снять, вернуться на VPS (discord-tproxy)
#   discord-voice-test.sh list                      — показать кандидатов
#
# ВАЖНО: между кандидатами звонок, скорее всего, придётся переустанавливать
# (смена пути = смена src-порта на стороне Discord voice-сервера) — это нормально,
# не баг теста.
set -uo pipefail

NFQWS=${NFQWS:-/opt/zapret/nfq/nfqws}
FAKEDIR=${FAKEDIR:-/opt/zapret/files/fake}
LAN=192.168.0.0/16
QNUM=59790
MARK=0x40000000
PIDFILE=/tmp/discord-voice-test.pid
# Точный диапазон портов голоса/IP-discovery Discord (не блинкет 50000-65535) —
# взят из живой рабочей конфигурации winws.exe (zapret-discord-youtube, 2026-07-29),
# подтверждённой реальным звонком пользователя в момент теста.
PORTS=${PORTS:-19294-19344,50000-50100}

declare -A CANDIDATES=(
  [udplen-plus2]="--filter-l7=discord --dpi-desync=udplen --dpi-desync-udplen-increment=2"
  [udplen-minus2]="--filter-l7=discord --dpi-desync=udplen --dpi-desync-udplen-increment=-2"
  [ipfrag-shallow]="--filter-l7=discord --dpi-desync=ipfrag2 --dpi-desync-ipfrag-pos-udp=8"
  [ipfrag-deep]="--filter-l7=discord --dpi-desync=ipfrag2 --dpi-desync-ipfrag-pos-udp=16"
  [fake-unknown]="--filter-l7=discord --dpi-desync=fake --dpi-desync-fake-unknown-udp=0xDEADBEEF --dpi-desync-repeats=2"
  [fake-unknown-ttl]="--filter-l7=discord --dpi-desync=fake --dpi-desync-fake-unknown-udp=0xDEADBEEF --dpi-desync-ttl=3 --dpi-desync-repeats=3"
  [tamper]="--filter-l7=discord --dpi-desync=tamper"
  [combo-fake-udplen]="--filter-l7=discord --dpi-desync=fake,udplen --dpi-desync-fake-unknown-udp=0xDEADBEEF --dpi-desync-udplen-increment=4"
  [combo-ipfrag-udplen]="--filter-l7=discord --dpi-desync=ipfrag2,udplen --dpi-desync-ipfrag-pos-udp=8 --dpi-desync-udplen-increment=-4"
  # ПРОВЕРЕННЫЙ РАБОЧИЙ (2026-07-29): извлечён из живой winws.exe-конфигурации
  # пользователя (zapret-discord-youtube) в момент реального звонка. Использует
  # родные --dpi-desync-fake-discord/-fake-stun (не generic-udp!) с фейком —
  # переиспользованным QUIC Initial пакетом (quic_initial_dbankcloud_ru.bin).
  [proven-real]="--filter-l7=discord,stun --dpi-desync=fake --dpi-desync-fake-discord=\$FAKE/discord_fake_dbankcloud.bin --dpi-desync-fake-stun=\$FAKE/discord_fake_dbankcloud.bin --dpi-desync-repeats=6"
)

usage() {
  echo "usage: $0 {start <candidate>|stop|list}" >&2
  exit 2
}

# iptables multiport использует ":" для диапазонов, nfqws --filter-udp — "-".
IPT_PORTS=$(echo "$PORTS" | tr '-' ':')

do_stop() {
  if [ -f "$PIDFILE" ]; then
    kill "$(cat "$PIDFILE")" 2>/dev/null
    rm -f "$PIDFILE"
  fi
  iptables -t mangle -D PREROUTING -s "$LAN" -p udp -m multiport --dports "$IPT_PORTS" -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM --queue-bypass 2>/dev/null
  iptables -t mangle -D PREROUTING -s "$LAN" -p udp -m multiport --dports "$IPT_PORTS" -j ACCEPT 2>/dev/null
  echo "остановлено — голос Discord снова на discord-tproxy.sh (VPS)"
}

do_start() {
  local name=$1
  local args="${CANDIDATES[$name]:-}"
  [ -n "$args" ] || { echo "нет такого кандидата: $name" >&2; do_list; exit 2; }
  args=${args//\$FAKE/$FAKEDIR}
  do_stop >/dev/null 2>&1
  # NFQUEUE ПЕРЕД discord-tproxy.sh (которое тоже в PREROUTING/mangle, -I 1 у
  # обоих — porядок вставки решает, кто раньше; ставим себя самым первым).
  iptables -t mangle -I PREROUTING 1 -s "$LAN" -p udp -m multiport --dports "$IPT_PORTS" -j ACCEPT
  iptables -t mangle -I PREROUTING 1 -s "$LAN" -p udp -m multiport --dports "$IPT_PORTS" -m mark ! --mark $MARK/$MARK -j NFQUEUE --queue-num $QNUM --queue-bypass
  $NFQWS --qnum=$QNUM --dpi-desync-fwmark=$MARK --filter-udp="$PORTS" $args >/tmp/discord-voice-test.log 2>&1 &
  echo $! > "$PIDFILE"
  echo "применён кандидат '$name': $args"
  echo "порты: $PORTS"
  echo "теперь позвони/переподключись в Discord — если голос идёт, это рабочая стратегия"
}

do_list() {
  echo "кандидаты:"
  for k in "${!CANDIDATES[@]}"; do echo "  $k — ${CANDIDATES[$k]}"; done
}

case "${1:-}" in
  start) shift; do_start "${1:?usage: $0 start <candidate>}" ;;
  stop)  do_stop ;;
  list)  do_list ;;
  *) usage ;;
esac
