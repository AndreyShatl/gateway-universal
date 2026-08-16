#!/usr/bin/env bash
# ensure-ipsets.sh (T-boot-masquerade, 2026-08-16) — создать (пустыми, если
# не существуют) все ipset'ы, на которые ссылаются сохранённые iptables-
# правила, ДО того как netfilter-persistent попытается их restore.
#
# Живой инцидент: после ребута Pi MASQUERADE-правило (и вообще ВСЕ
# сохранённые правила) пропали — iptables-restore атомарно откатывает ВЕСЬ
# файл при первой же ошибке, а один из наших ipset-based match-set правил
# (gw_autoroute/gw_direct_fastpath/brainc_grpc_*) ссылался на ipset,
# который в этот момент ещё не существовал (создаётся позже другими
# сервисами — gateway-detector/brain-apply.sh — при их собственном старте,
# не гарантированно раньше netfilter-persistent). Пустой ipset достаточно —
# членство (реальные IP) владеющий сервис заполнит сам при своём запуске,
# нам важно только чтобы iptables-restore не упал НА ССЫЛКЕ.
# НЕ set -e/pipefail: этот скрипт по дизайну best-effort - должен ВСЕГДА
# завершаться успешно (exit 0), что бы ни случилось. Живой баг (2026-08-16,
# второй ребут 132): rules.v4 без match-set-строк (пустой/старый файл) ->
# grep exit 1 (ничего не нашёл) -> с pipefail это валило весь unit ->
# netfilter-persistent (RequiredBy=) вообще отказывался стартовать -> хуже
# исходной проблемы (раньше хотя бы часть правил восстанавливалась).
set -u

RULES=/etc/iptables/rules.v4
if [ -f "$RULES" ]; then
  for set in $(grep -oE 'match-set [a-zA-Z0-9_]+' "$RULES" 2>/dev/null | awk '{print $2}' | sort -u); do
    ipset create "$set" hash:net family inet -exist 2>/dev/null || true
  done
fi
exit 0
