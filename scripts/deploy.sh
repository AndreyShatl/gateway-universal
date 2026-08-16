#!/usr/bin/env bash
# deploy.sh — синхронизировать исходники на стенд, собрать там и перезапустить.
# Компактный вывод (PASS/FAIL по шагам) — для быстрых итераций и экономии токенов.
#
#   scripts/deploy.sh detector      # gateway-detector (CGO/libpcap)
#   scripts/deploy.sh ui            # gateway-ui (go:embed)
#   scripts/deploy.sh xray          # рендер шаблона -> xray -test -> подмена+рестарт
#   scripts/deploy.sh brain         # scripts/*.sh -> /opt/gateway-brain/ (T-deploy-drift)
#   scripts/deploy.sh all           # detector + ui
#
# Стенд: env STAND (по умолчанию root@192.168.1.132), SRC_REMOTE=/opt/gateway-src.
set -euo pipefail

STAND="${STAND:-root@192.168.1.132}"
SRC_REMOTE="${SRC_REMOTE:-/opt/gateway-src}"
LOCAL="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
comp="${1:-}"

say() { printf '%-22s %s\n' "$1" "$2"; }
die() { echo "FAIL: $*" >&2; exit 1; }

sync_dir() { # локальный подкаталог -> стенд
  scp -q -r "$LOCAL/$1" "$STAND:$SRC_REMOTE/$(dirname "$1")/" || die "scp $1"
}

deploy_detector() {
  sync_dir detector
  ssh "$STAND" "cd $SRC_REMOTE/detector && go build -o /tmp/gw-det . && install -m755 /tmp/gw-det /opt/gateway-detector && systemctl restart gateway-detector" \
    || die "detector build/restart"
  say detector "PASS ($(ssh "$STAND" 'systemctl is-active gateway-detector'))"
}

deploy_ui() {
  sync_dir gateway-ui
  ssh "$STAND" "cd $SRC_REMOTE/gateway-ui && go build -o /tmp/gw-ui . && install -m755 /tmp/gw-ui /opt/gateway-ui/gateway-ui && systemctl restart gateway-ui" \
    || die "ui build/restart"
  say ui "PASS ($(ssh "$STAND" 'systemctl is-active gateway-ui'))"
}

deploy_brain() {
  # T-deploy-drift (2026-08-16): именно ЭТОТ путь ("поправил в репо, забыл
  # задеплоить в /opt/gateway-brain") давал реальные инциденты трижды за одну
  # ночь (solve.sh/zapret2, brain-domain-actualize.sh, brain-healthcheck.sh —
  # тихо ломались, exit=203/EXEC, никто не заметил бы без явной проверки).
  # Тот же принцип, что теперь в install.sh: копируем ВСЁ scripts/*.sh кроме
  # dev-инструментов, не гадаем, что могли забыть добавить в список.
  local skip="deploy.sh discord-voice-test.sh stand-status.sh"
  ssh "$STAND" "mkdir -p /opt/gateway-brain" || die "brain mkdir"
  for f in "$LOCAL"/scripts/*.sh; do
    b="$(basename "$f")"
    [[ " $skip " == *" $b "* ]] && continue
    scp -q "$f" "$STAND:/opt/gateway-brain/$b" || die "scp $b"
  done
  scp -q "$LOCAL/scripts/gwdb.py" "$STAND:/opt/gateway-brain/gwdb.py" || die "scp gwdb.py"
  ssh "$STAND" "chmod +x /opt/gateway-brain/*.sh" || die "chmod"
  say brain "PASS (synced $(ls "$LOCAL"/scripts/*.sh | wc -l | tr -d ' ') scripts + gwdb.py)"
}

deploy_xray() {
  sync_dir xray
  # рендер в temp -> валидация -> бэкап -> подмена -> рестарт -> проверка туннеля
  ssh "$STAND" 'set -e
    cd /root/gateway-universal
    cp '"$SRC_REMOTE"'/xray/config.template.json xray/config.template.json
    bash xray/render-config.sh --out /tmp/cfg.json --config /root/gateway-universal/config.env >/dev/null
    /opt/xray/xray -test -c /tmp/cfg.json >/dev/null || { echo "xray -test FAIL"; exit 1; }
    cp /opt/xray/config.json /opt/xray/config.json.bak-$(date +%s)
    cp /tmp/cfg.json /opt/xray/config.json
    systemctl restart xray; sleep 3
    ss -tnp 2>/dev/null | grep -q 2083 || { echo "tunnel down"; exit 1; }' \
    || die "xray render/test/restart"
  say xray "PASS (tunnel up)"
}

case "$comp" in
  detector) deploy_detector ;;
  ui)       deploy_ui ;;
  xray)     deploy_xray ;;
  brain)    deploy_brain ;;
  all)      deploy_detector; deploy_ui ;;
  *) echo "usage: deploy.sh {detector|ui|xray|brain|all}" >&2; exit 2 ;;
esac
