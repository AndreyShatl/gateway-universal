#!/usr/bin/env bash
# =====================================================================
# gateway-universal — установщик на целевом устройстве
#
# Делает из Debian/Ubuntu-машины прозрачный шлюз обхода блокировок:
#   • xray (VLESS + Reality, 2 inbound'а) — нативно, без Docker
#   • zapret (nfqws) — собран из исходников
#   • iptables NAT/FORWARD/REDIRECT/NFQUEUE + персистентность
#   • Instagram QUIC bypass (ACCEPT UDP/443 к Meta IP перед DROP)
#   • systemd сервисы xray.service / zapret.service
#
# Использование:
#   1) Локально (на самой машине):
#        sudo bash install.sh                # интерактивно
#        sudo bash install.sh --non-interactive  # из config.env / env
#
#   2) Через переменные окружения:
#        VPS_ADDR=1.2.3.4 VPS_UUID_VISION=... sudo -E bash install.sh
#
#   3) Удалённо (с Mac, через deploy.sh):
#        bash deploy.sh root@1.2.3.4
#
# Параметры берутся в таком порядке (первый найденный выигрывает):
#   1. CLI флаги  (--vps-addr, --lan, ...)
#   2. Переменные окружения (VPS_ADDR, LAN, ...)
#   3. config.env рядом со скриптом
#   4. Интерактивный ввод (если --non-interactive НЕ указан)
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
CONFIG_ENV="${SCRIPT_DIR}/config.env"

# ---------- Defaults -------------------------------------------------
: "${VPS_ADDR:=}"
: "${VPS_PORT_VISION:=443}"
: "${VPS_PORT_GRPC:=2083}"
: "${VPS_UUID_VISION:=}"
: "${VPS_UUID_GRPC:=}"
: "${VPS_PUBKEY:=}"
: "${VPS_SHORT_ID:=}"
: "${VPS_SERVER_NAME:=www.cloudflare.com}"
: "${VPS_FINGERPRINT:=chrome}"
: "${LAN:=192.168.0.0/16}"
: "${IFACE:=}"
: "${INSTALL_PREFIX:=/opt}"
: "${INSTALL_XRAY:=yes}"
: "${INSTALL_ZAPRET:=yes}"
: "${BUILD_ZAPRET_FROM_SOURCE:=yes}"
: "${INSTAGRAM_QUIC_BYPASS:=yes}"
: "${BLOCK_QUIC:=yes}"
: "${INSTALL_REVERSE_TUNNEL:=no}"
: "${VPS_TUNNEL_PORT:=2222}"
: "${INSTALL_FIX_GATEWAY:=yes}"
: "${ROUTER_IP:=}"
: "${INSTALL_WEB_UI:=yes}"
: "${WEB_UI_PORT:=8088}"
: "${WEB_UI_PASSWORD:=}"
: "${NON_INTERACTIVE:=no}"
: "${SKIP_HEALTHCHECK:=no}"

# ---------- CLI --------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --vps-addr)            VPS_ADDR="$2"; shift 2;;
        --vps-port-vision)     VPS_PORT_VISION="$2"; shift 2;;
        --vps-port-grpc)       VPS_PORT_GRPC="$2"; shift 2;;
        --vps-uuid-vision)     VPS_UUID_VISION="$2"; shift 2;;
        --vps-uuid-grpc)       VPS_UUID_GRPC="$2"; shift 2;;
        --vps-pubkey)          VPS_PUBKEY="$2"; shift 2;;
        --vps-short-id)        VPS_SHORT_ID="$2"; shift 2;;
        --vps-server-name)     VPS_SERVER_NAME="$2"; shift 2;;
        --vps-fingerprint)     VPS_FINGERPRINT="$2"; shift 2;;
        --lan)                 LAN="$2"; shift 2;;
        --iface)               IFACE="$2"; shift 2;;
        --install-prefix)      INSTALL_PREFIX="$2"; shift 2;;
        --no-xray)             INSTALL_XRAY=no; shift;;
        --no-zapret)           INSTALL_ZAPRET=no; shift;;
        --no-build-zapret)     BUILD_ZAPRET_FROM_SOURCE=no; shift;;
        --no-ig-quic)          INSTAGRAM_QUIC_BYPASS=no; shift;;
        --no-web-ui)           INSTALL_WEB_UI=no; shift;;
        --web-ui-port)         WEB_UI_PORT="$2"; shift 2;;
        --no-block-quic)       BLOCK_QUIC=no; shift;;
        --non-interactive|-y)  NON_INTERACTIVE=yes; shift;;
        --skip-healthcheck)    SKIP_HEALTHCHECK=yes; shift;;
        -h|--help)
            sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        *) die "Unknown option: $1";;
    esac
