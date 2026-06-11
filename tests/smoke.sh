#!/usr/bin/env bash
# =====================================================================
# smoke.sh — проверка «шлюз поднимается и работает» (T3, критерии GOALS.md).
# Один прогон — один вердикт PASS/FAIL, exit 0 (всё ок) / 1 (есть FAIL).
#
# Запуск:
#   На самом шлюзе:   bash tests/smoke.sh
#   Удалённо с Mac:   bash tests/smoke.sh --host root@192.168.1.132
#                     (ssh-доступ должен быть настроен; пароль спросит ssh)
#
# Что проверяет (см. GOALS.md):
#   G1  xray слушает tproxy :12345  и  socks :1080
#   G2  VLESS-туннель работает (egress через VPS != прямой провайдерский IP)
#   G3  nfqws запущен
#   G4  NFQUEUE-правила есть
#   +   сервисы xray/zapret active
# =====================================================================
set -uo pipefail

# ---- Удалённый режим: перезапустить себя на шлюзе через ssh ----------
HOST=""
[[ "${1:-}" == "--host" ]] && { HOST="$2"; shift 2; }
if [[ -n "$HOST" ]]; then
    exec ssh -o ConnectTimeout=12 "$HOST" 'bash -s' < "$0"
fi

# ---- Локальный режим (на шлюзе) -------------------------------------
if [[ -t 1 ]]; then GRN=$'\033[32m'; RED=$'\033[31m'; YEL=$'\033[33m'; OFF=$'\033[0m'
else GRN=; RED=; YEL=; OFF=; fi

PASS=0; FAIL=0
ok()   { printf "${GRN}PASS${OFF} %s\n" "$1"; PASS=$((PASS+1)); }
bad()  { printf "${RED}FAIL${OFF} %s\n" "$1"; FAIL=$((FAIL+1)); }
note() { printf "${YEL}  ·${OFF} %s\n" "$1"; }

# G1 — сервисы и порты
systemctl is-active --quiet xray.service   && ok "xray.service active"        || bad "xray.service не active"
systemctl is-active --quiet zapret.service && ok "zapret.service active"      || bad "zapret.service не active"
ss -tlnp 2>/dev/null | grep -q ':12345 '   && ok "G1 xray слушает :12345"     || bad "G1 xray НЕ слушает :12345"
ss -tlnp 2>/dev/null | grep -q ':1080 '    && ok "G1 socks :1080"             || bad "G1 socks :1080 не слушает"

# G3 — nfqws
NFQ=$(pgrep -x nfqws | wc -l | tr -d ' ')
[[ "$NFQ" -ge 1 ]] && ok "G3 nfqws запущен ($NFQ проц.)"                       || bad "G3 nfqws не запущен"

# G4 — NFQUEUE правила
iptables -t mangle -L POSTROUTING -n 2>/dev/null | grep -q NFQUEUE \
    && ok "G4 NFQUEUE-правила есть"                                           || bad "G4 NFQUEUE-правил нет"

# G2 — туннель работает: egress через прокси != прямой провайдерский egress
DIRECT=$(curl -sS -4 --max-time 8 https://api.ipify.org 2>/dev/null)
VIATUN=$(curl -sS --max-time 12 --socks5-hostname 127.0.0.1:1080 \
            https://www.cloudflare.com/cdn-cgi/trace 2>/dev/null | sed -n 's/^ip=//p')
if [[ -z "$VIATUN" ]]; then
    bad "G2 туннель: не удалось получить egress через socks"
elif [[ -n "$DIRECT" && "$VIATUN" == "$DIRECT" ]]; then
    bad "G2 туннель: egress через прокси == прямой ($DIRECT) — трафик НЕ идёт через VPS"
else
    ok "G2 туннель работает (direct=${DIRECT:-?}, через VPS=$VIATUN)"
fi

# gateway-ui (если установлен) — сервис active + /healthz
if [[ -f /etc/systemd/system/gateway-ui.service ]]; then
    systemctl is-active --quiet gateway-ui.service && ok "gateway-ui.service active" || bad "gateway-ui.service не active"
    PORT="$(sed -n 's/.*--listen :\([0-9]*\).*/\1/p' /etc/systemd/system/gateway-ui.service | head -1)"
    LANIP="$(ip -o -4 addr show 2>/dev/null | awk '$4 ~ /^192\.168\.|^10\.|^172\.(1[6-9]|2[0-9]|3[01])\./{sub(/\/.*/,"",$4); print $4; exit}')"
    if [[ -n "$PORT" && -n "$LANIP" ]]; then
        curl -s --max-time 4 "http://$LANIP:$PORT/healthz" | grep -q ok \
            && ok "gateway-ui /healthz (LAN)" || bad "gateway-ui /healthz недоступен с LAN"
    fi
else
    note "gateway-ui не установлен — пропуск"
fi

echo "-----------------------------------------------"
if [[ "$FAIL" -eq 0 ]]; then
    printf "${GRN}SMOKE PASS${OFF} — проверок: %d\n" "$PASS"; exit 0
else
    printf "${RED}SMOKE FAIL${OFF} — PASS=%d FAIL=%d\n" "$PASS" "$FAIL"; exit 1
fi
