# Troubleshooting

## Диагностические команды

```bash
# Статус сервисов
systemctl status xray zapret

# Логи
journalctl -u xray -n 100 --no-pager
journalctl -u zapret -n 100 --no-pager

# Что слушает xray
ss -tlnp | grep -E ':(12345|1080|8080)'

# Что делает zapret
pgrep -a nfqws
iptables -t mangle -L POSTROUTING -n -v
/opt/zapret-config/zapret.sh status

# iptables целиком
iptables-save

# Быстрый тест VLESS
curl --max-time 5 --socks5-hostname 127.0.0.1:1080 https://api.ipify.org
# Должен вернуть IP VPS, НЕ локальный выходящий IP

# Быстрый тест zapret (с клиента)
curl --max-time 5 https://www.youtube.com -I
```

## Типовые проблемы

### `nfq_create_queue: Operation not permitted` при старте zapret
**Причина:** в `/etc/iptables/rules.v4` сохранены NFQUEUE правила (попали туда при `iptables-save` когда zapret был запущен). При ребуте `netfilter-persistent` их восстанавливает → zapret пытается привязаться к уже занятой очереди.

**Лечение:**
```bash
systemctl stop zapret
iptables -t mangle -F POSTROUTING
iptables-save > /etc/iptables/rules.v4   # теперь без NFQUEUE
systemctl start zapret
```

### Старые nfqws процессы не убиваются
Бывает, когда `killall nfqws` не успевает, а новые процессы получают тот же qnum.

```bash
systemctl stop zapret
sleep 2
pgrep -x nfqws && killall -9 nfqws
sleep 1
systemctl start zapret
```

Если совсем упёртые (PPID=1, не дети zapret.service):
```bash
for pid in $(pgrep -x nfqws); do kill -9 $pid; done
```

### Instagram видео зависает / бесконечная загрузка
**Причина 1:** Все соединения идут через `proxy` (Vision) → он захлёбывается на десятках параллельных connection к CDN.
**Лечение:** убедиться что в `xray/config.json` Meta/Instagram идут через `proxy-mux` (gRPC), а не `proxy`.
Список доменов роутинга — единый сгенерированный блок, поэтому проверяем через парсинг JSON:
```bash
python3 -c "import json;d=json.load(open('/opt/xray/config.json'));print([r['outboundTag'] for r in d['routing']['rules'] if 'geosite:instagram' in r.get('domain',[])])"
# должно быть ['proxy-mux']
```

**Причина 2:** QUIC к Meta блокируется глобальным DROP.
**Лечение:** проверить, что Meta IP ACCEPT правила есть **перед** DROP в FORWARD:
```bash
iptables -L FORWARD -n --line-numbers | head -20
# первые строки должны быть ACCEPT для Meta IP подсетей
```

Если нет — `systemctl restart zapret` (он их ставит в `start()`).

### YouTube тормозит
**Причина:** Chrome использует QUIC, который обходит и xray и zapret.
**Лечение:** либо отключить QUIC в браузере (`chrome://flags/#enable-quic` → Disabled), либо убедиться что `BLOCK_QUIC=yes` и DROP UDP/443 стоит в FORWARD (браузер упадёт на TCP, который zapret обработает).

### Discord голос не работает
Голосовые каналы Discord — UDP к специфичным портам (50000-50100, 19294-19344). Нужен `--filter-l7=discord,stun` в zapret.sh:
```bash
grep 'filter-l7' /opt/zapret-config/zapret.sh
# должно быть: --filter-l7=discord,stun
```

### Клиенты в LAN не видят интернет вообще
Сначала — базовая сеть:
```bash
# На шлюзе
sysctl net.ipv4.ip_forward              # должно быть 1
iptables -t nat -L POSTROUTING -n -v    # должно быть MASQUERADE для LAN
ip route                                # default route есть?
ping 8.8.8.8                            # сам шлюз видит?
```

Если всё ок на шлюзе — проверь на роутере: DHCP Gateway действительно указывает на этот шлюз?

### `curl --socks5 127.0.0.1:1080` возвращает локальный IP, не VPS
Xray не дотянулся до VPS. Варианты:
1. VPS упал: `ping VPS_ADDR`
2. Reality параметры неправильные: `journalctl -u xray | grep -i reality`
3. UUID не совпадает с VPS: проверь в 3x-ui

### Xray падает с `failed to load config: unknown field`
Обычно после апгрейда xray. Посмотри changelog — некоторые поля убрали. Типичные:
- `spiderX` → переехал в `fingerprint`
- старые `mux` настройки

Лечение — обновить `xray/config.template.json` под новую версию и перезапустить `install.sh`.

### После ребута шлюз не работает
```bash
systemctl status xray zapret netfilter-persistent
```
Все три должны быть `active`. Если zapret не поднимается — см. первый пункт этого файла.

### Клиенты видят HTTPS, но HTTP (80) не работает / наоборот
Проверь REDIRECT правило:
```bash
iptables -t nat -L PREROUTING -n -v | grep REDIRECT
# должно быть для 80,443 → 12345
```

### TV/Smart устройства не ходят через шлюз
TV часто игнорирует DHCP DNS и ходит в `8.8.8.8` сам. Реши одним из:
- Принудительный редирект DNS: `iptables -t nat -A PREROUTING -s LAN -p udp --dport 53 -j REDIRECT --to-ports 53` (если ставишь свой dnsmasq)
- Или просто прибей жёстко на роутере: «запретить устройство-TV выходить на 8.8.8.8 напрямую»

## Сбор информации для багрепорта

Если что-то не работает и хочется разобраться — собери дамп:
```bash
{
    echo "=== OS ==="; cat /etc/os-release
    echo "=== Uname ==="; uname -a
    echo "=== Sysctl ==="; sysctl net.ipv4.ip_forward net.ipv4.conf.all.rp_filter
    echo "=== Xray status ==="; systemctl status xray --no-pager
    echo "=== Zapret status ==="; systemctl status zapret --no-pager
    echo "=== Xray logs ==="; journalctl -u xray -n 50 --no-pager
    echo "=== Zapret logs ==="; journalctl -u zapret -n 50 --no-pager
    echo "=== nfqws ==="; pgrep -a nfqws
    echo "=== iptables-save ==="; iptables-save
    echo "=== xray config (без секретов) ==="; sed 's/"id": "[^"]*"/"id": "REDACTED"/g; s/"publicKey": "[^"]*"/"publicKey": "REDACTED"/g' /opt/xray/config.json
} > /tmp/gateway-diag.txt
```