done

# ---------- Подгрузить config.env (если есть) -------------------------
if [[ -f "$CONFIG_ENV" ]]; then
    say "Reading config: $CONFIG_ENV"
    # shellcheck disable=SC1090
    set -a; source "$CONFIG_ENV"; set +a
fi

# ---------- Sanity checks ---------------------------------------------
[[ $EUID -eq 0 ]] || die "Run as root (sudo bash install.sh)"

if ! command -v apt-get >/dev/null 2>&1; then
    die "This installer supports Debian/Ubuntu only (apt-get not found)"
fi

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)  XRAY_ARCH="64";       ZAPRET_ARCH="x86_64";;
    aarch64) XRAY_ARCH="arm64-v8a"; ZAPRET_ARCH="aarch64";;
    armv7l)  XRAY_ARCH="arm32-v7a"; ZAPRET_ARCH="arm";;
    *) die "Unsupported architecture: $ARCH";;
esac
ok "Architecture: $ARCH (xray=$XRAY_ARCH)"

# ---------- Интерактивный опрос ---------------------------------------
ask() {
    # ask VAR "Prompt text" "default"
    local var="$1" prompt="$2" default="${3:-}" cur
    cur="${!var}"
    if [[ -n "$cur" ]]; then
        default="$cur"
    fi
    if [[ "$NON_INTERACTIVE" == "yes" ]]; then
        [[ -n "$default" ]] || die "Missing required value: $var (non-interactive mode)"
        printf -v "$var" '%s' "$default"
        return
    fi
    local suffix=""
    [[ -n "$default" ]] && suffix=" [${default}]"
    read -r -p "${C_BLU}?${C_OFF} ${prompt}${suffix}: " ans
    [[ -z "$ans" ]] && ans="$default"
    [[ -n "$ans" ]] || die "Value for $var is required"
    printf -v "$var" '%s' "$ans"
}

ask_secret() {
    local var="$1" prompt="$2" default="${3:-}" cur
    cur="${!var}"
    [[ -n "$cur" ]] && default="$cur"
    if [[ "$NON_INTERACTIVE" == "yes" ]]; then
        [[ -n "$default" ]] || die "Missing required value: $var (non-interactive mode)"
        printf -v "$var" '%s' "$default"
        return
    fi
    local suffix=""
    [[ -n "$default" ]] && suffix=" [${default:0:8}…]"
    read -r -s -p "${C_BLU}?${C_OFF} ${prompt}${suffix}: " ans
    echo
    [[ -z "$ans" ]] && ans="$default"
    [[ -n "$ans" ]] || die "Value for $var is required"
    printf -v "$var" '%s' "$ans"
}

if [[ "$INSTALL_XRAY" == "yes" ]]; then
    say "VPS (VLESS + Reality) — данные из 3x-ui панели"
    ask        VPS_ADDR           "VPS IP или домен"
    ask        VPS_PORT_GRPC      "Порт VLESS gRPC (ОСНОВНОЙ канал — весь proxy-трафик)" "$VPS_PORT_GRPC"
    ask        VPS_PORT_VISION    "Порт VLESS Vision (запасной, в роутинге не используется)" "$VPS_PORT_VISION"
    ask_secret VPS_UUID_GRPC      "UUID клиента gRPC (основной)"
    ask_secret VPS_UUID_VISION    "UUID клиента Vision (запасной)"           "$VPS_UUID_GRPC"
    ask_secret VPS_PUBKEY         "Reality Public Key"
    ask_secret VPS_SHORT_ID       "Reality Short ID"
    ask        VPS_SERVER_NAME    "Reality SNI"                              "$VPS_SERVER_NAME"
    ask        VPS_FINGERPRINT    "Reality fingerprint"                      "$VPS_FINGERPRINT"
