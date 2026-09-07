#!/usr/bin/env bash
# brain-refresh-ips.sh (T-cdn-refresh, 2026-08-18) — системный фикс класса
# бага "CDN_CIDR_HINTS": ротирующиеся CDN (Discord/Cloudflare, Instagram/
# Facebook/Meta, потенциально любой следующий) резолвятся в РАЗНОЕ
# подмножество edge-IP при каждом DNS-запросе. Группа получает свежий ipset
# один раз — при назначении стратегии (rebuild_group_ipset) или при restore
# после ребута — и дальше IP-состав ipset ЗАМОРОЖЕН до следующего такого
# события. Реальное соединение клиента может уйти на IP, который никогда не
# резолвился нам — трафик идёт мимо ipset совсем, DPI-обход не применяется
# (живая находка: Discord "checking for update" вечером, потом то же самое
# для Instagram — "чёрный экран половины видео", один и тот же паттерн дважды
# за одну ночь).
#
# CDN_CIDR_HINTS (в brain-apply.sh) — реактивный пластырь: работает только
# для доменов, которые кто-то УЖЕ заметил сломанными и вписал диапазон
# руками. Не масштабируется — следующий сервис с той же болезнью снова
# потребует ручной находки.
#
# Этот скрипт — системное решение: каждые несколько минут ПОДДОБАВЛЯЕТ
# (не пересобирает — flush тут не нужен, только add) свежерезолвленные IP
# всех доменов всех активных DPI-групп (все три движка) в их ipset. Ротация
# CDN у ЛЮБОГО домена самозаживает за один-два цикла, без человека и без
# хардкода диапазонов под каждый сервис. CDN_CIDR_HINTS остаётся полезным
# для по-настоящему широких anycast-пулов, которые редкий рефреш не успеет
# захватить весь — но перестаёт быть ЕДИНСТВЕННОЙ защитой.
#
# Только ADD, никогда flush/remove здесь — устаревшие IP убираются
# естественным путём при следующем полном rebuild (restore после ребута,
# переназначение стратегии) через rebuild_group_ipset. Между такими
# событиями ipset только растёт — это осознанный компромисс (см. заголовок
# rebuild_group_ipset про maxelem 65536, при типичных размерах групп расти
# до потолка между ребутами/переназначениями практически нереально).
set -uo pipefail

STATE=/etc/gateway/brain-services.json
CSTATE=/etc/gateway/brain-services-ciadpi.json
Z2STATE=/etc/gateway/brain-services-zapret2.json
LOG=/var/log/gateway-brain.log
IPSET_PREFIX_ZAPRET=brain_
IPSET_PREFIX_CIADPI=brainc_
IPSET_PREFIX_ZAPRET2=brainz2_

log() { echo "$(date '+%F %T') [refresh-ips] $*" >> "$LOG"; }

# T-cdn-refresh-deep-log (2026-08-18) — по просьбе пользователя: тестируем
# интервал 2 минуты, для этого нужна возможность реально разобрать, что
# происходит на каждом цикле (какие IP реально НОВЫЕ, не просто "прогнали
# резолв"), а не просто "готово" раз в прогон. ipset test КАЖДОГО
# резолвленного IP ДО add — дороже (в 2 раза больше вызовов ipset), но
# при 2-минутном интервале и типичном размере групп это по-прежнему
# секунды, а не минуты; ценность точного учёта "что реально появилось
# нового" перевешивает.
total_groups=0
total_domains=0
total_new=0

refresh_state_file() { # <state.json> <ipset-prefix>
  local state=$1 prefix=$2
  [ -f "$state" ] || return 0
  while IFS=$'\t' read -r gid domains_csv; do
    [ -n "$gid" ] || continue
    [ -n "$domains_csv" ] || continue
    local ipset="${prefix}${gid}"
    ipset list -n 2>/dev/null | grep -qx "$ipset" || continue # группа без ipset (ещё не создан) — пропускаем, не наше дело его создавать
    IFS=',' read -ra domains <<< "$domains_csv"
    local resolved new_ips=() new_count=0
    resolved=$(printf '%s\n' "${domains[@]}" | xargs -P 16 -I{} getent ahostsv4 {} 2>/dev/null | awk '{print $1}' | sort -u)
    while read -r ip; do
      [ -n "$ip" ] || continue
      if ! ipset test "$ipset" "$ip" >/dev/null 2>&1; then
        new_ips+=("$ip")
        new_count=$((new_count+1))
      fi
      ipset add "$ipset" "$ip" -exist
    done <<< "$resolved"
    total_groups=$((total_groups+1))
    total_domains=$((total_domains+${#domains[@]}))
    total_new=$((total_new+new_count))
    if [ "$new_count" -gt 0 ]; then
      log "группа $ipset: доменов=${#domains[@]} новых_IP=$new_count [${new_ips[*]}]"
    fi
  done < <(python3 -c "
import json
for g in json.load(open('$state')):
    print(g['group_id']+'\t'+','.join(g.get('domains', [])))
")
}

refresh_state_file "$STATE" "$IPSET_PREFIX_ZAPRET"
refresh_state_file "$CSTATE" "$IPSET_PREFIX_CIADPI"
refresh_state_file "$Z2STATE" "$IPSET_PREFIX_ZAPRET2"

log "готово: групп=$total_groups доменов=$total_domains новых_IP=$total_new"
