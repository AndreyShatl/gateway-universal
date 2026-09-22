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
SERVICES=${SERVICES:-/etc/gateway/zapret-services.json}

# T-ggc-ipidzero-pin (2026-09-22): единственная LOCAL-стратегия для GGC-видео
# (googlevideo/gvt1), подтверждённая эталонным конфигом владельца на ПК
# (zapret "general (ALT)", строка --filter-tcp=443 --hostlist=list-google.txt).
# Решающий флаг — --ip-id=zero: обнуляет IP ID в инжектируемых фейк-пакетах,
# чем ломает троттлинг провайдера на ДЛИТЕЛЬНОЙ передаче видео. Без него
# рукопожатие проходит (превью грузятся), но sustained-поток душится (§8.3 —
# изолированная проба solve.sh этого не видит: короткий curl не упирается в
# троттлинг). Поэтому GGC-хосты НЕ отправляем в общий перебор (solve.sh выдаёт
# пресеты без --ip-id=zero) — пинним сразу в эту стратегию. brain-apply сам
# кладёт nat RETURN группы поверх gw_autoroute и добавляет в ipset широкие
# CDN-диапазоны googlevideo + /24 наблюдаемых GGC-хостов (T-ggc-local-cache),
# так что покрытие ротации IP автоматическое, по IP, без ручного выбора хостов.
GGC_STRAT=${GGC_STRAT:---dpi-desync=fake,fakedsplit --dpi-desync-repeats=6 --dpi-desync-fooling=ts --dpi-desync-fakedsplit-pattern=0x00 --dpi-desync-fake-tls=/opt/zapret/files/fake/tls_clienthello_www_google_com.bin --ip-id=zero}

# T-vps-pin (2026-08-16): пользователь может закрепить сервис (discord/
# youtube/instagram) целиком на VPS кнопкой в UI — mode="vps" в
# zapret-services.json уже управляет статическим xray-роутингом
# (render-config.sh/build-domains.sh), но раньше НИКАК не влиял на этот
# воркер — pinned-домен мог быть вручную снят с DPI-обхода, а следующий же
# ночной/пассивный проход тихо назначал ему ciadpi заново (реальный кейс
# этой ночи: 41 домен discord пришлось снимать вручную, расползались бы
# снова). Теперь process_domain() проверяет pin ПЕРВЫМ делом.
direct_service() { # <domain> -> "1" если домен в сервисе с mode=direct (пользовательский выбор: без обхода)
  python3 - "$1" <<'PYD' 2>/dev/null
import json, sys
d = sys.argv[1].lower()
try:
    data = json.load(open("/etc/gateway/zapret-services.json"))
    if not isinstance(data, list): data = data.get("services", data)
    for svc in data:
        if svc.get("mode") == "direct":
            doms = [x.lower() for x in svc.get("domains", [])]
            if d in doms:
                raise SystemExit(0)
except SystemExit:
    raise
except Exception:
    pass
raise SystemExit(1)
PYD
}

pinned_vps_service() { # <domain> -> "1" если домен в сервисе с mode=vps, иначе ""
  local domain=$1
  python3 -c "
import json, sys
try:
    data = json.load(open('$SERVICES'))
except Exception:
    sys.exit(0)
d = '$domain'.lower()
for svc in data:
    if (svc.get('mode') or '') != 'vps':
        continue
    for sd in svc.get('domains', []):
        sd = sd.lower()
        if d == sd or d.endswith('.' + sd):
            print('1')
            sys.exit(0)
" 2>/dev/null
}

# GGC — особый случай pinned YouTube: сам сервис остаётся на VPS как безопасный
# baseline, но потоковый rr* hostname может быть доставлен только через LOCAL
# (провайдерский GGC недоступен с VPS). Для него разрешён только поиск уже
# проверенной LOCAL-стратегии; при неудаче VPS-пин не снимается.
ggc_delivery_host() {
  local host
  host=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  case "$host" in
    googlevideo.com|*.googlevideo.com|gvt1.com|*.gvt1.com) return 0 ;;
    *) return 1 ;;
  esac
}