fi

ask LAN "Локальная подсеть (LAN)" "$LAN"

if [[ -z "$IFACE" ]]; then
    IFACE="$(ip -o -4 route show to default 2>/dev/null | awk '{print $5}' | head -n1 || true)"
fi
ask IFACE "Внешний интерфейс (WAN)" "$IFACE"

echo
say "Итоговая конфигурация:"
cat <<EOF
  VPS_ADDR            = $VPS_ADDR
  VPS_PORT_VISION     = $VPS_PORT_VISION
  VPS_PORT_GRPC       = $VPS_PORT_GRPC
  VPS_SERVER_NAME     = $VPS_SERVER_NAME
  VPS_FINGERPRINT     = $VPS_FINGERPRINT
  LAN                 = $LAN
  IFACE               = $IFACE
  INSTALL_PREFIX      = $INSTALL_PREFIX
  INSTALL_XRAY        = $INSTALL_XRAY
  INSTALL_ZAPRET      = $INSTALL_ZAPRET
  BUILD_ZAPRET        = $BUILD_ZAPRET_FROM_SOURCE
  INSTAGRAM_QUIC_BYPASS = $INSTAGRAM_QUIC_BYPASS
  BLOCK_QUIC          = $BLOCK_QUIC
EOF

if [[ "$NON_INTERACTIVE" != "yes" ]]; then
    read -r -p "${C_YEL}Продолжить установку? [Y/n]${C_OFF} " yn
    [[ "${yn,,}" =~ ^(n|no)$ ]] && die "Aborted by user"
fi

# ---------- APT зависимости ------------------------------------------
say "Installing apt dependencies…"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
APT_PKGS=(
    curl wget ca-certificates gettext-base
    iptables iproute2 iputils-ping dnsutils
    gawk sed grep procps jq ipset libpcap0.8
)
if [[ "$INSTALL_ZAPRET" == "yes" && "$BUILD_ZAPRET_FROM_SOURCE" == "yes" ]]; then
    APT_PKGS+=(build-essential git libnetfilter-queue-dev libnfnetlink-dev libmnl-dev libcap-dev zlib1g-dev pkg-config)
fi
# iptables-persistent без вопросов
echo "iptables-persistent iptables-persistent/autosave_v4 boolean false" | debconf-set-selections
echo "iptables-persistent iptables-persistent/autosave_v6 boolean false" | debconf-set-selections
APT_PKGS+=(iptables-persistent)

apt-get install -y -qq "${APT_PKGS[@]}"
ok "APT packages installed"

# ---------- IP forwarding --------------------------------------------
say "Enabling IPv4 forwarding…"
mkdir -p /etc/sysctl.d
cat > /etc/sysctl.d/99-gateway.conf <<'EOF'
net.ipv4.ip_forward=1
net.ipv4.conf.all.rp_filter=0
net.ipv4.conf.default.rp_filter=0
net.ipv6.conf.all.forwarding=0
EOF
sysctl --system >/dev/null
ok "sysctl applied"

