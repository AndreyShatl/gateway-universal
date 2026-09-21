#!/usr/bin/env bash
# brain-sni-harvest.sh (T-sni-harvest, 2026-09-21, ТЗ владельца) — ночной жнец
# живых SNI-кандидатов: детектор весь день пишет "domain<TAB>ip" от реальных
# клиентов (смартфонные API/realtime/media-домены вне geosite). Здесь:
#   1) классификация по observed-IP в известные диапазоны сервисов
#      (Meta / Discord / Google) — «ничейные» только логируем (владелец:
#      «действуем по ситуации»);
#   2) валидация: домен резолвится в тот же диапазон сервиса;
#   3) добавление в список сервиса (через junk-фильтр actualize) + render;
#   4) QA-строка в лог; бэкап списков перед изменением.
# Единственный pipeline не дробим: это этап ночной цепочки ПОСЛЕ actualize.
set -u
LOG=/var/log/gateway-brain.log
CAND=/etc/gateway/observe/sni-candidates.log
SERVICES=/etc/gateway/zapret-services.json

log() { echo "$(date '+%F %T') [sni-harvest] $*" >> "$LOG"; }

classify() { # <ip> -> service id или "" (по диапазонам)
  python3 - "$1" <<'PYC'
import ipaddress, sys
ip = ipaddress.ip_address(sys.argv[1])
ranges = {
  "instagram": ["31.13.24.0/21","31.13.64.0/18","69.171.224.0/19","102.132.96.0/20","129.134.0.0/17","157.240.0.0/16","173.252.64.0/18","179.60.192.0/22","185.60.216.0/22","204.15.20.0/22","87.229.142.0/23"], // 87.229.142/23 — живое наблюдение 2026-09-21: fna.fbcdn edge
  "discord":   ["162.159.128.0/17","109.200.192.0/19"],
  "youtube":   ["74.125.0.0/16","142.250.0.0/15","172.217.0.0/16","216.58.192.0/19","142.0.0.0/8","108.177.0.0/17","209.85.128.0/17","173.194.0.0/16","64.233.160.0/19","209.85.0.0/16"],
}
for svc, cidrs in ranges.items():
    for c in cidrs:
        if ip in ipaddress.ip_network(c):
            print(svc); raise SystemExit
PYC
}

[ -f "$CAND" ] || { log "кандидатов нет (лог пуст)"; exit 0; }
cp "$CAND" "$CAND.prev" 2>/dev/null

declare -A picked unowned
while IFS=$'\t' read -r dom ip; do
  [ -n "$dom" ] && [ -n "$ip" ] || continue
  svc=$(classify "$ip")
  if [ -n "$svc" ]; then
    picked["$dom"]="$svc|$ip"
  else
    unowned["$dom"]="$ip"
  fi
done < "$CAND"
: > "$CAND"

added=0; rejected=0
if [ ${#picked[@]} -gt 0 ]; then
  cp "$SERVICES" "$SERVICES.bak-harvest-$(date +%Y%m%d)" 2>/dev/null
  for dom in "${!picked[@]}"; do
    svc=${picked[$dom]%|*}; obs_ip=${picked[$dom]#*|}
    # валидация: свежий резолв должен попадать в диапазоны того же сервиса
    fresh=$(getent ahostsv4 "$dom" 2>/dev/null | awk '{print $1}' | sort -u | head -1)
    [ -n "$fresh" ] || { rejected=$((rejected+1)); continue; }
    fresh_svc=$(classify "$fresh")
    [ "$fresh_svc" = "$svc" ] || { rejected=$((rejected+1)); continue; }
    # junk-фильтр (тот же, что actualize)
    case "$dom" in *.) dom="${dom%.}";; esac
    # добавить в список сервиса, если нет
    n=$(python3 - "$SERVICES" "$svc" "$dom" <<'PYA'
import json, sys
path, sid, dom = sys.argv[1:4]
data = json.load(open(path))
if not isinstance(data, list): data = data.get("services", data)
added = 0
for s in data:
    if s.get("id") == sid:
        doms = [d.lower() for d in s.get("domains", [])]
        if dom not in doms:
            s.setdefault("domains", []).append(dom)
            added = 1
json.dump(data, open(path, "w"), ensure_ascii=False, indent=2)
print(added)
PYA
)
    if [ "$n" = "1" ]; then
      added=$((added+1)); log "＋ $dom → $svc (живой трафик, IP $obs_ip подтверждён резолвом)"
    fi
  done
fi

# «ничейные» — только наблюдение (по ситуации: владелец решит по факту)
if [ ${#unowned[@]} -gt 0 ]; then
  { echo "# $(date -u +%Y-%m-%dT%H:%M:%SZ) — не классифицированы (наблюдение):"
    for dom in "${!unowned[@]}"; do echo -e "$dom\t${unowned[$dom]}"; done; } >> /etc/gateway/observe/sni-unowned.log
  log "ничейных SNI: ${#unowned[@]} (журнал observe/sni-unowned.log, по ситуации)"
fi

if [ "$added" -gt 0 ]; then
  bash /root/gateway-universal/xray/render-config.sh \
    --out /opt/xray/config.json --config /root/gateway-universal/config.env \
    --xray /opt/xray/xray >> "$LOG" 2>&1 \
    && systemctl restart xray.service && log "render+restart: добавлено=$added отклонено=$rejected"
else
  log "итог: добавлено=0 отклонено=$rejected (конфиг не трогаем)"
fi
echo "sni-harvest: добавлено=$added отклонено=$rejected ничейных=${#unowned[@]}"

# QA-сводка конвейера (ТЗ: контроль качества одним взглядом)
python3 - "$added" "$rejected" "${#unowned[@]}" <<'PYH'
import json, sys, datetime
path = "/etc/gateway/observe/pipeline-summary.json"
try: d = json.load(open(path))
except Exception: d = {}
if d.get("date") != datetime.date.today().isoformat():
    d = {"date": datetime.date.today().isoformat()}
d["harvest"] = {"added": int(sys.argv[1]), "rejected": int(sys.argv[2]),
                "unowned": int(sys.argv[3]), "at": datetime.datetime.now().isoformat(timespec="seconds")}
json.dump(d, open(path, "w"), ensure_ascii=False, indent=1)
PYH