# T-nightly-respect-quarantine (2026-09-22): карантин живых провалов —
# документированный механизм (см. комментарий T-pinned-fullsolve ниже и
# detector/live_retrigger.go), который должен ловить DPI-стратегии,
# «работающие» только для изолированного curl-теста и не пробивающие реального
# клиента (грабля §8.3, кейс updates.discord.com). Семантика 1:1 с Go:
# >=3 живых провала одного домена за 24ч = активный карантин. Днём его
# применяет детектор (forceVPSInstant), но ночной reeval про карантин не знал —
# отсюда и повторяющиеся отказы (youtube.*: 3 страйка, ночью «LOCAL
# подтверждён, fallback снят», реальный клиент не работает).
live_fail_quarantined() { # <domain> -> 0 если домен в активном карантине (>=3 живых провала за 24ч)
  python3 - "$1" <<'PYQ' 2>/dev/null
import json, re, sys
from datetime import datetime, timedelta, timezone
dom = sys.argv[1].strip().lower()
try:
    m = json.load(open("/etc/gateway/observe/live-fail-strikes.json"))
except Exception:
    raise SystemExit(1)
r = m.get(dom)
if not isinstance(r, dict):
    raise SystemExit(1)
try:
    count = int(r.get("count", 0))
    # Go пишет RFC3339Nano (9 цифр дроби); нормализуем до микросекунд, чтобы
    # fromisoformat работал и на python < 3.11 — иначе исключение и молчаливый
    # no-op всей защиты.
    ts = re.sub(r"\.(\d{6})\d+", r".\1", str(r["last_strike"]))
    last = datetime.fromisoformat(ts)
except Exception:
    raise SystemExit(1)
if last.tzinfo is None:
    last = last.replace(tzinfo=timezone.utc)
quarantined = count >= 3 and (datetime.now(timezone.utc) - last) < timedelta(hours=24)
raise SystemExit(0 if quarantined else 1)
PYQ
}

