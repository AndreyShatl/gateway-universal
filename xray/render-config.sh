#!/usr/bin/env bash
# =====================================================================
# render-config.sh — собрать /opt/xray/config.json из шаблона.
# Общий код для install.sh и веб-UI (T8): единственное место, где
# рендерится конфиг xray. НЕ дублировать логику в других местах.
#
# Параметры VPS_* берутся из окружения (caller их экспортирует) или из
# файла через --config. Делает:
#   1) резолв VPS_ADDR -> VPS_ADDR_IP (для ip-правил)
#   2) сборку списка доменов роутинга (build-domains.sh)
#   3) envsubst шаблона -> выходной config.json
#   4) (опц.) xray -test
#
# Использование:
#   render-config.sh --out /opt/xray/config.json [--template F] \
#                    [--config /path/config.env] [--xray /opt/xray/xray] \
#                    [--domains-dir DIR]
# =====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"

TEMPLATE="$SCRIPT_DIR/config.template.json"
OUT=""
CONFIG=""
XRAY_BIN=""
DOMAINS_DIR="$SCRIPT_DIR/domains"
USER_DOMAINS_DIR="/etc/gateway/domains"   # домены из UI, вне репо (T9)

while [[ $# -gt 0 ]]; do
    case "$1" in
        --template)         TEMPLATE="$2"; shift 2;;
        --out)              OUT="$2"; shift 2;;
        --config)           CONFIG="$2"; shift 2;;
        --xray)             XRAY_BIN="$2"; shift 2;;
        --domains-dir)      DOMAINS_DIR="$2"; shift 2;;
        --user-domains-dir) USER_DOMAINS_DIR="$2"; shift 2;;
        *) echo "render-config: unknown option: $1" >&2; exit 2;;
    esac
done

[[ -n "$OUT" ]]            || { echo "render-config: --out required" >&2; exit 2; }
[[ -f "$TEMPLATE" ]]       || { echo "render-config: template not found: $TEMPLATE" >&2; exit 2; }

# Опционально подгрузить config.env
if [[ -n "$CONFIG" ]]; then
    [[ -f "$CONFIG" ]] || { echo "render-config: config not found: $CONFIG" >&2; exit 2; }
    # shellcheck disable=SC1090
    set -a; source "$CONFIG"; set +a
fi

: "${VPS_ADDR:=}"
[[ -n "$VPS_ADDR" ]] || { echo "render-config: VPS_ADDR не задан (env или --config)" >&2; exit 1; }

# 1) Резолв VPS_ADDR -> IP (xray ждёт IP/CIDR в ip-правилах)
if [[ "${VPS_ADDR_IP:-}" == "" ]]; then
    if [[ "$VPS_ADDR" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        VPS_ADDR_IP="$VPS_ADDR"
    else
        VPS_ADDR_IP="$(getent ahostsv4 "$VPS_ADDR" 2>/dev/null | awk 'NR==1{print $1}')"
        [[ -n "$VPS_ADDR_IP" ]] || { echo "render-config: не резолвится VPS_ADDR=$VPS_ADDR" >&2; exit 1; }
        echo "  resolved $VPS_ADDR -> $VPS_ADDR_IP"
    fi
fi

# 2) Список доменов роутинга (репо + пользовательские из UI; второй каталог
#    может отсутствовать — build-domains.sh его пропустит)
ROUTING_DOMAINS="$(bash "$SCRIPT_DIR/build-domains.sh" "$DOMAINS_DIR" "$USER_DOMAINS_DIR")" \
    || { echo "render-config: build-domains.sh failed" >&2; exit 1; }
echo "  routing domains: $(printf '%s\n' "$ROUTING_DOMAINS" | grep -c .) entries"

# 3) Рендер шаблона
export VPS_ADDR VPS_ADDR_IP VPS_PORT_VISION VPS_PORT_GRPC VPS_UUID_VISION VPS_UUID_GRPC
export VPS_PUBKEY VPS_SHORT_ID VPS_SERVER_NAME VPS_FINGERPRINT ROUTING_DOMAINS
# Временный файл рядом с целью и с расширением .json — xray -test определяет
# формат по расширению, без .json он падает с "failed to get format".
mkdir -p "$(dirname "$OUT")"
TMP_OUT="${OUT}.tmp.$$.json"
trap 'rm -f "$TMP_OUT"' EXIT
envsubst < "$TEMPLATE" > "$TMP_OUT"

# 4) Проверка xray (если бинарник дан) — до подмены рабочего конфига
if [[ -n "$XRAY_BIN" && -x "$XRAY_BIN" ]]; then
    if ! "$XRAY_BIN" -test -config "$TMP_OUT" >/dev/null 2>&1; then
        "$XRAY_BIN" -test -config "$TMP_OUT" || true
        echo "render-config: xray config invalid" >&2; exit 1
    fi
fi

mv "$TMP_OUT" "$OUT"
trap - EXIT
echo "  rendered -> $OUT"
