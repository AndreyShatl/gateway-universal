#!/usr/bin/env bash
# brain-worker.sh — воркер очереди «мозга». Берёт домены из очереди по одному,
# гоняет solve.sh (перебор пресетов) и применяет вердикт через brain-apply.sh:
#   ZAPRET -> сущность на своей очереди (низкий пинг) + убрать из VPS
#   VPS    -> в автообход (fallback)
#   DIRECT -> ничего (не заблокирован)
# Serial: netns/тест-очередь одна, поэтому обрабатываем строго по одному.
#
# Очередь: /etc/gateway/brain-queue (один домен на строку, append = enqueue).
# Запуск: systemd gateway-brain-worker.service (loop, Restart=always).
set -uo pipefail

QUEUE=/etc/gateway/brain-queue
LOCK=/etc/gateway/brain-queue.lock
LOG=/var/log/gateway-brain.log
SOLVE=${SOLVE:-/root/solve.sh}
APPLY=${APPLY:-/root/brain-apply.sh}
PRESETS=${PRESETS:-/root/strategies.json}
IDLE=${IDLE:-5}

log() { echo "$(date '+%F %T') $*" >> "$LOG"; }
touch "$QUEUE"

# атомарно достать первую строку очереди (и удалить её)
pop() {
  exec 9>"$LOCK"; flock 9
  local d; d=$(head -1 "$QUEUE" 2>/dev/null)
  [ -n "$d" ] && sed -i '1d' "$QUEUE"
  flock -u 9
  echo "$d"
}

log "воркер запущен"
while true; do
  domain=$(pop)
  if [ -z "$domain" ]; then sleep "$IDLE"; continue; fi
  # нормализация: только домен, без схемы/путей
  domain=$(echo "$domain" | sed -E 's#^https?://##; s#/.*$##' | tr -d ' ')
  [ -n "$domain" ] || continue
  log "▶ $domain — тестирую пресеты"

  out=$(ZAPRET=/opt/zapret PRESETS="$PRESETS" bash "$SOLVE" "$domain" 2>/dev/null)
  verdict=$(echo "$out" | grep -E '^(ZAPRET|VPS|DIRECT)' | tail -1)

  case "$verdict" in
    ZAPRET*)
      strat=$(echo "$verdict" | cut -f3-)
      bash "$APPLY" zapret "$domain" $strat >/dev/null 2>&1 \
        && log "✅ $domain → zapret (низкий пинг)" \
        || log "⚠ $domain → zapret ошибка применения"
      ;;
    DIRECT*)
      # разблокировали (или не был заблокирован) — убрать из обхода целиком (GC)
      bash "$APPLY" remove "$domain" >/dev/null 2>&1
      log "⚪ $domain работает напрямую — убран из обхода (GC)" ;;
    VPS*|*)
      bash "$APPLY" vps "$domain" >/dev/null 2>&1 \
        && log "🔵 $domain → VPS (fallback, zapret не пробил)" \
        || log "⚠ $domain → VPS ошибка" ;;
  esac
done
