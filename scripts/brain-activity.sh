#!/usr/bin/env bash
# brain-activity.sh — T53: снэпшот пакетных счётчиков NFQUEUE-правил сущностей
# «мозга», обновление last_active при росте счётчика. Ничего не останавливает —
# только копит данные для idle-стопа (T54, brain-nightly.sh).
# Запуск: systemd gateway-brain-activity.timer (почасово).
set -uo pipefail

STATE=/etc/gateway/brain-services.json
LOG=/var/log/gateway-brain.log

[ -f "$STATE" ] || exit 0

# packet-счётчики NFQUEUE-правил POSTROUTING: "q:pkts,q:pkts,..." одной строкой
# (проще и надёжнее, чем растаскивать по позиционным аргументам python3).
snapshot=$(iptables -t mangle -L POSTROUTING -n -v -x 2>/dev/null \
  | awk '/NFQUEUE/ { for (i=1;i<=NF;i++) if ($i=="num") printf "%s:%s,", $(i+1), $1 }')

now=$(date '+%Y-%m-%dT%H:%M:%SZ')
python3 - "$STATE" "$now" "$snapshot" <<'PY'
import json, sys

state_path, now, snapshot = sys.argv[1], sys.argv[2], sys.argv[3]
current = {}
for pair in snapshot.strip(",").split(","):
    if not pair:
        continue
    q, p = pair.split(":")
    current[q] = int(p)

data = json.load(open(state_path))
changed = False
for e in data:
    q = str(e.get("queue", ""))
    new_pkts = int(current.get(q, e.get("packets", 0)))
    old_pkts = int(e.get("packets", 0))
    if new_pkts > old_pkts:
        e["packets"] = new_pkts
        e["last_active"] = now
        changed = True
    elif "packets" not in e:
        e["packets"] = new_pkts
        changed = True
    if "last_active" not in e:
        e["last_active"] = now
        changed = True

if changed:
    json.dump(data, open(state_path, "w"), ensure_ascii=False, indent=1)
PY

echo "$(date '+%F %T') 📊 активность: снэпшот счётчиков обновлён" >> "$LOG"
