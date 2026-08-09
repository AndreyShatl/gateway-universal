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
: "${INSTALL_DNSCRYPT:=yes}"
: "${INSTALL_BRAIN:=yes}"
: "${INSTALL_ADGUARD:=yes}"
: "${ADGUARD_PASSWORD:=}"
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
        --no-dnscrypt)         INSTALL_DNSCRYPT=no; shift;;
        --no-brain)            INSTALL_BRAIN=no; shift;;
        --no-adguard)          INSTALL_ADGUARD=no; shift;;
        --adguard-password)    ADGUARD_PASSWORD="$2"; shift 2;;
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

if [[ "$NON_INTERACTIVE" != "yes" && "$INSTALL_XRAY" == "yes" && -z "$VPS_ADDR" ]]; then
    read -r -p "${C_BLU}?${C_OFF} Есть ли у вас VPS для туннеля (VLESS+Reality)? Без него шлюз будет работать
  только через zapret (DPI-обход напрямую, без запасного VPS-канала). [Y/n]: " ans
    [[ "${ans,,}" =~ ^n ]] && INSTALL_XRAY=no
fi

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

if [[ "$NON_INTERACTIVE" != "yes" && "$INSTALL_ADGUARD" == "yes" ]]; then
    read -r -p "${C_BLU}?${C_OFF} Блокировать рекламу на всех устройствах дома (AdGuard Home)? [Y/n]: " ans
    [[ "${ans,,}" =~ ^n ]] && INSTALL_ADGUARD=no
fi

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
  INSTALL_DNSCRYPT    = $INSTALL_DNSCRYPT
  INSTALL_BRAIN       = $INSTALL_BRAIN
  INSTALL_ADGUARD     = $INSTALL_ADGUARD
EOF

if [[ "$NON_INTERACTIVE" != "yes" ]]; then
    read -r -p "${C_YEL}Продолжить установку? [Y/n]${C_OFF} " yn
    [[ "${yn,,}" =~ ^(n|no)$ ]] && die "Aborted by user"
fi

# ---------- APT зависимости ------------------------------------------
say "Updating system packages…"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
# Обновляем ВСЮ систему (не только пакеты шлюза) — особенно важно на свежей
# виртуалке/образе, где базовый набор пакетов может быть старым (безопасность,
# совместимость с systemd/iptables-nft и т.д.). --with-new-pkgs подтягивает
# новые зависимости, если версия пакета того требует.
apt-get upgrade -y -qq --with-new-pkgs || warn "apt-get upgrade завершился с ошибками — продолжаю установку"

say "Installing apt dependencies…"
APT_PKGS=(
    curl wget ca-certificates gettext-base
    iptables iproute2 iputils-ping dnsutils
    gawk sed grep procps jq ipset libpcap0.8
    python3 unzip conntrack
)
if [[ "$INSTALL_ZAPRET" == "yes" && "$BUILD_ZAPRET_FROM_SOURCE" == "yes" ]]; then
    APT_PKGS+=(build-essential git libnetfilter-queue-dev libnfnetlink-dev libmnl-dev libcap-dev zlib1g-dev pkg-config)
fi
if [[ "$INSTALL_WEB_UI" == "yes" ]]; then
    # ставим Go всегда: готового релиза gateway-ui может не быть под архитектуру/
    # версию — надёжнее собрать из исходников этого же клона, чем зависеть от сети
    APT_PKGS+=(golang-go)
fi
if [[ "$INSTALL_DNSCRYPT" == "yes" ]]; then
    APT_PKGS+=(dnscrypt-proxy)
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
# Бинарник/geodata/systemd-юнит ставим ВСЕГДА, независимо от INSTALL_XRAY —
# без VPS-креды на момент установки это просто неактивный, ничего не стоящий
# инструмент про запас (~40МБ). Так человек без VPS сейчас, но с VPS позже,
# может просто вставить vless://-ссылку в веб-панели (см. gateway-ui/
# connection.go, handleConnection) — без переустановки шлюза. Настройка (render-
# config) + запуск + iptables-редирект остаются строго за INSTALL_XRAY/наличием
# реальных VPS_* — нельзя рендерить конфиг без креды и нельзя заворачивать
# трафик в незапущенный xray (см. vps-mode.sh — тот же редирект, что ставится
# ниже, специально не трогаем без работающего xray).
XRAY_DIR="${INSTALL_PREFIX}/xray"
say "Installing xray binary → $XRAY_DIR"
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

