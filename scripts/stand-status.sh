#!/usr/bin/env bash
# stand-status.sh — компактный снимок стенда (~10 строк) вместо простыней команд.
# Стенд: env STAND (по умолчанию root@192.168.1.132).
set -euo pipefail
STAND="${STAND:-root@192.168.1.132}"

ssh "$STAND" 'bash -s' <<'REMOTE'
set -e
svc() { printf "%s=%s " "$1" "$(systemctl is-active "$2" 2>/dev/null)"; }
echo -n "services: "; svc xray xray; svc ui gateway-ui; svc det gateway-detector; svc zapret zapret; svc dns dnscrypt-proxy; echo
echo "recheck.timer: $(systemctl is-active gateway-recheck.timer 2>/dev/null) next=$(systemctl show gateway-recheck.timer -p NextElapseUSecRealtime --value 2>/dev/null)"
echo "tunnel ESTAB(:2083)=$(ss -tnp 2>/dev/null | grep -c 2083)  vps-exit=$(curl -s --max-time 6 --socks5-hostname 127.0.0.1:1081 https://api.ipify.org 2>/dev/null)  direct=$(curl -s --max-time 6 https://api.ipify.org 2>/dev/null)"
echo "iptables autoroute: nat=$(iptables -t nat -S PREROUTING 2>/dev/null | grep -c gw_autoroute) mangle=$(iptables -t mangle -S PREROUTING 2>/dev/null | grep -c gw_autoroute)  ipset=$(ipset list gw_autoroute 2>/dev/null | grep -cE '^[0-9]')"
python3 - <<'PY'
import json
try:
    a=json.load(open("/etc/gateway/autoroute.json"))
    src={}
    for e in a["entries"]:
        s=e.get("source","?") if isinstance(e,dict) else "legacy"
        src[s]=src.get(s,0)+1
    print("autoroute: enabled=%s total=%d %s" % (a.get("enabled"), len(a["entries"]), dict(sorted(src.items(),key=lambda x:-x[1]))))
except Exception as e:
    print("autoroute: n/a (%s)" % e)
try:
    r=json.load(open("/etc/gateway/recheck.json"))
    print("recheck: enabled=%s %s@%s workers=%d  last: run=%s checked=%s removed=%s %ss" % (
        r.get("enabled"), r.get("days"), r.get("time"), r.get("workers",0),
        r.get("last_run","-"), r.get("last_checked","-"), r.get("last_removed","-"), r.get("last_duration_sec","-")))
except Exception as e:
    print("recheck: n/a (%s)" % e)
PY
REMOTE
