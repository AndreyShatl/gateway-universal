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
    ask        VPS_PORT_VISION    "Порт VLESS Vision (основной)"             "$VPS_PORT_VISION"
    ask        VPS_PORT_GRPC      "Порт VLESS gRPC (для Meta/Instagram)"     "$VPS_PORT_GRPC"
    ask_secret VPS_UUID_VISION    "UUID клиента Vision"
    ask_secret VPS_UUID_GRPC      "UUID клиента gRPC"                        "$VPS_UUID_VISION"
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
    gawk sed grep
)
if [[ "$INSTALL_ZAPRET" == "yes" && "$BUILD_ZAPRET_FROM_SOURCE" == "yes" ]]; then
    APT_PKGS+=(build-essential git libnetfilter-queue-dev libnfnetlink-dev libcap-dev zlib1g-dev pkg-config)
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

    # Резолвим VPS_ADDR → IP для правила "ip" (xray ожидает IP/CIDR, не домен).
    # Для "address" в outbound'е оставляем как есть (там и домен подходит).
    if [[ "$VPS_ADDR" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        VPS_ADDR_IP="$VPS_ADDR"
    else
        VPS_ADDR_IP="$(getent ahostsv4 "$VPS_ADDR" 2>/dev/null | awk 'NR==1{print $1}')"
        [[ -n "$VPS_ADDR_IP" ]] || die "Cannot resolve VPS_ADDR=$VPS_ADDR to IP"
        say "Resolved $VPS_ADDR → $VPS_ADDR_IP"
    fi

    # Конфиг из шаблона
    say "Rendering xray config…"
    export VPS_ADDR VPS_ADDR_IP VPS_PORT_VISION VPS_PORT_GRPC VPS_UUID_VISION VPS_UUID_GRPC
    export VPS_PUBKEY VPS_SHORT_ID VPS_SERVER_NAME VPS_FINGERPRINT
    envsubst < "$SCRIPT_DIR/xray/config.template.json" > "$XRAY_DIR/config.json"

    # Проверка конфига
    if "$XRAY_DIR/xray" -test -config "$XRAY_DIR/config.json" >/dev/null 2>&1; then
        ok "xray config valid"
    else
        "$XRAY_DIR/xray" -test -config "$XRAY_DIR/config.json" || true
        die "xray config invalid — check placeholders"
    fi

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
            ( cd "$ZAPRET_DIR" && make -s -j"$(nproc)" nfqws )
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