# systemd unit — тоже всегда, даже без VPS (просто не enable/start ниже).
say "Installing xray.service…"
cp "$SCRIPT_DIR/systemd/xray.service" /etc/systemd/system/xray.service
sed -i "s|__XRAY_DIR__|$XRAY_DIR|g" /etc/systemd/system/xray.service
systemctl daemon-reload

if [[ "$INSTALL_XRAY" == "yes" ]]; then
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

    systemctl enable xray.service >/dev/null
    systemctl restart xray.service
    sleep 1
    if systemctl is-active --quiet xray.service; then
        ok "xray.service running"
    else
        journalctl -u xray.service -n 20 --no-pager || true
        die "xray failed to start"
    fi
else
    ok "xray готов, но не настроен (нет VPS-креды) — добавить можно позже через веб-панель (Подключение → вставить vless://-ссылку)"
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
    if [[ ! -f /etc/gateway/zapret-services.json ]]; then
        if [[ "$INSTALL_XRAY" == "yes" ]]; then
            cp "$SCRIPT_DIR/zapret/services.json" /etc/gateway/zapret-services.json
        else
            # без VPS фолбэка нет — сервисы с mode=vps (напр. Discord) без него
            # молча не десинхронизировались бы (не в zapret-хостлисте, не в
            # проксировании — xray не установлен). Переводим все на zapret —
            # хуже, чем VPS, не будет, лучше попробовать, чем оставить как есть.
            jq 'map(.mode = "zapret")' "$SCRIPT_DIR/zapret/services.json" > /etc/gateway/zapret-services.json
        fi
    fi
    ok "zapret services.json установлен$( [[ "$INSTALL_XRAY" != "yes" ]] && echo " (все сервисы на zapret — VPS не настроен)" )"

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
#                        ШИФРОВАННЫЙ DNS (dnscrypt-proxy)
# ==========================================================================
# Провайдер/ТСПУ подделывает открытый DNS :53 — блокируемые домены резолвятся в
# фейковые IP (T43). dns/setup-dnscrypt.sh: DoH-апстрим + редирект всего :53 в
# него + resolv.conf -> 127.0.0.1 (immutable). Если следом ставим AdGuard Home —
# он переставит dnscrypt на :5353 сам (см. ниже), тут этого не трогаем.
if [[ "$INSTALL_DNSCRYPT" == "yes" ]]; then
    say "Installing dnscrypt-proxy (encrypted DNS)…"
    if LAN="$LAN" bash "$SCRIPT_DIR/dns/setup-dnscrypt.sh"; then
        ok "dnscrypt-proxy настроен (шифрованный DNS на :53)"
    else
        warn "dnscrypt-proxy setup failed — DNS останется как есть"
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

say "Installing game-mode (blanket-ACCEPT for ephemeral game ports, off by default)…"
mkdir -p /opt/gateway /etc/gateway
cp "$SCRIPT_DIR/iptables/game-mode.sh" /opt/gateway/game-mode.sh
chmod +x /opt/gateway/game-mode.sh
[ -f /etc/gateway/game-mode.conf ] || echo off > /etc/gateway/game-mode.conf
cp "$SCRIPT_DIR/systemd/game-mode.service" /etc/systemd/system/game-mode.service
systemctl daemon-reload
systemctl enable game-mode.service >/dev/null
systemctl restart game-mode.service
ok "game-mode enabled (mode=$(cat /etc/gateway/game-mode.conf))"

# Переключатель "VPS+zapret" / "только zapret" (в UI — «Режим работы»). Дефолт
# зависит от того, настроен ли VPS при установке — если да, начинаем с "on"
# (как раньше, ничего не меняется), если нет — "off" сразу (нечего включать).
say "Installing vps-mode toggle…"
cp "$SCRIPT_DIR/iptables/vps-mode.sh" /opt/gateway/vps-mode.sh
chmod +x /opt/gateway/vps-mode.sh
if [[ ! -f /etc/gateway/vps-mode.conf ]]; then
    if [[ "$INSTALL_XRAY" == "yes" ]]; then echo on > /etc/gateway/vps-mode.conf; else echo off > /etc/gateway/vps-mode.conf; fi
fi
cp "$SCRIPT_DIR/systemd/vps-mode.service" /etc/systemd/system/vps-mode.service
systemctl daemon-reload
systemctl enable vps-mode.service >/dev/null
systemctl restart vps-mode.service
ok "vps-mode enabled (режим=$(cat /etc/gateway/vps-mode.conf))"

