#!/usr/bin/env bash
# =====================================================================
# build-release.sh — кросс-компиляция gateway-ui под архитектуры Linux,
# которые поддерживает install.sh. Имена ассетов = uname -m целевой
# машины (install качает gateway-ui-$(uname -m) из релиза).
#
# Использование:
#   bash gateway-ui/build-release.sh            # -> dist/
#   потом: gh release create <tag> dist/gateway-ui-*
# =====================================================================
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"

OUT="dist"
mkdir -p "$OUT"

# uname-m -> GOARCH (+GOARM)
build() { # <asset-suffix> <GOARCH> [GOARM]
    local suffix="$1" goarch="$2" goarm="${3:-}"
    echo "==> gateway-ui-$suffix (GOARCH=$goarch ${goarm:+GOARM=$goarm})"
    CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" \
        go build -trimpath -ldflags="-s -w" -o "$OUT/gateway-ui-$suffix" .
}

build x86_64  amd64
build aarch64 arm64
build armv7l  arm 7

echo "Готово:"
ls -la "$OUT"/gateway-ui-*