# ==========================================================================
#                            XRAY
# ==========================================================================
if [[ "$INSTALL_XRAY" == "yes" ]]; then
    XRAY_DIR="${INSTALL_PREFIX}/xray"
    say "Installing xray → $XRAY_DIR"
    mkdir -p "$XRAY_DIR"

    if ! command -v "$XRAY_DIR/xray" >/dev/null 2>&1 || ! "$XRAY_DIR/xray" version >/dev/null 2>&1; then
        XRAY_VER="$(curl -fsSL https://api.github.com/repos/XTLS/Xray-core/releases/latest | grep -oE '"tag_name": *"v[^"]+"' | head -1 | grep -oE 'v[0-9.]+')"
        [[ -n "$XRAY_VER" ]] || die "Cannot determine latest Xray version"
        say "Downloading Xray $XRAY_VER ($XRAY_ARCH)…"
        TMP="$(mktemp -d)"
        curl -fsSL "https://github.com/XTLS/Xray-core/releases/download/${XRAY_VER}/Xray-linux-${XRAY_ARCH}.zip" -o "$TMP/xray.zip"
        command -v unzip >/dev/null || apt-get install -y -qq unzip
        unzip -q -o "$TMP/xray.zip" -d "$XRAY_DIR"
        chmod +x "$XRAY_DIR/xray"
        rm -rf "$TMP"
        ok "Xray binary installed"
    else
        ok "Xray already installed: $("$XRAY_DIR/xray" version | head -1)"
    fi

    # Geodata (geosite/geoip)
    for f in geosite.dat geoip.dat; do
        if [[ ! -f "$XRAY_DIR/$f" ]]; then
            say "Downloading $f…"
            curl -fsSL "https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat" -o "$XRAY_DIR/geosite.dat" 2>/dev/null || true
            curl -fsSL "https://github.com/v2fly/geoip/releases/latest/download/geoip.dat"              -o "$XRAY_DIR/geoip.dat"   2>/dev/null || true
            break
        fi
    done
    [[ -s "$XRAY_DIR/geosite.dat" ]] || warn "geosite.dat is missing — geosite rules won't work"
    [[ -s "$XRAY_DIR/geoip.dat"   ]] || warn "geoip.dat is missing"
    ok "Geodata ready"

    # Рендер config.json — общий код для install и веб-UI (xray/render-config.sh):
    # резолв VPS_ADDR→IP, список доменов из xray/domains, envsubst, xray -test.
    say "Rendering xray config…"
    export VPS_ADDR VPS_PORT_VISION VPS_PORT_GRPC VPS_UUID_VISION VPS_UUID_GRPC
    export VPS_PUBKEY VPS_SHORT_ID VPS_SERVER_NAME VPS_FINGERPRINT
    bash "$SCRIPT_DIR/xray/render-config.sh" \
        --template "$SCRIPT_DIR/xray/config.template.json" \
        --out "$XRAY_DIR/config.json" \
        --xray "$XRAY_DIR/xray" \
        || die "render-config failed (проверь VPS-параметры / шаблон)"
    ok "xray config valid"

    # systemd unit
    say "Installing xray.service…"
    cp "$SCRIPT_DIR/systemd/xray.service" /etc/systemd/system/xray.service
    sed -i "s|__XRAY_DIR__|$XRAY_DIR|g" /etc/systemd/system/xray.service
    systemctl daemon-reload
    systemctl enable xray.service >/dev/null
    systemctl restart xray.service
    sleep 1
    if systemctl is-active --quiet xray.service; then
        ok "xray.service running"
    else
        journalctl -u xray.service -n 20 --no-pager || true
        die "xray failed to start"
    fi
fi

# ==========================================================================
#                            ZAPRET
# ==========================================================================
if [[ "$INSTALL_ZAPRET" == "yes" ]]; then
    ZAPRET_DIR="${INSTALL_PREFIX}/zapret"
    ZAPRET_CFG="${INSTALL_PREFIX}/zapret-config"
    say "Installing zapret → $ZAPRET_DIR"

    if [[ ! -x "$ZAPRET_DIR/nfq/nfqws" ]]; then
        if [[ "$BUILD_ZAPRET_FROM_SOURCE" == "yes" ]]; then
            say "Cloning and building bol-van/zapret…"
            rm -rf "$ZAPRET_DIR"
            git clone --depth 1 https://github.com/bol-van/zapret.git "$ZAPRET_DIR"
            ( cd "$ZAPRET_DIR" && make -C nfq -j"$(nproc)" )
            [[ -x "$ZAPRET_DIR/nfq/nfqws" ]] || die "nfqws build failed"
        else
            die "Prebuilt binaries not supported yet — use BUILD_ZAPRET_FROM_SOURCE=yes"
        fi
        ok "nfqws built: $("$ZAPRET_DIR/nfq/nfqws" --version 2>&1 | head -1)"
    else
        ok "nfqws already installed"
    fi

    # Конфиг (домены + скрипт)
    say "Installing zapret config → $ZAPRET_CFG"
    mkdir -p "$ZAPRET_CFG/domains"
    cp "$SCRIPT_DIR/zapret/domains/"*.txt "$ZAPRET_CFG/domains/"

    sed "s|__LAN__|$LAN|g" "$SCRIPT_DIR/zapret/zapret.sh" > "$ZAPRET_CFG/zapret.sh"
    # Также подставим ZAPRET_DIR / CONFIG_DIR на случай нестандартного INSTALL_PREFIX
    sed -i "s|^ZAPRET_DIR=.*|ZAPRET_DIR=$ZAPRET_DIR|" "$ZAPRET_CFG/zapret.sh"
    sed -i "s|^CONFIG_DIR=.*|CONFIG_DIR=$ZAPRET_CFG|" "$ZAPRET_CFG/zapret.sh"
    chmod +x "$ZAPRET_CFG/zapret.sh"
    ok "zapret.sh rendered with LAN=$LAN"

    # Список сервисов: сид рядом с конфигом + пользовательский в /etc/gateway (если ещё нет)
    cp "$SCRIPT_DIR/zapret/services.json" "$ZAPRET_CFG/services.json"
    mkdir -p /etc/gateway
    [[ -f /etc/gateway/zapret-services.json ]] || cp "$SCRIPT_DIR/zapret/services.json" /etc/gateway/zapret-services.json
    ok "zapret services.json установлен"

    # Доп. fake-болванки для готовых стратегий (flowseal): дополняют штатные
    # из апстрима zapret. Нужны пресетам из gateway-ui (--dpi-desync-fake-*).
    if [[ -d "$SCRIPT_DIR/zapret/fake" ]]; then
        mkdir -p "$ZAPRET_DIR/files/fake"
        cp -n "$SCRIPT_DIR/zapret/fake/"*.bin "$ZAPRET_DIR/files/fake/" 2>/dev/null || true
        ok "fake-болванки для пресетов установлены"
    fi

    # systemd unit
    say "Installing zapret.service…"
    cp "$SCRIPT_DIR/systemd/zapret.service" /etc/systemd/system/zapret.service
    sed -i "s|__ZAPRET_CFG__|$ZAPRET_CFG|g" /etc/systemd/system/zapret.service
    systemctl daemon-reload
    systemctl enable zapret.service >/dev/null
fi

# ==========================================================================
#                            IPTABLES BASE
# ==========================================================================
say "Applying base iptables rules (LAN=$LAN, IFACE=$IFACE)…"

# NAT
iptables -t nat -C POSTROUTING -s "$LAN" -o "$IFACE" -j MASQUERADE 2>/dev/null \
    || iptables -t nat -A POSTROUTING -s "$LAN" -o "$IFACE" -j MASQUERADE

# FORWARD
iptables -C FORWARD -s "$LAN" -j ACCEPT 2>/dev/null \
    || iptables -A FORWARD -s "$LAN" -j ACCEPT
iptables -C FORWARD -d "$LAN" -m state --state ESTABLISHED,RELATED -j ACCEPT 2>/dev/null \
    || iptables -A FORWARD -d "$LAN" -m state --state ESTABLISHED,RELATED -j ACCEPT

# Прозрачный прокси: LAN TCP 80/443 → xray dokodemo-door :12345
# (PREROUTING без -i, потому что LAN-трафик может приходить на разные интерфейсы — bridge, wlan и т.п.)
if [[ "$INSTALL_XRAY" == "yes" ]]; then
    iptables -t nat -C PREROUTING -s "$LAN" -p tcp -m multiport --dports 80,443 -j REDIRECT --to-ports 12345 2>/dev/null \
        || iptables -t nat -A PREROUTING -s "$LAN" -p tcp -m multiport --dports 80,443 -j REDIRECT --to-ports 12345
fi

# QUIC: блокируем UDP/443 форвардом, чтобы браузеры падали на TCP (который идёт через xray/zapret)
if [[ "$BLOCK_QUIC" == "yes" ]]; then
    iptables -C FORWARD -p udp --dport 443 -j DROP 2>/dev/null \
        || iptables -A FORWARD -p udp --dport 443 -j DROP
fi

# Персистентность
say "Saving iptables rules (netfilter-persistent)…"
mkdir -p /etc/iptables
iptables-save > /etc/iptables/rules.v4
systemctl enable netfilter-persistent >/dev/null 2>&1 || true
ok "iptables saved to /etc/iptables/rules.v4"

# ---------- Старт zapret (ПОСЛЕ iptables-save чтобы не сохранять NFQUEUE) ---
if [[ "$INSTALL_ZAPRET" == "yes" ]]; then
    say "Starting zapret…"
    systemctl restart zapret.service
    sleep 1
    if systemctl is-active --quiet zapret.service; then
        ok "zapret.service running"
    else
        journalctl -u zapret.service -n 20 --no-pager || true
        warn "zapret failed — check logs"
    fi
fi

# ==========================================================================
#                        FIX-GATEWAY SERVICE
# ==========================================================================
if [[ "$INSTALL_FIX_GATEWAY" == "yes" ]]; then
    say "Installing fix-gateway.service…"

    # Определяем IP роутера если не задан
    if [[ -z "$ROUTER_IP" ]]; then
        ROUTER_IP="$(ip -o -4 route show to default 2>/dev/null | awk '{print $3}' | head -n1 || true)"
    fi
    [[ -n "$ROUTER_IP" ]] || { warn "Cannot detect router IP — skipping fix-gateway"; }

    if [[ -n "$ROUTER_IP" ]]; then
        # общий код с веб-UI — systemd/apply-fix-gateway.sh
        if bash "$SCRIPT_DIR/systemd/apply-fix-gateway.sh" "$ROUTER_IP" >/dev/null; then
            ok "fix-gateway.service enabled (router=$ROUTER_IP)"
        else
            warn "fix-gateway apply failed"
        fi
    fi
fi

# ==========================================================================
#                  DISCORD VOICE UDP TPROXY -> TUNNEL
# ==========================================================================
# РФ DPI глушит UDP голосовых серверов Discord на прямом пути. Заворачиваем
# Discord-голос (UDP 50000-65535) через xray-туннель (gRPC). Обычный TCP-роутинг
# это не покрывает — там REDIRECT только 80/443, UDP идёт мимо xray.
if [[ "${INSTALL_DISCORD_TPROXY:-yes}" == "yes" && -n "$VPS_ADDR" ]]; then
    say "Installing discord-tproxy (UDP voice via tunnel)…"
    mkdir -p /opt/gateway
    sed "s|__VPS_ADDR__|$VPS_ADDR|g" "$SCRIPT_DIR/iptables/discord-tproxy.sh" > /opt/gateway/discord-tproxy.sh
    chmod +x /opt/gateway/discord-tproxy.sh
    cp "$SCRIPT_DIR/systemd/discord-tproxy.service" /etc/systemd/system/discord-tproxy.service
    systemctl daemon-reload
    systemctl enable discord-tproxy.service >/dev/null
    systemctl restart discord-tproxy.service
    ok "discord-tproxy enabled"
fi

# ==========================================================================
#                        REVERSE SSH TUNNEL
# ==========================================================================
if [[ "$INSTALL_REVERSE_TUNNEL" == "yes" ]]; then
    say "Setting up reverse SSH tunnel → VPS:$VPS_TUNNEL_PORT…"
    [[ -n "$VPS_ADDR" ]] || die "VPS_ADDR required for reverse tunnel"

    # Генерируем SSH-ключ если нет
    if [[ ! -f /root/.ssh/id_ed25519 ]]; then
        ssh-keygen -t ed25519 -f /root/.ssh/id_ed25519 -N "" -q
        ok "SSH key generated"
    else
        ok "SSH key already exists"
    fi

    # Добавляем VPS в known_hosts чтобы не спрашивало подтверждение
    ssh-keyscan -H "$VPS_ADDR" >> /root/.ssh/known_hosts 2>/dev/null || true

    # Устанавливаем systemd unit
    sed -e "s|__VPS_ADDR__|$VPS_ADDR|g" \
        -e "s|__TUNNEL_PORT__|$VPS_TUNNEL_PORT|g" \
        "$SCRIPT_DIR/systemd/ssh-tunnel.service" \
        > /etc/systemd/system/ssh-tunnel.service
    systemctl daemon-reload
    systemctl enable ssh-tunnel.service >/dev/null
    systemctl restart ssh-tunnel.service
    sleep 2
    if systemctl is-active --quiet ssh-tunnel.service; then
        ok "ssh-tunnel.service running"
    else
        warn "ssh-tunnel failed to start — возможно ключ ещё не добавлен на VPS"
    fi

    PUBKEY="$(cat /root/.ssh/id_ed25519.pub)"
    echo
    warn "ACTION REQUIRED: добавь публичный ключ этой машины на VPS:"
    echo
    echo "  ${C_BLD}ssh root@${VPS_ADDR} \"echo '${PUBKEY}' >> ~/.ssh/authorized_keys\"${C_OFF}"
    echo
    say "После добавления ключа туннель поднимется автоматически (RestartSec=15)"
    say "Доступ через VPS: ssh -p ${VPS_TUNNEL_PORT} root@localhost"
fi

# ==========================================================================
#                            WEB UI (gateway-ui)
# ==========================================================================
if [[ "$INSTALL_WEB_UI" == "yes" ]]; then
    say "Installing gateway-ui (веб-интерфейс)…"
    UI_DIR="${INSTALL_PREFIX}/gateway-ui"
    mkdir -p "$UI_DIR" /etc/gateway

    # 1) Бинарник: скачать готовый из GitHub release под арх; если не вышло —
    #    собрать из исходников (только если на машине есть go).
    UI_OK=yes
    UI_URL="https://github.com/AndreyShatl/gateway-universal/releases/latest/download/gateway-ui-${ARCH}"
    # качаем во временный файл и подменяем через mv — иначе curl -o поверх
    # запущенного бинарника падает с "Text file busy" при переустановке.
    if curl -fsSL "$UI_URL" -o "$UI_DIR/gateway-ui.new" 2>/dev/null && [[ -s "$UI_DIR/gateway-ui.new" ]]; then
        chmod +x "$UI_DIR/gateway-ui.new"
        mv "$UI_DIR/gateway-ui.new" "$UI_DIR/gateway-ui"
        ok "gateway-ui: скачан из release ($ARCH)"
    elif command -v go >/dev/null 2>&1; then
        say "release недоступен — собираю из исходников…"
        ( cd "$SCRIPT_DIR/gateway-ui" && go build -o "$UI_DIR/gateway-ui" . ) \
            && ok "gateway-ui собран" || { warn "сборка gateway-ui не удалась"; UI_OK=no; }
    else
        warn "не удалось скачать gateway-ui ($ARCH) и нет Go для сборки — пропускаю"
        UI_OK=no
    fi

    if [[ "$UI_OK" == "yes" ]]; then
        # 2) Пароль (если ui.conf ещё нет): из WEB_UI_PASSWORD или случайный
        UI_GEN_PW=""
        if [[ ! -f /etc/gateway/ui.conf ]]; then
            if [[ -z "$WEB_UI_PASSWORD" ]]; then
                WEB_UI_PASSWORD="$(head -c 9 /dev/urandom | base64 | tr -d '/+=' | cut -c1-12)"
                UI_GEN_PW="$WEB_UI_PASSWORD"
            fi
            GATEWAY_UI_PASSWORD="$WEB_UI_PASSWORD" "$UI_DIR/gateway-ui" \
                --conf /etc/gateway/ui.conf --init-password >/dev/null \
                && ok "пароль UI сохранён в /etc/gateway/ui.conf"
        fi

        # 3) systemd unit (LAN-only через ExecStartPre iptables)
        sed -e "s|__PORT__|$WEB_UI_PORT|g" \
            -e "s|__LAN__|$LAN|g" \
            -e "s|__UI_BIN__|$UI_DIR/gateway-ui|g" \
            -e "s|__REPO__|$SCRIPT_DIR|g" \
            -e "s|__CONFIG_ENV__|$CONFIG_ENV|g" \
            "$SCRIPT_DIR/systemd/gateway-ui.service" > /etc/systemd/system/gateway-ui.service
        systemctl daemon-reload
        systemctl enable gateway-ui.service >/dev/null
        systemctl restart gateway-ui.service
        sleep 1
        if systemctl is-active --quiet gateway-ui.service; then
            ok "gateway-ui.service running (LAN:$WEB_UI_PORT)"
        else
            journalctl -u gateway-ui.service -n 15 --no-pager || true
            warn "gateway-ui не стартовал — см. логи"
        fi
        [[ -n "$UI_GEN_PW" ]] && warn "Пароль веб-интерфейса (сохрани!): ${C_BLD}${UI_GEN_PW}${C_OFF}"
    fi
fi

# ==========================================================================
#                    AUTO-ROUTE DETECTOR (gateway-detector)
# ==========================================================================
# Пассивный детектор блокировок (pcap → prober → applier). Запускается/останав-
# ливается gateway-ui по тумблеру «Авто-обход» (здесь только ставим бинарь+юнит).
if [[ -d "$SCRIPT_DIR/detector" ]] && command -v go >/dev/null 2>&1; then
    say "Building gateway-detector…"
    apt-get install -y -qq libpcap-dev >/dev/null 2>&1 || true
    if ( cd "$SCRIPT_DIR/detector" && CGO_ENABLED=1 go build -o /opt/gateway-detector . ) 2>/dev/null; then
        cp "$SCRIPT_DIR/systemd/gateway-detector.service" /etc/systemd/system/gateway-detector.service
        # ночная перепроверка списка авто-обхода (снятие разблокированных)
        cp "$SCRIPT_DIR/systemd/gateway-recheck.service" /etc/systemd/system/gateway-recheck.service
        cp "$SCRIPT_DIR/systemd/gateway-recheck.timer" /etc/systemd/system/gateway-recheck.timer
        systemctl daemon-reload
        systemctl enable --now gateway-recheck.timer >/dev/null 2>&1 || true
        ok "gateway-detector установлен (управляется тумблером в UI); ночная перепроверка в 04:00"
    else
        warn "сборка gateway-detector не удалась (нужен gcc + libpcap-dev) — авто-детект недоступен"
    fi
fi

# ==========================================================================
#                            HEALTHCHECK
# ==========================================================================
if [[ "$SKIP_HEALTHCHECK" != "yes" ]]; then
    echo
    say "Healthcheck…"

    if [[ -n "$VPS_ADDR" ]]; then
        if ping -c 2 -W 2 "$VPS_ADDR" >/dev/null 2>&1; then
            ok "VPS $VPS_ADDR reachable"
        else
            warn "VPS $VPS_ADDR not pingable (может быть нормально если ICMP закрыт)"
        fi
    fi

    if [[ "$INSTALL_XRAY" == "yes" ]]; then
        if ss -tlnp 2>/dev/null | grep -q ':12345 '; then
            ok "xray listening on :12345 (tproxy)"
        else
            warn "xray NOT listening on :12345"
        fi
        if ss -tlnp 2>/dev/null | grep -q ':1080 '; then
            ok "xray SOCKS5 on :1080"
        fi

        # Быстрый тест SOCKS через VLESS
        if command -v curl >/dev/null 2>&1; then
            IP_VIA_PROXY="$(curl -sS --max-time 8 --socks5-hostname 127.0.0.1:1080 https://api.ipify.org 2>/dev/null || true)"
            if [[ -n "$IP_VIA_PROXY" ]]; then
                ok "VLESS proxy works — exit IP: $IP_VIA_PROXY"
            else
                warn "VLESS SOCKS5 test failed (проверь UUID/Reality ключи)"
            fi
        fi
    fi

    if [[ "$INSTALL_ZAPRET" == "yes" ]]; then
        if pgrep -x nfqws >/dev/null; then
            ok "nfqws running ($(pgrep -x nfqws | wc -l) processes)"
        else
            warn "nfqws is NOT running"
        fi
        if iptables -t mangle -L POSTROUTING -n 2>/dev/null | grep -q NFQUEUE; then
            ok "NFQUEUE rules present"
        else
            warn "NFQUEUE rules missing"
        fi
    fi
fi

echo
ok "Installation complete."
cat <<EOF

${C_BLD}Next steps:${C_OFF}
  1. На роутере: DHCP → шлюз = IP этой машины, DNS = 8.8.8.8
  2. Проверить с клиента: curl https://api.ipify.org (должен вернуться IP VPS для заблокированных)
  3. Логи:
       journalctl -u xray.service -f
       journalctl -u zapret.service -f
  4. Управление zapret:
       ${INSTALL_PREFIX}/zapret-config/zapret.sh {start|stop|restart|status}

${C_BLD}Repo:${C_OFF} https://github.com/andrey-shat/gateway-universal
EOF
