# AGENT.md — инструкции для Claude при деплое gateway-universal

Этот файл читает будущий Claude-агент, когда пользователь просит «раскатай gateway на новую железку». Держи его рядом с репой, чтобы агент знал контекст без долгого опроса.

## Что это за проект

`gateway-universal` — шаблон для **прозрачного шлюза обхода блокировок** на Debian/Ubuntu. Ставится одной командой:

```bash
bash deploy.sh --host root@IP
```

Компоненты: xray (VLESS+Reality, нативно, без Docker) + zapret (nfqws из исходников) + iptables + Instagram QUIC bypass.

## Минимум, что надо спросить у пользователя

Вопросы задавай **в таком порядке**. Если у пользователя нет ответа — подскажи, как узнать.

### 1. Куда деплоим
- **IP целевой машины** (пример: `192.168.1.69`)
- **Root пароль** (если ключа нет) — агент использует `sshpass`
- *(опционально)* SSH-порт, если не 22

### 2. VPS (VLESS+Reality endpoint)
Всё это лежит в 3x-ui панели VPS. Попроси пользователя открыть «Inbounds» → выбрать нужный → «...» → «Показать QR» / «Скопировать URL».

- `VPS_ADDR` — IP или домен VPS
- `VPS_PORT_VISION` — порт Vision inbound'а (часто `443` или `8443`)
- `VPS_PORT_GRPC` — порт gRPC inbound'а (часто `2083`). Нужен **второй inbound** для Meta/Instagram. Если у пользователя нет — скажи: «нужен второй inbound в 3x-ui, transport=gRPC, flow=(пусто), service name=`grpc-meta`».
- `VPS_UUID_VISION`, `VPS_UUID_GRPC` — UUID клиентов (разные для двух inbound'ов)
- `VPS_PUBKEY` — Reality Public Key (из деталей inbound'а)
- `VPS_SHORT_ID` — Reality Short ID
- `VPS_SERVER_NAME` — Reality SNI. Дефолт `www.cloudflare.com` работает у большинства.
- `VPS_FINGERPRINT` — `chrome` (дефолт, почти всегда ок)

### 3. Сеть
- `LAN` — локальная подсеть. Дефолт `192.168.0.0/16` (покрывает все типовые домашние сети). Спрашивай только если пользователь явно хочет что-то узкое.
- `IFACE` — внешний интерфейс. Скрипт сам определит через `ip route`, обычно не надо спрашивать.

## Типовой план действий

1. `cd ~/Projects/gateway-universal && git pull` — взять свежий шаблон
2. `cp config.example.env config.env` и заполнить (можно вписать сразу через `sed`/`tee`, не открывая редактор)
3. `bash deploy.sh --host root@IP --password 'xxx' --config ./config.env --yes`
4. Мониторить вывод `install.sh` — он сам делает healthcheck в конце. Если `VLESS proxy works — exit IP: ...` совпадает с `VPS_ADDR` — всё ок.
5. Подсказать пользователю настроить **DHCP роутера**: Gateway = IP машины, DNS = `8.8.8.8`.

## Что нельзя забывать

### iptables персистентность
`install.sh` сохраняет `rules.v4` **до** старта zapret. Это важно: если сохранить после, в файл попадут NFQUEUE правила, и при ребуте `netfilter-persistent` их восстановит, потом zapret попытается добавить их снова и упадёт с `nfq_create_queue: Operation not permitted`. Проверил зимой 2026 — эта грабля реальная.

### Instagram QUIC bypass
QUIC (UDP/443) глобально DROP'ается в FORWARD чтобы клиенты падали на TCP через xray. Но для Meta IP подсетей в `zapret.sh start()` добавляются ACCEPT правила **перед** DROP — иначе Instagram зависает. Эти правила динамические, в `rules.v4` их нет.

### Две VLESS inbound'а
Нужны **два** inbound'а на VPS:
- Vision (`xtls-rprx-vision`, TCP transport) — основной канал
- gRPC (flow=пусто, transport=grpc, serviceName=`grpc-meta`) — для Instagram (Vision падает при многих параллельных соединениях на видео)

Если у пользователя пока один — деплой можно сделать на одном (оба UUID одинаковые, оба порта одинаковые), но Instagram будет подтуплять на видео.

### Zapret стратегии (работающие на текущих ТСПУ)
Их менять не надо без нужды — калиброваны под актуальный DPI. См. [docs/STRATEGIES.md](docs/STRATEGIES.md).

## Если что-то не работает

1. `ssh root@IP 'systemctl status xray zapret'` — что лежит?
2. `ssh root@IP 'journalctl -u xray -n 50 --no-pager'`
3. `ssh root@IP 'journalctl -u zapret -n 50 --no-pager'`
4. `ssh root@IP 'curl --max-time 5 --socks5-hostname 127.0.0.1:1080 https://api.ipify.org'` — xray работает?
5. `ssh root@IP 'iptables -t mangle -L POSTROUTING -n -v'` — NFQUEUE правила есть?

Больше — в [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md).

## Идемпотентность

`install.sh` можно безопасно перезапускать — он проверяет, что уже установлено, и не качает заново. Для полной чистки:
```bash
bash deploy.sh --host IP --uninstall
```

## Что этот агент **не** должен делать

- Не менять стратегии zapret без явной просьбы пользователя (они тонко настроены)
- Не трогать `iptables-save` пока zapret запущен — в результате правила NFQUEUE попадут в `rules.v4` и сломают ребут
- Не ставить Docker — проект специально нативный
- Не коммитить `config.env` в git (он в `.gitignore`)