# direct-fastpath: снижает нагрузку xray на тяжёлый direct-трафик (Steam/
# торренты/видео) — весь LAN 80/443 редиректится на xray ради решения
# direct/proxy на КАЖДОЕ соединение, даже когда решение заведомо direct.
# Смысла нет без xray (нечего разгружать), ставим только вместе с ним.
if [[ "$INSTALL_XRAY" == "yes" ]]; then
    say "Installing direct-fastpath (снижение CPU на тяжёлом direct-трафике)…"
    cp "$SCRIPT_DIR/iptables/direct-fastpath.sh" /opt/gateway/direct-fastpath.sh
    chmod +x /opt/gateway/direct-fastpath.sh
    cp "$SCRIPT_DIR/systemd/gateway-direct-fastpath.service" /etc/systemd/system/gateway-direct-fastpath.service
    systemctl daemon-reload
    systemctl enable gateway-direct-fastpath.service >/dev/null
    systemctl restart gateway-direct-fastpath.service
    ok "direct-fastpath enabled"
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

    # 1) Собрать из исходников ЭТОГО клона (гарантированно актуальный код — готовый
    #    release может отставать/не существовать под архитектуру); если Go почему-то
    #    недоступен — запасной путь: скачать готовый релиз.
    UI_OK=yes
    if command -v go >/dev/null 2>&1; then
        say "Собираю gateway-ui из исходников…"
        ( cd "$SCRIPT_DIR/gateway-ui" && GOTOOLCHAIN=local TMPDIR=/var/tmp go build -o "$UI_DIR/gateway-ui.new" . ) \
            && mv "$UI_DIR/gateway-ui.new" "$UI_DIR/gateway-ui" && ok "gateway-ui собран" \
            || { warn "сборка gateway-ui не удалась — пробую готовый release"; UI_OK=no; }
    else
        UI_OK=no
    fi
    if [[ "$UI_OK" != "yes" ]]; then
        UI_URL="https://github.com/AndreyShatl/gateway-universal/releases/latest/download/gateway-ui-${ARCH}"
        # качаем во временный файл и подменяем через mv — иначе curl -o поверх
        # запущенного бинарника падает с "Text file busy" при переустановке.
        if curl -fsSL "$UI_URL" -o "$UI_DIR/gateway-ui.new" 2>/dev/null && [[ -s "$UI_DIR/gateway-ui.new" ]]; then
            chmod +x "$UI_DIR/gateway-ui.new"
            mv "$UI_DIR/gateway-ui.new" "$UI_DIR/gateway-ui"
            ok "gateway-ui: скачан из release ($ARCH)"
            UI_OK=yes
        else
            warn "ни сборка, ни скачивание gateway-ui не удались — пропускаю веб-интерфейс"
        fi
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
    say "Building gateway-detector (pcap, безопасный дефолт на всех архитектурах)…"
    apt-get install -y -qq libpcap-dev >/dev/null 2>&1 || true
    # TMPDIR=/var/tmp — на слабом железе /tmp бывает отдельным маленьким разделом,
    # сборка Go может упасть "no space left on device" даже при свободном / (было).
    if ( cd "$SCRIPT_DIR/detector" && CGO_ENABLED=1 TMPDIR=/var/tmp GOTOOLCHAIN=local go build -o /opt/gateway-detector . ) 2>/dev/null; then
        cp "$SCRIPT_DIR/systemd/gateway-detector.service" /etc/systemd/system/gateway-detector.service
        # ночная перепроверка списка авто-обхода (снятие разблокированных)
        cp "$SCRIPT_DIR/systemd/gateway-recheck.service" /etc/systemd/system/gateway-recheck.service
        cp "$SCRIPT_DIR/systemd/gateway-recheck.timer" /etc/systemd/system/gateway-recheck.timer
        systemctl daemon-reload
        systemctl enable --now gateway-recheck.timer >/dev/null 2>&1 || true
        ok "gateway-detector установлен (управляется тумблером в UI); ночная перепроверка в 04:00"
        ok "(опционально, вручную) eBPF-детектор (T59, экспериментальный, x86_64): clang libbpf-dev linux-headers-\$(uname -r) bpftool, затем go build -tags ebpf, systemd/gateway-detector-ebpf.service"
    else
        warn "сборка gateway-detector не удалась (нужен gcc + libpcap-dev) — авто-детект недоступен"
    fi
fi

