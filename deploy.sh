#!/usr/bin/env bash
# =====================================================================
# gateway-universal — удалённый деплой с Mac на целевую машину
#
# Что делает:
#   1) Проверяет SSH-доступ к целевой машине (root@ip + пароль)
#   2) Синхронизирует репозиторий в /root/gateway-universal/
#   3) Пробрасывает config.env (локальный) или запускает install.sh
#      с переменными окружения
#   4) Забирает статус и показывает сводку
#
# Использование:
#   # Интерактивно (спросит IP, пароль, VPS-параметры)
#   bash deploy.sh
#
#   # С флагами
#   bash deploy.sh --host 192.168.1.69 --password 'Pa$$w0rd'
#
#   # Полностью автоматизированно (после того как config.env заполнен)
#   bash deploy.sh --host 192.168.1.69 --password 'xxx' --config ./config.env --yes
#
# Требования:
#   • На Mac: ssh, sshpass, rsync (brew install sshpass hudochenkov/sshpass/sshpass)
#   • На target: Debian/Ubuntu, доступ root
# =====================================================================

set -euo pipefail

# ---------- Цвета ----------------------------------------------------
if [[ -t 1 ]]; then
    C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YEL=$'\033[33m'
    C_BLU=$'\033[34m'; C_CYN=$'\033[36m'; C_BLD=$'\033[1m'; C_OFF=$'\033[0m'
else
    C_RED=; C_GRN=; C_YEL=; C_BLU=; C_CYN=; C_BLD=; C_OFF=
fi
say()  { printf "${C_CYN}==>${C_OFF} %s\n" "$*"; }
ok()   { printf "${C_GRN}✓${C_OFF} %s\n" "$*"; }
warn() { printf "${C_YEL}⚠${C_OFF} %s\n" "$*" >&2; }
die()  { printf "${C_RED}✗${C_OFF} %s\n" "$*" >&2; exit 1; }

# ---------- Пути -----------------------------------------------------
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"

# ---------- Defaults -------------------------------------------------
HOST=""
SSH_USER="root"
SSH_PORT="22"
PASSWORD=""
KEY=""
CONFIG_FILE="${SCRIPT_DIR}/config.env"
YES=no
REMOTE_DIR="/root/gateway-universal"
PASS_INSTALL_FLAGS=""

# ---------- CLI ------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --host)      HOST="$2"; shift 2;;
        --user)      SSH_USER="$2"; shift 2;;
        --port)      SSH_PORT="$2"; shift 2;;
        --password)  PASSWORD="$2"; shift 2;;
        --key)       KEY="$2"; shift 2;;
        --config)    CONFIG_FILE="$2"; shift 2;;
        --yes|-y)    YES=yes; shift;;
        --uninstall) PASS_INSTALL_FLAGS="UNINSTALL"; shift;;
        --)          shift; PASS_INSTALL_FLAGS="$*"; break;;
        -h|--help)
            sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        *) die "Unknown flag: $1";;
    esac
done

# ---------- Проверки Mac-side ----------------------------------------
command -v ssh   >/dev/null || die "ssh not found"
command -v rsync >/dev/null || die "rsync not found (brew install rsync)"

# ---------- Опрос ----------------------------------------------------
if [[ -z "$HOST" ]]; then
    read -r -p "${C_BLU}?${C_OFF} Target host (root@IP или IP): " HOST
fi
# Разобрать user@host
if [[ "$HOST" == *"@"* ]]; then
    SSH_USER="${HOST%%@*}"
    HOST="${HOST##*@}"
fi
[[ -n "$HOST" ]] || die "Host required"

# Как аутентифицируемся?
SSH_AUTH_CMD=()
if [[ -n "$KEY" ]]; then
    SSH_AUTH_CMD=(-i "$KEY" -o PubkeyAuthentication=yes)
elif [[ -n "$PASSWORD" ]]; then
    command -v sshpass >/dev/null || die "sshpass required for password auth (brew install hudochenkov/sshpass/sshpass)"
    SSH_AUTH_CMD=(sshpass -p "$PASSWORD")
else
    # Предложить ввести пароль, если нет ключа
    if [[ $YES == yes ]]; then
        die "Need --password or --key in --yes mode"
    fi
    read -r -s -p "${C_BLU}?${C_OFF} SSH password for ${SSH_USER}@${HOST} (или Enter для ключа): " PASSWORD
    echo
    if [[ -n "$PASSWORD" ]]; then
        command -v sshpass >/dev/null || die "sshpass required (brew install hudochenkov/sshpass/sshpass)"
        SSH_AUTH_CMD=(sshpass -p "$PASSWORD")
    fi
fi

