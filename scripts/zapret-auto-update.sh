#!/usr/bin/env bash
# zapret-auto-update.sh — еженедельное автообновление движка zapret (nfqws/mdig/tpws)
# из апстрима bol-van/zapret. Тот же путь, что и ручная кнопка «Обновить движок» в UI
# (gateway-ui/zapret.go handleZapretUpdate), только без HTTP — для systemd-таймера.
#
# ВАЖНО (CANON #20): zapret.service флашит mangle POSTROUTING при КАЖДОМ рестарте —
# без явного restore все brain-группы (T-consolidate) осиротеют (демоны живы, правил
# нет). Поэтому restart И restore — одна неразрывная операция, никогда не разделять.
set -uo pipefail

ZAPRET_DIR=${ZAPRET_DIR:-/opt/zapret}
BRAIN_APPLY=${BRAIN_APPLY:-/opt/gateway-brain/brain-apply.sh}
LOG=${LOG:-/var/log/gateway-zupdate-auto.log}

exec >> "$LOG" 2>&1
echo "=== $(date '+%F %T') автообновление zapret ==="

cd "$ZAPRET_DIR" || { echo "нет $ZAPRET_DIR"; exit 1; }

before=$(git rev-parse --short HEAD)
git fetch --depth 1 origin || { echo "git fetch не удался"; exit 1; }
after=$(git rev-parse --short FETCH_HEAD)

if [ "$before" = "$after" ]; then
  echo "уже актуально ($before) — пропуск"
  exit 0
fi

git reset --hard FETCH_HEAD || { echo "git reset не удался"; exit 1; }
if ! (make -C nfq && make -C mdig && make -C tpws); then
  echo "сборка не удалась, откатываю на $before и пересобираю"
  git reset --hard "$before"
  make -C nfq && make -C mdig && make -C tpws
  exit 1
fi

systemctl restart zapret.service
sleep 2
bash "$BRAIN_APPLY" restore

echo "готово: $before -> $after"
