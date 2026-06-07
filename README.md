# gateway-universal

Универсальный шаблон для превращения любой Debian/Ubuntu-машины (Raspberry Pi, тонкий клиент, VPS) в прозрачный сетевой шлюз обхода блокировок **за одну команду**.

Нативный xray (VLESS+Reality) + zapret (nfqws) + iptables + Instagram QUIC bypass. Без Docker.

**Дополнительно:** защита от петли шлюза (fix-gateway) + опциональный реверс-туннель к VPS для экстренного доступа.

## Как это работает

```
Клиенты LAN ──► Этот шлюз ──► WAN
                  │
                  ├─ iptables REDIRECT 80/443 ──► xray :12345 (dokodemo-door)
                  │    └─ VLESS Reality gRPC :2083 ──► VPS (ОСНОВНОЙ канал — весь proxy-трафик)
                  │    └─ direct                      (всё остальное напрямую)
                  │
                  └─ iptables NFQUEUE ──► zapret nfqws (DPI bypass)
                       ├─ YouTube  (TCP+UDP: fake,fakedsplit + ts)
                       ├─ Discord  (TCP: fake-tls / UDP: filter-l7)
                       ├─ Instagram (TCP: multidisorder / UDP: fake-quic)
                       └─ General  (Twitch, ModDB и прочее)
```

> ⚠️ **Почему gRPC, а не Vision на :443.** Российские провайдеры (ТСПУ/DPI) режут
> длинные TLS-сессии к зарубежному VPS на стандартном порту **443** — Reality Vision
> там нестабилен (SYN не доходит, туннель висит). Поэтому весь proxy-трафик идёт через
> **VLESS Reality gRPC на нестандартном порту 2083**, который проходит мимо фильтра.
> Vision-outbound (`proxy`, :443) в шаблоне оставлен, но не используется в роутинге.
> Если у тебя другой регион/провайдер без блокировки :443 — можно вернуть домены на
> `proxy` (Vision) ради чуть большей производительности.

Детали архитектуры — см. [AGENT.md](AGENT.md).

## Быстрый старт

### Вариант A — деплой с Mac на удалённую машину

```bash
git clone git@github.com:andrey-shat/gateway-universal.git
cd gateway-universal
cp config.example.env config.env
$EDITOR config.env                       # заполнить VPS_ADDR, UUID, Reality-ключи
bash deploy.sh --host 192.168.1.69       # спросит пароль, зальёт репо, запустит install.sh
```

Полностью без вопросов:
```bash
bash deploy.sh --host 192.168.1.69 --password 'xxx' --config ./config.env --yes
```

### Вариант B — установка прямо на целевой машине

```bash
git clone https://github.com/andrey-shat/gateway-universal.git
cd gateway-universal
cp config.example.env config.env
nano config.env
sudo bash install.sh
```

Или через переменные окружения без интерактива:
```bash
VPS_ADDR=1.2.3.4 VPS_UUID_VISION=... VPS_PUBKEY=... \
    sudo -E bash install.sh --non-interactive
```

Или чистый интерактив — скрипт задаст все нужные вопросы:
```bash
sudo bash install.sh
```

## Что нужно от пользователя

Минимум (остальное у скрипта есть разумные дефолты):

| Поле | Откуда взять |
|---|---|
| `VPS_ADDR` | IP или домен VPS |
| `VPS_PORT_VISION` | Порт VLESS Vision inbound (обычно 443 или 8443) |
| `VPS_PORT_GRPC` | Порт VLESS gRPC inbound (обычно 2083) |
| `VPS_UUID_VISION` | UUID клиента первого inbound'а |
| `VPS_UUID_GRPC` | UUID клиента второго inbound'а |
| `VPS_PUBKEY` | Reality Public Key |
| `VPS_SHORT_ID` | Reality Short ID |

Эти параметры показываются в 3x-ui панели в деталях inbound'а ("Просмотр" → "..." → скопировать).

## Что ставится

| Компонент | Где |
|---|---|
| xray бинарник + geodata | `/opt/xray/` |
| xray config | `/opt/xray/config.json` |
| zapret (nfqws из исходников) | `/opt/zapret/` |
| zapret runtime скрипт + домены | `/opt/zapret-config/` |
| systemd units | `/etc/systemd/system/{xray,zapret}.service` |
| iptables rules | `/etc/iptables/rules.v4` (через netfilter-persistent) |
| sysctl | `/etc/sysctl.d/99-gateway.conf` (ip_forward=1) |
| fix-gateway.service | `/etc/systemd/system/fix-gateway.service` |
| ssh-tunnel.service (опц.) | `/etc/systemd/system/ssh-tunnel.service` |

## Проверка после установки

```bash
# На шлюзе
systemctl status xray zapret
ss -tlnp | grep -E ':(12345|1080|8080)'         # xray слушает
/opt/zapret-config/zapret.sh status             # nfqws + NFQUEUE правила
curl --socks5-hostname 127.0.0.1:1080 https://api.ipify.org   # должен вернуть IP VPS

# С клиента (после настройки DHCP шлюза на роутере)
curl https://api.ipify.org                      # IP VPS для заблокированных
curl https://www.google.com -I                  # 200 OK
```

## Следующие шаги на роутере

В админке роутера (Keenetic или любой другой):
1. **DHCP → Шлюз (Gateway)** = IP этой машины
2. **DHCP → DNS** = `8.8.8.8`, `1.1.1.1`
3. Перезагрузить клиентов (или отключить/включить Wi-Fi)

> **Важно:** когда шлюз в DHCP указывает на эту машину, она сама получает себя как шлюз и теряет интернет. `fix-gateway.service` решает это автоматически — он устанавливает маршрут через реальный роутер (`ROUTER_IP`) после каждого старта сети.

## Реверс-туннель (экстренный доступ)

Если хочешь иметь доступ к шлюзу через VPS даже когда локальная сеть недоступна — включи в `config.env`:

```env
INSTALL_REVERSE_TUNNEL="yes"
VPS_TUNNEL_PORT="2222"   # порт на VPS для обратного подключения
```

После установки скрипт покажет публичный ключ — его нужно добавить на VPS:
```bash
ssh root@VPS_IP "echo 'ключ' >> ~/.ssh/authorized_keys"
```

Экстренный вход через VPS:
```bash
ssh root@VPS_IP
ssh -p 2222 root@localhost   # ← попадёшь на шлюз
```

## Управление

```bash
systemctl restart xray zapret                   # перезапуск
systemctl stop xray zapret                      # остановка
journalctl -u xray -f                           # логи xray
journalctl -u zapret -f                         # логи zapret
/opt/zapret-config/zapret.sh status             # статус zapret

# Реверс-туннель (если установлен)
systemctl status ssh-tunnel                     # статус туннеля
journalctl -u ssh-tunnel -f                     # логи туннеля
```

## Обновление стратегий Zapret

Если какой-то сервис перестал работать — стратегии редактируются в `/opt/zapret-config/zapret.sh`. Шаблон лежит в `zapret/zapret.sh` в репо. После правок:
```bash
systemctl restart zapret
```

Подробнее про тюнинг — [docs/STRATEGIES.md](docs/STRATEGIES.md).

## Удаление

```bash
sudo bash uninstall.sh          # оставит бинарники
sudo bash uninstall.sh --purge  # удалит всё из /opt
```

С Mac:
```bash
bash deploy.sh --host 192.168.1.69 --uninstall
```

## Troubleshooting

См. [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md).

## Лицензия

Личный проект, MIT.
