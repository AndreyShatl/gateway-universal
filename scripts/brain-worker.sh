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
FAKEDIR=${FAKEDIR:-/opt/zapret/files/fake}

# strategy-find для zapret получает строки уже с ПОДСТАВЛЕННЫМ $FAKE (brain-apply.sh
# и ZAPRET-вердикт solve.sh отдают резолвленный путь /opt/zapret/files/fake/...,
# а в strategies.args хранится литеральный плейсхолдер "$FAKE") — без обратной
# подстановки strategy-find никогда бы не находил совпадение для zapret-стратегий.
unfake() { echo "${1//$FAKEDIR/\$FAKE}"; }

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
      local sid; sid=$(python3 "$GWDB" strategy-find "$proto" zapret "$(unfake "$strat")" 2>/dev/null)
      [ -n "$sid" ] && python3 "$GWDB" service-touch "$domain" "$sid" >/dev/null 2>&1
      return 0
    fi
  done <<< "$strategies"
  return 1
}

# try_existing_cgroups — то же самое для ciadpi-групп (T-ciadpi). Только tcp
# (адаптер brain-apply.sh пока не умеет ciadpi+udp). Тест — --test-ciadpi-args
# (SOCKS-режим ciadpi, см. solve.sh), не netns/REDIRECT — дешевле и не трогает
# iptables стенда на время проверки.
try_existing_cgroups() {
  local domain=$1 proto=$2 exclude=${3:-}
  [ "$proto" = "tcp" ] || return 1
  local strategies
  strategies=$(bash "$APPLY" list-ciadpi 2>/dev/null | python3 -c "
import json,sys
data=json.load(sys.stdin)
groups=[g for g in data if g.get('proto')=='tcp' and g.get('strategy') and g.get('group_id')!='$exclude']
groups.sort(key=lambda g: -len(g.get('domains',[])))
for g in groups: print(g['strategy'])
" 2>/dev/null)
  [ -n "$strategies" ] || return 1
  local strat res
  while IFS= read -r strat; do
    [ -n "$strat" ] || continue
    res=$(bash "$SOLVE" --test-ciadpi-args "$domain" $strat 2>/dev/null | tail -1)
    if [[ "$res" == OK* ]]; then
      bash "$APPLY" ciadpi "$domain" tcp $strat >/dev/null 2>&1
      log "✅ $domain → существующая ciadpi-группа (без полного перебора)"
      local sid; sid=$(python3 "$GWDB" strategy-find tcp ciadpi "$strat" 2>/dev/null)
      [ -n "$sid" ] && python3 "$GWDB" service-touch "$domain" "$sid" >/dev/null 2>&1
      return 0
    fi
  done <<< "$strategies"
  return 1
}

# try_existing_z2groups — то же самое для zapret2-групп (T-zapret2/T-zapret2-udp).
# Оба протокола — свой набор групп на каждый (совпадение $proto с группой
# обязательно, домен tcp не должен присоединяться к udp-группе и наоборот).
# Тест — --test-zapret2-args (netns+NFQUEUE, своя очередь на каждый proto).
try_existing_z2groups() {
  local domain=$1 proto=$2 exclude=${3:-}
  local strategies
  strategies=$(bash "$APPLY" list-zapret2 2>/dev/null | python3 -c "
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
    res=$(bash "$SOLVE" --test-zapret2-args "$domain" "$proto" $strat 2>/dev/null | tail -1)
    if [[ "$res" == OK* ]]; then
      bash "$APPLY" zapret2 "$domain" "$proto" $strat >/dev/null 2>&1
      log "✅ $domain → существующая zapret2-группа/$proto (без полного перебора)"
      local sid; sid=$(python3 "$GWDB" strategy-find "$proto" zapret2 "$strat" 2>/dev/null)
      [ -n "$sid" ] && python3 "$GWDB" service-touch "$domain" "$sid" >/dev/null 2>&1
      return 0
    fi
  done <<< "$strategies"
  return 1
}

process_domain() {
  local domain=$1 source=$2
  local proto; if [ "$source" = "quic-no-response" ]; then proto=udp; else proto=tcp; fi

  # 1. Домен уже в zapret-группе, ИЛИ ciadpi-группе, ИЛИ zapret2-группе (взаимно-
  #    исключающе — brain-apply.sh сам отцепляет домен от «чужого» движка при
  #    переносе) — сначала проверить, что ЕЁ стратегия ещё работает.
  local cur cur_gid cur_proto cur_strat ccur ccur_gid ccur_strat zcur zcur_gid zcur_strat
  cur=$(bash "$APPLY" group-of "$domain" 2>/dev/null)
  if [ -n "$cur" ]; then
    cur_gid=$(echo "$cur" | cut -f1); cur_proto=$(echo "$cur" | cut -f2); cur_strat=$(echo "$cur" | cut -f3)
    if [ -n "$cur_strat" ]; then
      local res; res=$(bash "$SOLVE" --test-args "$domain" "$cur_proto" $cur_strat 2>/dev/null | tail -1)
      if [[ "$res" == OK* ]]; then
        log "✅ $domain — текущая группа ($cur_gid) всё ещё работает"
        local sid0; sid0=$(python3 "$GWDB" strategy-find "$cur_proto" zapret "$(unfake "$cur_strat")" 2>/dev/null)
        [ -n "$sid0" ] && python3 "$GWDB" service-touch "$domain" "$sid0" >/dev/null 2>&1
        return 0
      fi
      log "⚠ $domain — группа $cur_gid больше не работает для этого домена, ищу замену"
      # T-vps-safety-net (2026-08-15): пока идёт поиск замены (полный перебор
      # может занимать несколько минут), домен временно уходит на VPS —
      # иначе он бы висел на уже подтверждённо СЛОМАННОЙ стратегии всё это
      # время, реально не работая ни через DPI, ни через VPS. Найдётся
      # замена — try_existing_groups/полный перебор ниже сами применят её и
      # снимут этот временный откат.
      bash "$APPLY" remove "$domain" >/dev/null 2>&1
    fi
  else
    ccur=$(bash "$APPLY" cgroup-of "$domain" 2>/dev/null)
    if [ -n "$ccur" ]; then
      ccur_gid=$(echo "$ccur" | cut -f1); ccur_strat=$(echo "$ccur" | cut -f3)
      if [ -n "$ccur_strat" ]; then
        local cres; cres=$(bash "$SOLVE" --test-ciadpi-args "$domain" $ccur_strat 2>/dev/null | tail -1)
        if [[ "$cres" == OK* ]]; then
          log "✅ $domain — текущая ciadpi-группа ($ccur_gid) всё ещё работает"
          local sid0c; sid0c=$(python3 "$GWDB" strategy-find tcp ciadpi "$ccur_strat" 2>/dev/null)
          [ -n "$sid0c" ] && python3 "$GWDB" service-touch "$domain" "$sid0c" >/dev/null 2>&1
          return 0
        fi
        log "⚠ $domain — ciadpi-группа $ccur_gid больше не работает для этого домена, ищу замену"
        # T-vps-safety-net (2026-08-15) — см. комментарий выше по zapret-ветке.
        bash "$APPLY" remove "$domain" >/dev/null 2>&1
      fi
    else
      zcur=$(bash "$APPLY" z2group-of "$domain" 2>/dev/null)
      if [ -n "$zcur" ]; then
        zcur_gid=$(echo "$zcur" | cut -f1); zcur_strat=$(echo "$zcur" | cut -f3)
        if [ -n "$zcur_strat" ]; then
          local zres; zres=$(bash "$SOLVE" --test-zapret2-args "$domain" $zcur_strat 2>/dev/null | tail -1)
          if [[ "$zres" == OK* ]]; then
            log "✅ $domain — текущая zapret2-группа ($zcur_gid) всё ещё работает"
            local sid0z; sid0z=$(python3 "$GWDB" strategy-find tcp zapret2 "$zcur_strat" 2>/dev/null)
            [ -n "$sid0z" ] && python3 "$GWDB" service-touch "$domain" "$sid0z" >/dev/null 2>&1
            return 0
          fi
          log "⚠ $domain — zapret2-группа $zcur_gid больше не работает для этого домена, ищу замену"
          # T-vps-safety-net (2026-08-15) — см. комментарий выше по zapret-ветке.
          bash "$APPLY" remove "$domain" >/dev/null 2>&1
        fi
      fi
    fi
  fi

  # 2. Быстрый путь: остальные существующие группы (исключая текущую — уже
  #    проверили и она не подошла) — сначала zapret, потом ciadpi, потом zapret2.
  if try_existing_groups "$domain" "$proto" "${cur_gid:-}"; then
    return 0
  fi
  if try_existing_cgroups "$domain" "$proto" "${ccur_gid:-}"; then
    return 0
  fi
  if try_existing_z2groups "$domain" "$proto" "${zcur_gid:-}"; then
    return 0
  fi

  # 3. Полный перебор (как раньше) — solve.sh сам пробует zapret, потом ciadpi
  #    (только tcp), и только если ничего не подошло — отдаёт VPS.
  log "▶ $domain (source=$source) — существующие группы не подошли, полный перебор пресетов"
  local out verdict
  out=$(ZAPRET=/opt/zapret GWDB="$GWDB" bash "$SOLVE" "$domain" "$source" 2>/dev/null)
  verdict=$(echo "$out" | grep -E '^(ZAPRET2|ZAPRET|CIADPI|VPS|DIRECT)' | tail -1)

  case "$verdict" in
    ZAPRET*)
      local proto2 strat2
      proto2=$(echo "$verdict" | cut -f2)
      strat2=$(echo "$verdict" | cut -f4-)
      if bash "$APPLY" zapret "$domain" "$proto2" $strat2 >/dev/null 2>&1; then
        log "✅ $domain → zapret/$proto2 (новая стратегия)"
        local sid2; sid2=$(python3 "$GWDB" strategy-find "$proto2" zapret "$(unfake "$strat2")" 2>/dev/null)
        [ -n "$sid2" ] && python3 "$GWDB" service-touch "$domain" "$sid2" >/dev/null 2>&1
      else
        log "⚠ $domain → zapret ошибка применения"
      fi
      ;;
    CIADPI*)
      local strat3
      strat3=$(echo "$verdict" | cut -f4-)
      if bash "$APPLY" ciadpi "$domain" tcp $strat3 >/dev/null 2>&1; then
        log "✅ $domain → ciadpi/tcp (новая стратегия)"
        local sid3; sid3=$(python3 "$GWDB" strategy-find tcp ciadpi "$strat3" 2>/dev/null)
        [ -n "$sid3" ] && python3 "$GWDB" service-touch "$domain" "$sid3" >/dev/null 2>&1
      else
        log "⚠ $domain → ciadpi ошибка применения"
      fi
      ;;
    ZAPRET2*)
      local proto4 strat4
      proto4=$(echo "$verdict" | cut -f2)
      strat4=$(echo "$verdict" | cut -f4-)
      if bash "$APPLY" zapret2 "$domain" "$proto4" $strat4 >/dev/null 2>&1; then
        log "✅ $domain → zapret2/$proto4 (новая стратегия)"
        local sid4; sid4=$(python3 "$GWDB" strategy-find "$proto4" zapret2 "$strat4" 2>/dev/null)
        [ -n "$sid4" ] && python3 "$GWDB" service-touch "$domain" "$sid4" >/dev/null 2>&1
      else
        log "⚠ $domain → zapret2 ошибка применения"
      fi
      ;;
    DIRECT*)
      bash "$APPLY" remove "$domain" >/dev/null 2>&1
      log "⚪ $domain работает напрямую — убран из обхода (GC)" ;;
    VPS*|*)
      if bash "$APPLY" vps "$domain" >/dev/null 2>&1; then
        log "🔵 $domain → VPS (fallback, ни одна стратегия не пробила)"
        # T-vps-hysteresis: подтверждённая работа через VPS — свой гистерезис
        # (макс. 3 дня, короче чем у zapret/ciadpi), иначе список vps[] в
        # brain-nightly.sh растёт без пруна и без пропуска навсегда (см. gwdb.py).
        python3 "$GWDB" vps-touch "$domain" success >/dev/null 2>&1
      else
        log "⚠ $domain → VPS ошибка"
        python3 "$GWDB" vps-touch "$domain" fail >/dev/null 2>&1
      fi ;;
  esac
}

log "воркер запущен (v2, T-consolidate)"
PROGRESS=/etc/gateway/brain-progress.json
while true; do
  line=$(pop)
  # T-progress-ui: очередь опустела — сбросить счётчик "поставлено сегодня", чтобы
  # следующий цикл enqueue (nightly/static-reeval) стартовал прогресс-бар с нуля,
  # а не продолжал накапливать total от предыдущего цикла.
  if [ -z "$line" ]; then
    echo '{"total":0,"started_at":""}' > "$PROGRESS" 2>/dev/null
    sleep "$IDLE"; continue
  fi
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
