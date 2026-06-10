# CANON — внутренняя правда об архитектуре gateway-universal

> Это канон архитектуры для тимлида и исполнителей. Источник истины — код,
> на который ссылаются пункты ниже (файл:строка). Если код разошёлся с этим
> файлом — прав код, а CANON надо обновить. Пользовательская дока (как ставить)
> живёт в [../README.md](../README.md) и [../AGENT.md](../AGENT.md), сюда не дублируется.

## Назначение

Превращает Debian/Ubuntu-машину в прозрачный шлюз обхода блокировок:
весь LAN-трафик к заблокированным ресурсам уходит через VLESS+Reality на VPS,
DPI обходится локально через zapret. Нативно, без Docker.

## Поток трафика (карта)

```
LAN ──┬─ TCP 80/443 ─► iptables REDIRECT :12345 ─► xray dokodemo-door
      │                                              ├─ заблокировано ─► proxy-mux (gRPC :2083) ─► VPS
      │                                              └─ остальное      ─► direct
      ├─ TCP 80/443 + спец-порты ─► iptables NFQUEUE 200 ─► zapret nfqws (TCP DPI bypass)
      ├─ UDP 443 / discord ─────► iptables NFQUEUE 201 ─► zapret nfqws (UDP DPI bypass)
      ├─ UDP 443 к Meta IP ─────► ACCEPT (Instagram QUIC) — ставится ПЕРЕД DROP
      ├─ UDP 443 прочее ────────► DROP (заставляет браузеры падать на TCP)
      └─ Discord voice UDP ─────► tproxy через xray-туннель
```

## Модули и интерфейсы

### xray (прокси-движок)
- Конфиг-шаблон: [../xray/config.template.json](../xray/config.template.json), рендерится через `envsubst` в [../install.sh:290](../install.sh:290).
- Inbounds: `tproxy-in` :12345 (dokodemo-door TCP/UDP, [config.template.json:36](../xray/config.template.json:36)), `socks-in` :1080, `http-in` :8080, `tproxy-udp` :12346.
- Outbounds: `direct` (freedom), `proxy` (Vision/TCP, **объявлен но не используется в роутинге** — [config.template.json:136](../xray/config.template.json:136)), `proxy-mux` (gRPC, serviceName=`grpc-meta` — [config.template.json:171](../xray/config.template.json:171)).
- **Роутинг**: всё заблокированное → `proxy-mux`; приватные сети, VPS, Steam, BitTorrent → `direct` ([config.template.json:214](../xray/config.template.json:214)).
- **Список доменов роутинга — генерируемый**: единственный источник истины — `xray/domains/*.txt`; [build-domains.sh](../xray/build-domains.sh) превращает их в JSON-массив, install.sh подставляет в `${ROUTING_DOMAINS}` при рендере. Пополнять = дописать строку в .txt (T6). IP-правила Meta/Telegram и Steam-direct остаются захардкоженными в шаблоне.
- systemd: [../systemd/xray.service](../systemd/xray.service), ставится в [install.sh:301](../install.sh:301).

### zapret (DPI bypass)
- Собирается из исходников bol-van/zapret в [install.sh:328](../install.sh:328); бинарь `nfqws`.
- Runtime-скрипт: [../zapret/zapret.sh](../zapret/zapret.sh) (рендерится с подстановкой `LAN`, [install.sh:344](../install.sh:344)).
- Два инстанса nfqws: TCP queue 200 ([zapret.sh:24](../zapret/zapret.sh:24)), UDP queue 201 ([zapret.sh:61](../zapret/zapret.sh:61)).
- Стратегии калиброваны под актуальный ТСПУ — менять только осознанно. См. [STRATEGIES.md](STRATEGIES.md).
- systemd: [../systemd/zapret.service](../systemd/zapret.service).

### iptables
- Базовые правила (NAT/FORWARD/REDIRECT/QUIC DROP): [install.sh:362-392](../install.sh:362).
- **Критично**: `iptables-save` выполняется ДО старта zapret ([install.sh:390](../install.sh:390)), иначе NFQUEUE-правила попадут в rules.v4 и сломают ребут.
- Шаблон персистентности: [../iptables/rules.v4.template](../iptables/rules.v4.template).

### Доп. сервисы (опциональные)
- `fix-gateway` — чинит петлю шлюза, маршрут через реальный роутер ([install.sh:410](../install.sh:410)).
- `discord-tproxy` — Discord voice UDP через туннель ([install.sh:435](../install.sh:435), [../iptables/discord-tproxy.sh](../iptables/discord-tproxy.sh)).
- `ssh-tunnel` — reverse SSH для экстренного доступа ([install.sh:450](../install.sh:450)).

### gateway-ui (веб-интерфейс) — ПЛАН (кода ещё нет)
- Go, один статичный бинарник, фронтенд через embed.FS; systemd `gateway-ui.service`; bind на LAN-IP, доступ по паролю.
- Тонкий оркестратор над примитивами: правит config.env / xray/domains, зовёт `render-config.sh` (ПЛАН — вынести из install.sh) + `build-domains.sh`, рестартит сервисы, гоняет `tests/smoke.sh`.
- Пользовательские домены из UI — вне репо: `/etc/gateway/domains/local.txt` (переживают rsync --delete при передеплое).
- Скоуп v1: IP роутера (ROUTER_IP в config.env), списки доменов, статус/управление. Детали — DECISIONS 2026-06-10, задачи T8–T15.

## Состояние и конфигурация
- Конфигурация деплоя: `config.env` (НЕ в git, см. [../config.example.env](../config.example.env)).
- Параметры берутся в порядке: CLI флаги → env → config.env → интерактив ([install.sh:23-27](../install.sh:23)).
- Деплой с Mac: [../deploy.sh](../deploy.sh) (rsync + ssh, sshpass для пароля).
- Рантайм-состояние на целевой машине: `/opt/xray/`, `/opt/zapret/`, `/opt/zapret-config/`, `/etc/systemd/system/`, `/etc/iptables/rules.v4`.

## Незыблемые инварианты (ломать = баг)
1. `iptables-save` только при остановленном zapret ([install.sh:390](../install.sh:390)).
2. Meta-IP ACCEPT для QUIC ставится ПЕРЕД глобальным DROP ([zapret.sh:15](../zapret/zapret.sh:15)).
3. Основной канал — gRPC :2083, не Vision :443 (ТСПУ режет длинный TLS на :443). См. DECISIONS.
4. Проект нативный — Docker не вводить.
5. config.env не коммитить.
