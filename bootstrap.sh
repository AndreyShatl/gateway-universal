#!/usr/bin/env bash
# =====================================================================
# bootstrap.sh — точка входа для установки одной командой.
#
# install.sh сам по себе не самодостаточен (нужны остальные файлы репозитория
# рядом — xray/config.template.json, zapret/, systemd/*, scripts/* и т.д.),
# поэтому curl+bash одного install.sh не работает. Этот скрипт клонирует ВЕСЬ
# репозиторий в фиксированное место и запускает install.sh оттуда.
#
# Использование (см. README):
#   curl -fsSL https://raw.githubusercontent.com/AndreyShatl/gateway-universal/main/bootstrap.sh | sudo bash
#
# Фиксированное место клонирования — /root/gateway-universal — важно: часть
# systemd-юнитов (мозг, авто-обновление zapret) ссылается на этот путь напрямую.
# =====================================================================
set -euo pipefail

REPO_URL="${REPO_URL:-https://github.com/AndreyShatl/gateway-universal.git}"
REPO_DIR="${REPO_DIR:-/root/gateway-universal}"
BRANCH="${BRANCH:-main}"

[[ $EUID -eq 0 ]] || { echo "Запусти от root (sudo bash bootstrap.sh)" >&2; exit 1; }

if ! command -v git >/dev/null 2>&1; then
    echo "==> Installing git…"
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq && apt-get install -y -qq git
fi

if [[ -d "$REPO_DIR/.git" ]]; then
    echo "==> $REPO_DIR уже существует — обновляю…"
    git -C "$REPO_DIR" fetch --depth 1 origin "$BRANCH"
    git -C "$REPO_DIR" reset --hard "origin/$BRANCH"
else
    echo "==> Клонирую $REPO_URL в $REPO_DIR…"
    git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$REPO_DIR"
fi

cd "$REPO_DIR"
exec bash install.sh "$@"