# ==========================================================================
#                    "МОЗГ" (brain): авто-обход per-domain через zapret
# ==========================================================================
# solve.sh (перебор пресетов) -> brain-apply.sh (группа=стратегия, T-consolidate)
# -> brain-worker.sh (очередь) -> brain-nightly.sh (переоценка). Детектор выше
# кладёт заблокированные домены в очередь автоматически.
if [[ "$INSTALL_BRAIN" == "yes" ]]; then
    say "Installing brain (авто-подбор zapret-стратегий по доменам)…"
    mkdir -p /opt/gateway-brain /etc/gateway
    for f in gwdb.py solve.sh brain-apply.sh brain-worker.sh brain-nightly.sh \
             brain-activity.sh brain-idle-stop.sh brain-static-reeval.sh zapret-auto-update.sh; do
        cp "$SCRIPT_DIR/scripts/$f" /opt/gateway-brain/"$f"
    done
    chmod +x /opt/gateway-brain/*.sh

    # состояние с нуля (свежая установка) — не трогаем, если уже есть (переустановка)
    [[ -f /etc/gateway/brain-services.json ]] || echo '[]' > /etc/gateway/brain-services.json
    [[ -f /etc/gateway/brain-queue ]] || : > /etc/gateway/brain-queue
    if [[ ! -f /etc/gateway/zapret-services.json ]]; then
        if [[ "$INSTALL_XRAY" == "yes" ]]; then
            cp "$SCRIPT_DIR/zapret/services.json" /etc/gateway/zapret-services.json
        else
            jq 'map(.mode = "zapret")' "$SCRIPT_DIR/zapret/services.json" > /etc/gateway/zapret-services.json
        fi
    fi

    # whitelist (.ru/.su/.рф не анализируются) + пресеты (flowseal, из strategies.json)
    python3 /opt/gateway-brain/gwdb.py init --strategies-file "$SCRIPT_DIR/gateway-ui/strategies.json" >/dev/null 2>&1 || true

    for u in gateway-brain-worker.service gateway-brain-restore.service \
             gateway-brain-nightly.service gateway-brain-nightly.timer \
             gateway-brain-activity.service gateway-brain-activity.timer \
             gateway-brain-idle-stop.service gateway-brain-idle-stop.timer \
             gateway-brain-static-reeval.service gateway-brain-static-reeval.timer \
             gateway-zapret-autoupdate.service gateway-zapret-autoupdate.timer; do
        cp "$SCRIPT_DIR/systemd/$u" /etc/systemd/system/"$u"
    done
    systemctl daemon-reload
    systemctl enable --now gateway-brain-restore.service gateway-brain-worker.service >/dev/null 2>&1 || true
    systemctl enable --now gateway-brain-nightly.timer gateway-brain-activity.timer \
        gateway-brain-idle-stop.timer gateway-brain-static-reeval.timer \
        gateway-zapret-autoupdate.timer >/dev/null 2>&1 || true
    ok "brain установлен (voркер + ночная переоценка 04:00 + автообновление zapret по воскресеньям 02:00)"
fi

# ==========================================================================
#                 БЛОКИРОВКА РЕКЛАМЫ (AdGuard Home, DNS-уровень)
# ==========================================================================
# Встраивается МЕЖДУ клиентами и dnscrypt-proxy: клиент -> AdGuard Home (:53,
# фильтрует рекламу/трекеры) -> dnscrypt-proxy (127.0.0.1:5353, шифрованный
# upstream) -> интернет. Существующий редирект всего :53 (dns/gateway-dns-
# redirect.sh) не трогаем — AdGuard Home просто занимает порт вместо dnscrypt.
if [[ "$INSTALL_ADGUARD" == "yes" ]]; then
    say "Installing AdGuard Home (блокировка рекламы на DNS-уровне)…"
    AGH_DIR="${INSTALL_PREFIX}/AdGuardHome"
    if [[ -z "$ADGUARD_PASSWORD" ]]; then
        ADGUARD_PASSWORD="$(head -c 9 /dev/urandom | base64 | tr -d '/+=' | cut -c1-12)"
    fi

    AGH_OK=yes
    if [[ ! -x "$AGH_DIR/AdGuardHome" ]]; then
        TMP="$(mktemp -d)"
        AGH_ASSET="AdGuardHome_linux_amd64.tar.gz"
        case "$ARCH" in
            aarch64) AGH_ASSET="AdGuardHome_linux_arm64.tar.gz";;
            armv7l)  AGH_ASSET="AdGuardHome_linux_armv7.tar.gz";;
        esac
        AGH_URL="https://static.adguard.com/adguardhome/release/$AGH_ASSET"
        # AdGuardHome — open-source, тот же .tar.gz лежит и в GitHub Releases
        # (без привязки к версии: releases/latest/download/... сам редиректит
        # на актуальный релиз). Резервный источник — не для скорости, а на
        # случай, когда static.adguard.com блокируется/душится (см. ниже),
        # а GitHub в это же время доступен (живой инцидент 2026-08-01: TLS-
        # handshake к static.adguard.com завис намертво, тот же файл с GitHub
        # долетел за 2с).
        AGH_URL_FALLBACK="https://github.com/AdguardTeam/AdGuardHome/releases/latest/download/$AGH_ASSET"
        # static.adguard.com у некоторых провайдеров/ТСПУ душит именно НАЧАЛО
        # больших TLS-закачек (первые секунды на десятки КБ/с, потом разгоняется
        # до нормальной скорости) — короткого --max-time не хватает не потому что
        # соединение мертво, а потому что троттлинг съедает бюджет ДО разгона.
        # 45с + пара повторов почти всегда переживают этот эффект (см. живой
        # инцидент 2026-08-01: тот же URL с 15с падал, с руки — проходил за 2-3с
        # после старта). Но иногда это не троттлинг, а полная блокировка TLS по
        # SNI (тот же день, тот же домен — TLS handshake завис насмерть без
        # единого байта ответа) — тогда таймаут/ретраи не помогут в принципе,
        # нужен другой источник (GitHub) или VPS-туннель.
        if ! curl -fsSL --max-time 45 --retry 2 --retry-delay 3 -o "$TMP/agh.tar.gz" "$AGH_URL" 2>/dev/null || [[ ! -s "$TMP/agh.tar.gz" ]]; then
            if [[ "$INSTALL_XRAY" == "yes" ]] && ss -tlnp 2>/dev/null | grep -q ':1081 '; then
                warn "прямое скачивание AdGuard Home зависло — пробую через VPS-туннель…"
                curl -fsSL --max-time 60 --socks5-hostname 127.0.0.1:1081 -o "$TMP/agh.tar.gz" "$AGH_URL" 2>/dev/null || true
            fi
            if [[ ! -s "$TMP/agh.tar.gz" ]]; then
                warn "static.adguard.com недоступен — пробую GitHub Releases…"
                curl -fsSL --max-time 30 -o "$TMP/agh.tar.gz" "$AGH_URL_FALLBACK" 2>/dev/null || true
            fi
        fi
        if [[ -s "$TMP/agh.tar.gz" ]] && tar -xzf "$TMP/agh.tar.gz" -C "$INSTALL_PREFIX" 2>/dev/null; then
            ok "AdGuard Home скачан"
        else
            warn "не удалось скачать/распаковать AdGuard Home — пропускаю блокировку рекламы"
            AGH_OK=no
        fi
        rm -rf "$TMP"
    fi

    if [[ "$AGH_OK" == "yes" ]]; then
        # dnscrypt-proxy (если стоит) — сдвигаем на 127.0.0.1:5353, освобождая :53
        if systemctl list-unit-files dnscrypt-proxy.socket >/dev/null 2>&1; then
            mkdir -p /etc/systemd/system/dnscrypt-proxy.socket.d
            cat > /etc/systemd/system/dnscrypt-proxy.socket.d/override.conf <<'OVR'
[Socket]
FreeBind=true
ListenStream=
ListenDatagram=
ListenStream=127.0.0.1:5353
ListenDatagram=127.0.0.1:5353
OVR
            systemctl daemon-reload
            systemctl restart dnscrypt-proxy.socket dnscrypt-proxy.service 2>/dev/null || true
        fi

        if [[ ! -f /etc/systemd/system/AdGuardHome.service ]]; then
            ( cd "$AGH_DIR" && ./AdGuardHome -s install ) >/dev/null 2>&1 || true
            sleep 2
        fi
        # веб-панель :3000 — только LAN (тот же паттерн, что у gateway-ui:8088)
        mkdir -p /etc/systemd/system/AdGuardHome.service.d
        cat > /etc/systemd/system/AdGuardHome.service.d/firewall.conf <<EOF
[Service]
ExecStartPre=/bin/bash -c "iptables -C INPUT -p tcp --dport 3000 -s $LAN -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport 3000 -s $LAN -j ACCEPT"
ExecStartPre=/bin/bash -c "iptables -C INPUT -p tcp --dport 3000 -j DROP 2>/dev/null || iptables -A INPUT -p tcp --dport 3000 -j DROP"
EOF
        # loopback ACCEPT (иначе сам шлюз не достучится до :3000/:8088 — правило
        # ACCEPT-LAN-DROP-остальное не пропускает 127.0.0.1, он не в LAN-подсети)
        iptables -C INPUT -i lo -j ACCEPT 2>/dev/null || iptables -I INPUT -i lo -j ACCEPT
        systemctl daemon-reload
        systemctl enable --now AdGuardHome.service >/dev/null 2>&1 || true
        sleep 2

        # первичная настройка через API (без интерактивного мастера установки)
        curl -s --max-time 10 -X POST http://127.0.0.1:3000/control/install/configure \
            -H "Content-Type: application/json" \
            -d "{\"web\":{\"ip\":\"0.0.0.0\",\"port\":3000},\"dns\":{\"ip\":\"0.0.0.0\",\"port\":53},\"username\":\"admin\",\"password\":\"$ADGUARD_PASSWORD\"}" \
            >/dev/null 2>&1 || true
        sleep 1
        AGH_COOKIE_JAR="$(mktemp)"
        curl -s --max-time 30 -c "$AGH_COOKIE_JAR" -X POST http://127.0.0.1:3000/control/login \
            -H "Content-Type: application/json" -d "{\"name\":\"admin\",\"password\":\"$ADGUARD_PASSWORD\"}" >/dev/null 2>&1 || true
        # upstream — наш локальный шифрованный резолвер, не внешний DoH напрямую
        curl -s --max-time 10 -b "$AGH_COOKIE_JAR" -X POST http://127.0.0.1:3000/control/dns_config \
            -H "Content-Type: application/json" \
            -d '{"upstream_dns":["127.0.0.1:5353"],"bootstrap_dns":["127.0.0.1:5353"]}' >/dev/null 2>&1 || true
        curl -s --max-time 30 -b "$AGH_COOKIE_JAR" -X POST http://127.0.0.1:3000/control/filtering/refresh \
            -H "Content-Type: application/json" -d '{}' >/dev/null 2>&1 || true
        rm -f "$AGH_COOKIE_JAR"

        # для gateway-ui (сводка статистики на дашборде)
        if ! grep -q "^ADGUARD_PASSWORD=" "$CONFIG_ENV" 2>/dev/null; then
            echo "ADGUARD_PASSWORD=$ADGUARD_PASSWORD" >> "$CONFIG_ENV"
        fi

        if systemctl is-active --quiet AdGuardHome.service; then
            ok "AdGuard Home установлен и активен (панель :3000, LAN-only)"
        else
            warn "AdGuardHome.service не запустился — проверь логи"
        fi
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

GW_LAN_IP="$(ip -o -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')"
echo
ok "Installation complete."
cat <<EOF

${C_BLD}Next steps:${C_OFF}
  1. На роутере: DHCP → шлюз = IP этой машины (${GW_LAN_IP:-?}), DNS = ${GW_LAN_IP:-IP_шлюза}
     (сам шлюз резолвит через шифрованный DNS — не указывайте 8.8.8.8/другой публичный,
     иначе провайдер снова увидит и подменит DNS-запросы всех устройств дома)
  2. Проверить с клиента: curl https://api.ipify.org (должен вернуться IP VPS для заблокированных)
  3. Веб-интерфейс шлюза: http://${GW_LAN_IP:-IP_шлюза}:${WEB_UI_PORT}
EOF
[[ -n "${UI_GEN_PW:-}" ]] && echo "     Пароль: ${C_BLD}${UI_GEN_PW}${C_OFF} (сохрани — больше нигде не показывается)"
if [[ "$INSTALL_ADGUARD" == "yes" && "${AGH_OK:-no}" == "yes" ]]; then
cat <<EOF
  4. Блокировка рекламы (AdGuard Home): http://${GW_LAN_IP:-IP_шлюза}:3000
     Логин: admin  Пароль: ${C_BLD}${ADGUARD_PASSWORD}${C_OFF} (сохрани — тоже нигде больше не показывается)
     Сводка статистики уже видна прямо в веб-интерфейсе шлюза, раздел «Реклама (DNS)»
EOF
fi
cat <<EOF
  5. Логи:
       journalctl -u xray.service -f
       journalctl -u zapret.service -f
       journalctl -u gateway-brain-worker.service -f   # (или в веб-интерфейсе — Логи)
  6. Управление zapret:
       ${INSTALL_PREFIX}/zapret-config/zapret.sh {start|stop|restart|status}

${C_BLD}Repo:${C_OFF} https://github.com/AndreyShatl/gateway-universal
EOF