# После первого LOCAL-успеха autoroute нарочно остаётся как VPS-fallback.
# Только ночной reeval подтверждает, что локальная стратегия не была разовой
# удачей, и снимает fallback. Это даёт CDN время пережить ротацию edge-IP.
confirm_local_at_nightly() { # <domain> <source>
  [ "$2" = "reeval" ] || return 0
  bash "$APPLY" confirm-local "$1" >/dev/null 2>&1
  log "🌙 $1 — LOCAL подтверждён ночью, VPS-fallback снят"
}

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

  # 0.0. T-ggc-ipidzero-pin (2026-09-22): GGC-видео (googlevideo/gvt1) по TCP —
  #      ВСЕГДА пинним в проверенную стратегию с --ip-id=zero, минуя общий
  #      перебор. Причина: GGC недостижим через VPS (§8.2 — провайдерский кэш,
  #      TLS режется на границе), значит LOCAL — единственный путь; а единственный
  #      LOCAL-вариант, пробивающий ДЛИТЕЛЬНОЕ видео (не только превью), —
  #      --ip-id=zero (см. определение GGC_STRAT). solve.sh в переборе выдаёт
  #      пресеты без этого флага → «превью есть, видео нет». Пин идемпотентен:
  #      brain-apply ensure_group переиспользует одну группу (proto+strategy),
  #      так что все ротирующиеся rr*---sn-*.gvt1/googlevideo-хосты стекаются в
  #      неё же, а её ipset покрывает ротацию по IP (CDN-диапазоны + /24).
  #      VPS-фолбэк сохраняется (T-parallel-fallback) для новых непокрытых IP.
  #      UDP (source=quic-no-response) не трогаем — там QUIC-фейк, своя группа.
  if [ "$proto" = tcp ] && ggc_delivery_host "$domain"; then
    bash "$APPLY" zapret "$domain" tcp $GGC_STRAT >/dev/null 2>&1
    log "🎞 $domain — GGC-пин: применена LOCAL-стратегия --ip-id=zero (длительное видео без троттлинга)"
    return 0
  fi

  # 0. Сервис закреплён на VPS кнопкой в UI (T-vps-pin) — никакого DPI-обхода,
  #    даже пробовать не нужно. Снимаем любую существующую группу (на случай
  #    гонки: пин поставили ПОСЛЕ того, как домен уже получил DPI-стратегию)
  #    и гарантируем VPS. Всегда return — шаги 1-3 ниже не должны выполняться.
  # T-pinned-fullsolve (2026-09-17, идея владельца: auto = реальный поиск для
  # каждого домена). Раньше пиннед-сервисы обрабатывались жёстко: «VPS и не
  # искать вовсе» (консерватизм до появления VPS-подложки). Теперь подложка
  # гарантирует ноль простоев при любом исходе — пиннед-доменам РАЗРЕШЁН
  # полный перебор: нашли пробивающуюся стратегию → домен в DPI-группе С
  # сохранением VPS-фолбэка (dormant, неуязвим для ротации CDN); не нашли →
  # остаётся на VPS-полу. Карантин живых провалов ловит стратегии, которые
  # «работают» только для нашего curl-теста.
  if [ -n "$(pinned_vps_service "$domain")" ]; then
    # T-pinned-fullsolve-gate (2026-09-18, живой инцидент: детектор поставил
    # discord-домены в очередь фоновым поиском после живых провалов — и они
    # уехали на DPI без ведома владельца, реальные клиенты отвалились).
    # Полный перебор для пиннед — ТОЛЬКО по явному действию владельца
    # (source=auto от кнопки). Фоновые постановки — быстрая ветка ниже.
    if [ "$source" != "auto" ]; then
      # СТРОГИЙ vps (семантика владельца 2026-09-18): кнопка vps = только VPS.
      # Фоновые постановки (детектор/ночь) тоже снимают DPI-членства, если
      # они завелись — микс возможен только в режиме auto.
      bash "$APPLY" vps "$domain" >/dev/null 2>&1
      return 0
    fi
    if ggc_delivery_host "$domain" && [ "$source" != "auto" ]; then
      # GGC-хосты в строгом vps: не трогаем (они не обрабатываются вообще —
      # стриминговые хосты недостижимы через VPS, микс только в auto)
      return 0
    fi
    # Не-GGC пиннед-домен: подложка + падаем в общий конвейер ниже (полный
    # поиск с сохранением fallback). vps-touch не делаем — путь не «конечный».
    bash "$APPLY" vps-fallback "$domain" >/dev/null 2>&1
    log "🔎 $domain — пиннед-сервис: VPS-подложка ensured, запускаем полный поиск LOCAL (fallback остаётся)"
  fi

  # 0.5. T-nightly-respect-quarantine (2026-09-22): домен в АКТИВНОМ карантине
  #      живых провалов (>=3 реальных провала клиента за 24ч) не назначаем и не
  #      подтверждаем на LOCAL — изолированная проба (шаги 1-3) структурно не
  #      видит разницу TLS-отпечатков и «подтверждает» стратегию, не работающую у
  #      живого клиента (§8.3). Гарантируем VPS (идемпотентно: уже-на-VPS
  #      пропускается без flush conntrack) и выходим — карантин истечёт через 24ч,
  #      и следующая ночь даст DPI ещё один шанс. Исключения: source=auto (явная
  #      кнопка владельца переопределяет всё) и GGC-хосты googlevideo/gvt1
  #      (недостижимы через VPS — для них LOCAL единственный путь).
  if [ "$source" != "auto" ] && ! ggc_delivery_host "$domain" && live_fail_quarantined "$domain"; then
    bash "$APPLY" vps "$domain" >/dev/null 2>&1
    log "⛔ $domain — активный карантин живых провалов (>=3/24ч): LOCAL не применяем, остаётся на VPS"
    return 0
  fi

  # 1. Домен уже в zapret-группе, ИЛИ ciadpi-группе, ИЛИ zapret2-группе (взаимно-
  #    исключающе — brain-apply.sh сам отцепляет домен от «чужого» движка при
  #    переносе) — сначала проверить, что ЕЁ стратегия ещё работает.
  local cur cur_gid cur_proto cur_strat ccur ccur_gid ccur_strat zcur zcur_gid zcur_strat
  cur=$(bash "$APPLY" group-of "$domain" 2>/dev/null)
  # T-ggc-tcp-coverage (2026-09-22): GGC-хост (googlevideo/gvt1), у которого есть
  # группа ТОЛЬКО под UDP/QUIC, формально выглядит «решённым» — и шаг 1 ниже на
  # этом успокаивался, никогда не подбирая TCP-стратегию. Но реальный клиент тянет
  # видео по TCP (QUIC на шлюзе глобально дропается, кроме исключений), а TCP-путь
  # GGC через VPS структурно мёртв (§8.2 — провайдерский кэш недостижим с VPS,
  # TLS режется на границе). В итоге IP таких хостов оставались в gw_autoroute →
  # REDIRECT :12347 → VPS → «превью есть, видео нет». Для GGC требуем группу ИМЕННО
  # нужного протокола: UDP-группа не закрывает TCP-потребность → сбрасываем cur и
  # падаем в поиск TCP-стратегии (brain-apply при успехе даст nat RETURN выше
  # gw_autoroute + /24-покрытие T-ggc-local-cache, VPS останется dormant-fallback).
  # source=quic-no-response (proto=udp) не трогаем — там UDP-группа и есть цель.
  if [ -n "$cur" ] && [ "$proto" = tcp ] && ggc_delivery_host "$domain"; then
    cur_proto=$(echo "$cur" | cut -f2)
    if [ "$cur_proto" != tcp ]; then
      log "🎞 $domain — GGC: есть только $cur_proto-группа, а видео идёт по TCP → ищу TCP-стратегию"
      cur=""
    fi
  fi
  if [ -n "$cur" ]; then
    cur_gid=$(echo "$cur" | cut -f1); cur_proto=$(echo "$cur" | cut -f2); cur_strat=$(echo "$cur" | cut -f3)
    if [ -n "$cur_strat" ]; then
      local res; res=$(bash "$SOLVE" --test-args "$domain" "$cur_proto" $cur_strat 2>/dev/null | tail -1)
      if [[ "$res" == OK* ]]; then
        log "✅ $domain — текущая группа ($cur_gid) всё ещё работает"
        local sid0; sid0=$(python3 "$GWDB" strategy-find "$cur_proto" zapret "$(unfake "$cur_strat")" 2>/dev/null)
        [ -n "$sid0" ] && python3 "$GWDB" service-touch "$domain" "$sid0" >/dev/null 2>&1
        confirm_local_at_nightly "$domain" "$source"
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
          confirm_local_at_nightly "$domain" "$source"
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
            confirm_local_at_nightly "$domain" "$source"
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

  # T-vps-safety-net-new (2026-08-16): домен совсем новый (шаги 1-2 выше не
  # нашли для него вообще НИКАКОЙ группы) — полный перебор ниже (solve.sh сам
  # по себе НЕ трогает боевой трафик, это песочница в netns) может занимать
  # несколько минут, а brain-apply.sh vps раньше вызывался только ПОСЛЕ его
  # завершения (ветка VPS* ниже). Всё это время домен был голый — ни DPI-
  # обхода, ни VPS (живой случай: stable.dl2.discordapp.net на Pi завис
  # именно так). Симметрично уже сделанному фиксу для «старая стратегия
  # сломалась» — сразу ставим VPS-заглушку, полный перебор её при успехе
  # сам заменит на найденную стратегию (case ниже).
  bash "$APPLY" vps "$domain" >/dev/null 2>&1

  # 3. Полный перебор (как раньше) — solve.sh сам пробует zapret, потом ciadpi
  #    (только tcp), и только если ничего не подошло — отдаёт VPS.
  log "▶ $domain (source=$source) — существующие группы не подошли, полный перебор пресетов"
  local out verdict
  # T-solve-semaphore (2026-09-17): полный перебор — тяжёлый (netns + тестовые
  # демоны + сотни проб; на 2-ядерном стенде 4 одновременных задушат шлюз,
  # load 4.4+ при живом тесте). Быстрые ветки выше остаются параллельными (×4),
  # полный перебор — глобально по одному (flock). Это всё равно быстрее старого
  # полностью-последовательного воркера: пока один solve пыхтит, остальные
  # воркеры разбирают быстрые домены.
  out=$(ZAPRET=/opt/zapret GWDB="$GWDB" flock /tmp/solve-global.lock bash "$SOLVE" "$domain" "$source" 2>/dev/null)
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

# T-parallel-workers (2026-09-17): до 4 воркеров разбирают очередь параллельно
# (по просьбе владельца; тест-режим: "посмотрим как будет себя чувствовать 4").
# Безопасность гонок: pop() под flock на $LOCK (было и раньше), процессинг
# per-domain независим; лог может чередоваться строками — осознанно.
WORKERS="${WORKERS:-4}"

worker_loop() {
  local line domain source
  while true; do
    line=$(pop)
    # T-progress-ui: очередь опустела — сбросить счётчик "поставлено сегодня",
    # чтобы следующий цикл enqueue стартовал прогресс с нуля. Гонка записи
    # между воркерами безвредна — пишут одно и то же.
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
    if direct_service "$domain"; then
      log "↗ $domain — сервис в режиме direct (пользователь), не трогаем"
      continue
    fi
    process_domain "$domain" "$source"
  done
}

log "параллельный режим: WORKERS=$WORKERS"
for _w in $(seq 1 "$WORKERS"); do
  worker_loop &
done
wait
