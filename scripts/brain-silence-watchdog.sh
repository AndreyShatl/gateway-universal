#!/usr/bin/env bash
# Сторож "тихого молчания мозга" (2026-09-07, после инцидента 26.08–07.09:
# мозг молчал 12 дней из-за переполненного диска — воркер был active, очередь
# пуста, и простой был невидим без ручного взгляда в history).
#
# Проверяет два инварианта:
#   1. brain-worker запущен (иначе сработает его собственный Restart, но
#      если он в состоянии failed/activating слишком долго — это тоже тихая смерть);
#   2. в gateway.db появляются свежие записи history: ночная цепочка
#      (recheck -> nightly -> ...) ставит ВСЕ управляемые домены в очередь
#      каждую ночь, значит history должна пополняться минимум раз в сутки.
#      Порог THRESHOLD_H (по умолчанию 26ч) покрывает опоздание ночной цепочки.
#
# При нарушении — exit 1: юнит уходит в failed и становится виден в
# systemctl --failed / gateway-ui / GMP-дашборде. Следующий успешный прогон
# (таймер раз в час) сам переводит юнит из failed в inactive — ручной
# reset-failed не нужен.
#
# Read-only: ничего не чинит, не пишет в БД (открытие SQLite в RO).

set -u
DB="${DB:-/etc/gateway/gateway.db}"
THRESHOLD_H="${THRESHOLD_H:-26}"

fail() { echo "BRAIN-SILENCE-WATCHDOG: $1" >&2; exit 1; }

# --- 1. brain-worker жив? ---
if ! systemctl is-active --quiet gateway-brain-worker.service; then
    state=$(systemctl is-active gateway-brain-worker.service 2>/dev/null || echo unknown)
    fail "gateway-brain-worker не active (состояние: ${state})"
fi

# --- 2. history свежая? ---
[ -r "$DB" ] || fail "БД недоступна для чтения: $DB"

last=$(python3 - "$DB" <<'PYEOF'
import sqlite3, sys
try:
    c = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
    row = c.execute("select max(tested_at) from history").fetchone()
    print(row[0] or "")
except Exception as e:
    print(f"ERR:{e}", file=sys.stderr)
    sys.exit(2)
PYEOF
) || fail "Не удалось прочитать history (БД битая или занята?): см. stderr"

[ -n "$last" ] || fail "history пуста — ни одной записи вообще"

age_h=$(python3 - "$last" <<'PYEOF'
import sys
from datetime import datetime, timezone
t = datetime.strptime(sys.argv[1][:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
print(int((datetime.now(timezone.utc) - t).total_seconds() // 3600))
PYEOF
) || fail "Не удалось разобрать timestamp последней записи: ${last}"

if [ "$age_h" -ge "$THRESHOLD_H" ]; then
    fail "Мозг молчит: последняя запись history ${age_h}ч назад (${last}), порог ${THRESHOLD_H}ч. Ночная цепочка не пишет — проверить диск/brain-nightly/brain-queue."
fi

echo "OK: brain-worker active, последняя запись history ${age_h}ч назад (${last})"
