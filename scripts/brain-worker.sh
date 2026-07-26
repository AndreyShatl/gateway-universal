#!/usr/bin/env bash
# brain-worker.sh v2 (T-consolidate, 2026-07-23) — воркер очереди «мозга».
#
# Порядок обработки домена ИЗМЕНИЛСЯ (раньше — сразу полный перебор):
#   1. Если домен уже состоит в какой-то ГРУППЕ — сначала ОДНИМ быстрым тестом
#      (solve.sh --test-args) проверяем, что стратегия ЭТОЙ группы всё ещё
#      работает. Работает — ничего не делаем (не гоняем полный перебор зря).
#   2. Не работает (или домен свежий, ни в какой группе) — пробуем ВСЕ
#      СУЩЕСТВУЮЩИЕ группы (proto тот же), от самой крупной к мелкой — тоже
#      только --test-args, без полного перебора. Нашли — присоединяем к ней.
#   3. Ни одна существующая группа не подошла — полный перебор пресетов
#      (solve.sh как раньше): ZAPRET -> новая или существующая (если строка
#      стратегии случайно совпала) группа; VPS -> автообход; DIRECT -> GC.
#
# Очередь: /etc/gateway/brain-queue ("domain<TAB>source" на строку, T50 — source
# из сигнатуры детектора, напр. syn-timeout/rst-after-clienthello; строки без
# табуляции — старый формат/ночная переоценка, source считается "reeval").
# Запуск: systemd gateway-brain-worker.service (loop, Restart=always).
set -uo pipefail

QUEUE=/etc/gateway/brain-queue
LOCK=/etc/gateway/brain-queue.lock
LOG=/var/log/gateway-brain.log
SOLVE=${SOLVE:-/root/solve.sh}
APPLY=${APPLY:-/root/brain-apply.sh}
GWDB=${GWDB:-/root/gateway-universal/scripts/gwdb.py}
IDLE=${IDLE:-5}

log() { echo "$(date '+%F %T') $*" >> "$LOG"; }
touch "$QUEUE"

pop() {
  exec 9>"$LOCK"; flock 9
  local d; d=$(head -1 "$QUEUE" 2>/dev/null)
  [ -n "$d" ] && sed -i '1d' "$QUEUE"
  flock -u 9
  echo "$d"
}

# try_existing_groups <domain> <proto> [exclude_group_id] — попробовать все
# существующие группы этого proto (от крупной к мелкой), без полного перебора.
# При успехе сам присоединяет домен к найденной группе (через brain-apply.sh
# zapret — ensure_group найдёт группу по точному совпадению строки стратегии).
try_existing_groups() {
  local domain=$1 proto=$2 exclude=${3:-}
  local strategies
  strategies=$(bash "$APPLY" groups 2>/dev/null | python3 -c "
import json,sys
data=json.load(sys.stdin)
groups=[g for g in data if g.get('proto')=='$proto' and g.get('strategy') and g.get('group_id')!='$exclude']
groups.sort(key=lambda g: -len(g.get('domains',[])))
for g in groups: print(g['strategy'])
" 2>/dev/null)
  [ -n "$strategies" ] || return 1
  local strat res
  while IFS= read -r strat; do
    [ -n "$strat" ] || continue
    res=$(bash "$SOLVE" --test-args "$domain" "$proto" $strat 2>/dev/null | tail -1)
    if [[ "$res" == OK* ]]; then
      bash "$APPLY" zapret "$domain" "$proto" $strat >/dev/null 2>&1
      log "✅ $domain → существующая группа ($proto, без полного перебора)"
      return 0
    fi
  done <<< "$strategies"
  return 1
}

process_domain() {
  local domain=$1 source=$2
  local proto; if [ "$source" = "quic-no-response" ]; then proto=udp; else proto=tcp; fi

  # 1. Домен уже в группе — сначала проверить, что ЕЁ стратегия ещё работает.
  local cur cur_gid cur_proto cur_strat
  cur=$(bash "$APPLY" group-of "$domain" 2>/dev/null)
  if [ -n "$cur" ]; then
    cur_gid=$(echo "$cur" | cut -f1); cur_proto=$(echo "$cur" | cut -f2); cur_strat=$(echo "$cur" | cut -f3)
    if [ -n "$cur_strat" ]; then
      local res; res=$(bash "$SOLVE" --test-args "$domain" "$cur_proto" $cur_strat 2>/dev/null | tail -1)
      if [[ "$res" == OK* ]]; then
        log "✅ $domain — текущая группа ($cur_gid) всё ещё работает"
        return 0
      fi
      log "⚠ $domain — группа $cur_gid больше не работает для этого домена, ищу замену"
    fi
  fi

  # 2. Быстрый путь: остальные существующие группы (исключая текущую — уже
  #    проверили и она не подошла).
  if try_existing_groups "$domain" "$proto" "${cur_gid:-}"; then
    return 0
  fi

  # 3. Полный перебор (как раньше) — новый или естественно совпавший кластер.
  log "▶ $domain (source=$source) — существующие группы не подошли, полный перебор пресетов"
  local out verdict
  out=$(ZAPRET=/opt/zapret GWDB="$GWDB" bash "$SOLVE" "$domain" "$source" 2>/dev/null)
  verdict=$(echo "$out" | grep -E '^(ZAPRET|VPS|DIRECT)' | tail -1)

  case "$verdict" in
    ZAPRET*)
      local proto2 strat2
      proto2=$(echo "$verdict" | cut -f2)
      strat2=$(echo "$verdict" | cut -f4-)
      bash "$APPLY" zapret "$domain" "$proto2" $strat2 >/dev/null 2>&1 \
        && log "✅ $domain → zapret/$proto2 (новая стратегия)" \
        || log "⚠ $domain → zapret ошибка применения"
      ;;
    DIRECT*)
      bash "$APPLY" remove "$domain" >/dev/null 2>&1
      log "⚪ $domain работает напрямую — убран из обхода (GC)" ;;
    VPS*|*)
      bash "$APPLY" vps "$domain" >/dev/null 2>&1 \
        && log "🔵 $domain → VPS (fallback, ни одна стратегия не пробила)" \
        || log "⚠ $domain → VPS ошибка" ;;
  esac
}

log "воркер запущен (v2, T-consolidate)"
while true; do
  line=$(pop)
  if [ -z "$line" ]; then sleep "$IDLE"; continue; fi
  domain="${line%%$'\t'*}"
  if [ "$domain" = "$line" ]; then source=reeval; else source="${line#*$'\t'}"; fi
  domain=$(echo "$domain" | sed -E 's#^https?://##; s#/.*$##' | tr -d ' ')
  [ -n "$domain" ] || continue
  if [ "$(python3 "$GWDB" whitelisted "$domain" 2>/dev/null)" = "1" ]; then
    log "⚪ $domain — whitelist, пропуск"
    continue
  fi
  process_domain "$domain" "$source"
done