# Обёртки
ssh_exec() {
    if [[ ${#SSH_AUTH_CMD[@]} -gt 0 && "${SSH_AUTH_CMD[0]}" == "sshpass" ]]; then
        "${SSH_AUTH_CMD[@]}" ssh -o StrictHostKeyChecking=accept-new -o PubkeyAuthentication=no -p "$SSH_PORT" "${SSH_USER}@${HOST}" "$@"
    else
        ssh "${SSH_AUTH_CMD[@]}" -o StrictHostKeyChecking=accept-new -p "$SSH_PORT" "${SSH_USER}@${HOST}" "$@"
    fi
}

rsync_push() {
    local src="$1" dst="$2"
    if [[ ${#SSH_AUTH_CMD[@]} -gt 0 && "${SSH_AUTH_CMD[0]}" == "sshpass" ]]; then
        "${SSH_AUTH_CMD[@]}" rsync -az --delete \
            --exclude=.git --exclude=config.env --exclude='*.bak.*' \
            -e "ssh -o StrictHostKeyChecking=accept-new -o PubkeyAuthentication=no -p $SSH_PORT" \
            "$src" "${SSH_USER}@${HOST}:$dst"
    else
        rsync -az --delete \
            --exclude=.git --exclude=config.env --exclude='*.bak.*' \
            -e "ssh ${SSH_AUTH_CMD[*]} -o StrictHostKeyChecking=accept-new -p $SSH_PORT" \
            "$src" "${SSH_USER}@${HOST}:$dst"
    fi
}

# ---------- 1. SSH проверка ------------------------------------------
say "Testing SSH to ${SSH_USER}@${HOST}:${SSH_PORT}…"
TARGET_OS="$(ssh_exec 'cat /etc/os-release | grep -E "^(ID|VERSION_ID)=" | head -2 | paste -sd, -' 2>&1)" \
    || die "SSH failed. Check credentials."
ok "Connected. Target: $TARGET_OS"

TARGET_ARCH="$(ssh_exec 'uname -m')"
ok "Architecture: $TARGET_ARCH"

# ---------- 2. Подготовка config.env локально ------------------------
if [[ ! -f "$CONFIG_FILE" ]]; then
    warn "config.env не найден ($CONFIG_FILE)"
    if [[ $YES == yes ]]; then
        die "Need config.env in --yes mode"
    fi
    read -r -p "Создать из config.example.env и открыть в редакторе? [Y/n] " yn
    if [[ ! "${yn,,}" =~ ^(n|no)$ ]]; then
        cp "$SCRIPT_DIR/config.example.env" "$CONFIG_FILE"
        "${EDITOR:-nano}" "$CONFIG_FILE"
    fi
fi

# ---------- 3. Синхронизация -----------------------------------------
say "Syncing repo → ${HOST}:${REMOTE_DIR}"
ssh_exec "mkdir -p $REMOTE_DIR"
rsync_push "$SCRIPT_DIR/" "$REMOTE_DIR/"
# отдельно закинуть config.env, если есть локально
if [[ -f "$CONFIG_FILE" ]]; then
    if [[ ${#SSH_AUTH_CMD[@]} -gt 0 && "${SSH_AUTH_CMD[0]}" == "sshpass" ]]; then
        "${SSH_AUTH_CMD[@]}" scp -o StrictHostKeyChecking=accept-new -o PubkeyAuthentication=no -P "$SSH_PORT" "$CONFIG_FILE" "${SSH_USER}@${HOST}:${REMOTE_DIR}/config.env" >/dev/null
    else
        scp "${SSH_AUTH_CMD[@]}" -o StrictHostKeyChecking=accept-new -P "$SSH_PORT" "$CONFIG_FILE" "${SSH_USER}@${HOST}:${REMOTE_DIR}/config.env" >/dev/null
    fi
    ok "config.env uploaded"
fi

# ---------- 4. Запуск install.sh --------------------------------------
if [[ "$PASS_INSTALL_FLAGS" == "UNINSTALL" ]]; then
    say "Running uninstall.sh remotely…"
    ssh_exec "cd $REMOTE_DIR && bash uninstall.sh --yes" || die "uninstall failed"
    ok "Uninstalled"
    exit 0
fi

INSTALL_CMD="cd $REMOTE_DIR && bash install.sh"
if [[ $YES == yes ]]; then
    INSTALL_CMD+=" --non-interactive"
fi

say "Running install.sh remotely…"
echo "---"
ssh_exec "$INSTALL_CMD" || die "install.sh failed (see output above)"
echo "---"

# ---------- 5. Проверка после установки -------------------------------
say "Post-install verification…"
ssh_exec "
    echo '=== systemd status ==='
    systemctl is-active xray.service    || true
    systemctl is-active zapret.service  || true
    echo '=== listening ports ==='
    ss -tlnp 2>/dev/null | grep -E ':(12345|1080|8080)' || echo 'NO XRAY PORTS'
    echo '=== NFQUEUE ==='
    iptables -t mangle -L POSTROUTING -n 2>/dev/null | grep NFQUEUE | wc -l
    echo '=== nfqws ==='
    pgrep -a nfqws || echo 'nfqws not running'
"

echo
ok "Deploy complete."
cat <<EOF

${C_BLD}Управление:${C_OFF}
  ssh ${SSH_USER}@${HOST} 'systemctl restart xray zapret'
  ssh ${SSH_USER}@${HOST} 'journalctl -u xray -f'
  ssh ${SSH_USER}@${HOST} '/opt/zapret-config/zapret.sh status'

${C_BLD}Удалить:${C_OFF}
  bash deploy.sh --host $HOST --uninstall
EOF
