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
- **Список доменов роутинга — генерируемый**: источники — `xray/domains/*.txt` (курируемые, в репо) + `/etc/gateway/domains/*.txt` (пользовательские из UI, вне репо, T9); [build-domains.sh](../xray/build-domains.sh) читает оба и превращает в JSON-массив с дедупом. Пополнять = дописать строку в .txt (T6). IP-правила Meta/Telegram и Steam-direct остаются захардкоженными в шаблоне.
- **Рендер config.json — единственное место**: [render-config.sh](../xray/render-config.sh) (T8) делает резолв VPS_ADDR→IP, зовёт build-domains.sh, envsubst, `xray -test` по временному `.json` до атомарной подмены. install.sh ([install.sh:286](../install.sh:286)) и будущий UI зовут именно его — логика не дублируется.
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

### gateway-ui (веб-интерфейс) — каркас готов (T10), функции в работе
- Go, один статичный бинарник (stdlib-only), фронтенд через embed.FS — исходники в [../gateway-ui/](../gateway-ui/) ([main.go](../gateway-ui/main.go)).
- Каркас (T10): HTTP-сервер `--listen`, вход по паролю (salt:sha256 в `/etc/gateway/ui.conf`, первый пароль из env `GATEWAY_UI_PASSWORD` или `--init-password`), сессии-cookie, `/healthz`, `/api/ping`.
- Установка (T14): install.sh собирает/кладёт бинарник в `/opt/gateway-ui/`, юнит [systemd/gateway-ui.service](../systemd/gateway-ui.service) с LAN-only через ExecStartPre iptables (ACCEPT из LAN, DROP остального на порт — не пишем в rules.v4). Флаги INSTALL_WEB_UI/WEB_UI_PORT/WEB_UI_PASSWORD.
- Функции (тонкая обвязка): `/api/router-ip` (T11) — ROUTER_IP в config.env + [systemd/apply-fix-gateway.sh](../systemd/apply-fix-gateway.sh); `/api/domains` (T12) — правка /etc/gateway/domains/local.txt → render-config.sh → restart xray, с откатом при ошибке; `/api/status|exit-ip|restart|smoke|logs` (T13) — статус/управление через systemctl/curl/smoke.sh/journalctl.
- Тонкий оркестратор над примитивами: правит config.env / xray/domains, зовёт `render-config.sh` (готов, T8) + `build-domains.sh`, рестартит сервисы, гоняет `tests/smoke.sh`.
- Пользовательские домены из UI — вне репо: `/etc/gateway/domains/local.txt` (переживают rsync --delete при передеплое).
- Скоуп v1: IP роутера (ROUTER_IP в config.env), списки доменов, статус/управление. Детали — DECISIONS 2026-06-10, задачи T8–T15.

### dnscrypt (шифрованный DNS)
- `dns/setup-dnscrypt.sh` ставит `dnscrypt-proxy` в режиме DoH — резолвинг не читается/не подменяется провайдером на пути детекта и прокси.
- `dns/gateway-dns-redirect.sh` заворачивает локальный DNS-трафик LAN на dnscrypt-proxy.
- systemd: `dnscrypt-proxy.service` — идёт ПЕРЕД детектором в `After=` ([systemd/gateway-detector.service:2](../systemd/gateway-detector.service)), т.к. детектор резолвит через него.
- На боевой (192.168.1.106) стоит, на тестовом стенде — нет (см. DECISIONS).

### detector + автообход «Авто-обход» (T41–T42) — обнаружение блокировок и VPS-fallback
- Исходники: [../detector/](../detector/) — отдельный Go-бинарь `gateway-detector`, три сущности:
  - **prober** ([detector/prober/prober.go](../detector/prober/prober.go)) — активная проверка цели (прямой TCP/TLS vs через VPS socks) → вердикт.
  - **watcher** ([detector/watcher/watcher.go](../detector/watcher/watcher.go)) — пассивный pcap-детект (gopacket) провалов на боевом трафике. Пороги: TCP `synThreshold=5` попыток без SYN-ACK на один dst ([watcher.go:54](../detector/watcher/watcher.go:54)), UDP `udpThreshold=8` исходящих без единого ответа ([watcher.go:76](../detector/watcher/watcher.go:76)); флагует только после порога, чтобы не шуметь на разовые сбои. Отдельно детектит QUIC/HTTP3 с извлечением SNI ([detector/watcher/quic.go](../detector/watcher/quic.go)).
  - **applier** ([detector/applier/applier.go](../detector/applier/applier.go)) — применяет подтверждённый блок в `autoroute.json` + ipset `gw_autoroute`, без рестарта xray.
- CLI: `gateway-detector probe|watch|recheck` ([detector/main.go](../detector/main.go)).
- **Двойной, независимый переключатель** в UI ([../gateway-ui/autoroute.go](../gateway-ui/autoroute.go)) — НЕ единый тумблер «не провайдер → VPS», как может показаться:
  - `detect` — старт/стоп systemd `gateway-detector.service` (autoroute.go:52-54) — только он реально ищет блоки.
  - `route` — реально ли список толкается в ipset `gw_autoroute` и заворачивается на VPS-inbound REDIRECT(TCP)/TPROXY(UDP) (autoroute.go:28-45,49-76); выключен — список хранится, но ipset пуст (ничего не заворачивается).
  - Записи — ручные (домен/IP/CIDR, пачкой) или от детектора с `source`: `rst-after-clienthello` / `syn-timeout` (порог 5 попыток, antishum) / `quic-no-response` / `no-response-after-clienthello` / `legacy`.
- **Подвкладка «Перепроверка» (T42)** — [../gateway-ui/recheck.go](../gateway-ui/recheck.go), конфиг `/etc/gateway/recheck.json`, расписание через override `gateway-recheck.timer` ([systemd/gateway-recheck.timer](../systemd/gateway-recheck.timer), по умолчанию 04:00). Работает **независимо** от тумблера `detect/route` — прогоняет весь список напрямую vs через VPS, убирает адрес из автообхода, если он работает напрямую **два прогона подряд** (антифлак). Это уже частично закрывает то, что ТЗ следующей вехи называет «ночной самоочисткой», но только для VPS-автообхода — до zapret-сущностей «мозга» не дотягивается (это отдельный процесс, см. ниже).
- systemd: [systemd/gateway-detector.service](../systemd/gateway-detector.service) (детект+применение), [systemd/gateway-recheck.service](../systemd/gateway-recheck.service) + `.timer` (ночная чистка автообхода).

### «Мозг» — авто-подбор zapret-стратегий (T41-46 катч-ап 2026-07-20; T48-51 v2 2026-07-20/21)
Цель: минимум пинга — максимум локального zapret, VPS только как fallback, когда zapret не пробивает. Работает **поверх** detector/autoroute (потребляет то, что попало в `autoroute.json`) и поверх статических featured-сервисов из `zapret-services.json` (instagram/youtube/general/discord, мультипрофиль на queue 200/201 — их «мозг» не трогает).

#### gateway.db — whitelist + presets (T48)
Единственная БД в проекте — `/etc/gateway/gateway.db`, схема и весь доступ через
[../scripts/gwdb.py](../scripts/gwdb.py) (stdlib `sqlite3` в python3). **НЕ Go-драйвер**: пробовали
`modernc.org/sqlite`, откатили — тянет cc/ccgo-транспайлер, реальная сборка на стенде (6.6ГБ диск
ЦЕЛИКОМ) упала `no space left on device` (см. DECISIONS 2026-07-20). gateway-ui зовёт тот же
`gwdb.py` через `exec.Command` ([../gateway-ui/db.go](../gateway-ui/db.go)) — единая точка схемы
для Go и bash, ноль новых Go-зависимостей.
- `whitelist(pattern, kind[suffix|exact], note, source, added_at)` — сид (`ru`/`su`/`рф`/`xn--p1ai`
  suffix) заводится в `initGWDB()` ([db.go](../gateway-ui/db.go)) при каждом старте UI.
- `presets(name, proto[tcp|udp], args, source[standard|custom], trusted, success_count, fail_count)`
  — 20 стандартных (flowseal) засеиваются из `gateway-ui/strategies.json` один раз (INSERT-конфликт
  игнорируется). Custom добавляются через `POST /api/presets`.
- API: `/api/whitelist` (GET/POST add|remove), `/api/presets` (GET ?tier=&proto=, POST add) —
  вкладки «Whitelist»/«Пресеты» в dashboard.html (T56).

#### whitelist (T49)
Домены `.ru`/`.su`/`.рф` (+ punycode `xn--p1ai`) не анализируются вообще: проверка в
[../detector/main.go](../detector/main.go) (`isWhitelisted`, шелл в `gwdb.py whitelisted`) — стоит
ДО любой обработки кандидата, включая теневой лог. **Приоритет курируемого списка**:
`inCuratedRouting()` (main.go) читает `xray/domains/*.txt` — если домен там уже прописан на VPS,
whitelist его не перекрывает. Защитно проверяется повторно в `brain-worker.sh` (на случай домена,
попавшего в очередь в обход детектора).

#### Классификация причины блока (T50)
`watcher.go` уже давно тегирует кандидатов сигнатурой (`syn-timeout` / `rst-after-clienthello` /
`no-response-after-clienthello` / `quic-no-response` / `legacy`) — до T50 это никто не читал при
переборе пресетов. Теперь `brain-queue` несёт `domain<TAB>source` (строки без таба = `reeval`,
ночная переоценка). `solve.sh <domain> [source]`: если `source=syn-timeout` (похоже на
IP-блокировку — SYN не доходит вообще) — перебор пропускается, сразу `VPS`. Любой другой source —
перебор как раньше. DNS-подмена отдельно не детектируется (dnscrypt + мульти-IP-фикс уже закрывают
практический кейс, см. DECISIONS).

#### solve.sh — перебор пресетов (T44, тиры — T51, UDP — T57)
[../scripts/solve.sh](../scripts/solve.sh) — ядро перебора через **netns-фейк-клиента**: поднимает
`netns solvns` с veth (solve.sh:~50), форвардит+MASQUERADE через WAN — трафик десинхронизируется
nfqws ТАК ЖЕ, как боевой LAN-клиент (собственный трафик стенда себя не десинхронизирует — это
стоило дня отладки, см. DECISIONS). Тестовые очереди — `QNUM_TCP=59781`/`QNUM_UDP=59782`,
изолированы от боевых 200/201/210+ отдельным ACCEPT. Критичная деталь очистки: `ip link del
veth-s` в teardown — без этого следующий прогон падает/флакует. nfqws запускается БЕЗ `--daemon`,
чтобы `$!` был реальным PID. Пресеты — **не из raw JSON** (было до T51), а из `gateway.db` через
`gwdb.py presets-list --proto tcp|udp` — функция `try_tier <standard|custom>` перебирает один и
тот же код для обоих тиров И обоих протоколов: standard (по `id`, всегда первым) → если ни один,
custom (`trusted DESC, success_count DESC`). Успех custom-пресета → `gwdb.py preset-mark-success`
(trusted=true навсегда). Вывод: `ZAPRET<TAB>proto<TAB>name<TAB>args` | `VPS` | `DIRECT`.
**Протокол выбирается по `source` (T50/T57):** `quic-no-response` → UDP (`curl --http3-only` вместо
обычного curl), всё остальное → TCP. **UDP — особый случай:** мы САМИ глобально DROP'аем UDP/443
кроме Meta-подсетей ([../zapret/zapret.sh:63-70](../zapret/zapret.sh:63), "заставляет браузеры
падать на TCP") — поэтому "работает после снятия нашего же DROP" для UDP это НЕ "DIRECT" (ничего
не делать, как для TCP), а отдельный вердикт `ZAPRET udp accept-only` (пустая стратегия) —
постоянный ACCEPT всё равно нужен, иначе прод-трафик по-прежнему дропается.
- **brain-apply.sh** ([../scripts/brain-apply.sh](../scripts/brain-apply.sh)) — применитель, proto-осведомлённый (T57), v2 (T-consolidate, 2026-07-23). Модель **«группа = стратегия»** (было «сущность = домен» до 2026-07-23, см. DECISIONS): ipset `brain_grp_<10hex>` (хеш от proto+strategy) и iptables-правила общие для ВСЕХ доменов с одинаковой рабочей стратегией (`svc_rules`, proto-ветка — САМА не менялась):
  1. **TCP**: `nat PREROUTING RETURN` — обходит xray tproxy-redirect :12345 (иначе весь TCP 80/443 уходит в xray как LOCAL и до nfqws не доходит). **UDP**: `mangle PREROUTING ACCEPT` + `filter FORWARD ACCEPT` (позиция 1, ПЕРЕД глобальным DROP) — у UDP/443 нет xray-redirect (только `gw_autoroute` ipset через TPROXY), зато есть наш DROP, который и обходим.
  2. `mangle POSTROUTING NFQUEUE` с `--queue-bypass` на своей очереди (`alloc_queue`, аллоцирует от 210+, проверяет И iptables-правила, И реально запущенные nfqws — см. инвариант #22) — один десинхрон-демон на ВСЮ группу. **Пропускается для accept-only** (пустая стратегия, только UDP).
  3. `ensure_group` — найти группу по ТОЧНОМУ совпадению (proto, strategy) или создать новую; `do_zapret` добавляет домен в группу (пересобирая её ipset — resolve+add IP всех доменов группы). CLI не изменился: `zapret <d> <proto> <args>`/`vps <d>`/`remove <d>`/`list`/`restore`; новое: `groups`/`group-of <d>`/`move <d> <group_id>`.
  - `restore` пересоздаёт ВСЕ группы (не по одной на домен) из `/etc/gateway/brain-services.json` после ребута — читает `domains[]` каждой группы, ре-резолвит все IP, один демон на группу.
  - **Инвариант:** `do_remove`/`_detach_domain_from_group` обязаны звать `svc_rules -D` ВСЕГДА при опустевшей группе — иначе accept-only группы при удалении последнего домена оставляют ACCEPT-правила в iptables навсегда (была утечка, закрыта в T57, сохранена и в v2).
  - **Схема состояния сменилась**: было `[{domain,mode,proto,queue,strategy}]` (T44-46/T52), стало `[{group_id,proto,strategy,queue,domains:[...]}]` (T-consolidate) — любой код, читающий `brain-services.json` напрямую (не через brain-apply.sh), должен учитывать новую форму (см. инвариант — `brain-nightly.sh` сломался бы на `x['domain']` без этого фикса).
- **brain-worker.sh** ([../scripts/brain-worker.sh](../scripts/brain-worker.sh)) — v2 (T-consolidate, 2026-07-23): потребитель очереди `/etc/gateway/brain-queue` (atomic pop через flock, строка `domain\tsource`), для каждого домена: whitelist-guard → (1) если домен уже в brain-группе — быстрый `solve.sh --test-args` её текущей стратегии, работает → готово; (2) не работает/домен новый — пробуем ВСЕ существующие группы (`--test-args`, крупные первыми), нашли → присоединяемся без полного перебора; (3) ничего не подошло → полный перебор (`solve.sh domain source`) → `brain-apply.sh zapret <domain> <proto> <args>` (ZAPRET → присоединяет к существующей группе с такой же стратегией или создаёт новую+убрать из VPS; VPS → автообход; DIRECT → пропуск/GC).
- **brain-nightly.sh** ([../scripts/brain-nightly.sh](../scripts/brain-nightly.sh)) — v2 (T-consolidate): складывает в очередь воркера ВСЕ управляемые домены (из `domains[]` каждой brain-группы + VPS-автообход домены, не покрытые featured-сервисом) на переоценку, source=`reeval`. Вся логика "своя группа → другие → полный перебор" — в brain-worker.sh (см. выше), nightly просто наполняет очередь.
- Discord voice **закреплён на VPS** — ни solve, ни nightly его не трогают (через zapret не пробивается; см. DECISIONS).
- systemd: [systemd/gateway-brain-worker.service](../systemd/gateway-brain-worker.service) (постоянный), [systemd/gateway-brain-nightly.timer](../systemd/gateway-brain-nightly.timer) (04:00, `RandomizedDelaySec=1800`), [systemd/gateway-brain-restore.service](../systemd/gateway-brain-restore.service) (на старте, `Before=gateway-brain-worker.service`).
- **У «мозга» нет собственного API/вкладки в gateway-ui** (кроме `/api/whitelist`/`/api/presets`, T48). Монитор ([../gateway-ui/monitor.go](../gateway-ui/monitor.go)) показывает его очереди/профили как побочный продукт разбора `nfqws --new` из cmdline (подписывает доменом из `brain-services.json`), но посмотреть очередь/форсировать re-check можно только через SSH/файлы.
- **Известный технический долг** (изоляция «очередь=сущность» остаётся, см. DECISIONS 2026-07-18 —
  не мержим демоны): состояние вне whitelist/presets по-прежнему размазано по независимым
  JSON-файлам (`brain-services.json`, `autoroute.json`, `zapret-services.json`, `connections.json`,
  `recheck.json`) — сознательно НЕ мигрировано на gateway.db.

#### Пул очередей, активность, idle-стоп, no_bypass (T52-55)
- **Пул очередей** — [scripts/brain-apply.sh:16](scripts/brain-apply.sh:16), `QBASE=210 QPOOL=500`
  (было 50, на стенде уже 25 занятых из 51 упёрлись в потолок). `alloc_queue()` при исчерпании
  пишет в stderr и `return 1` — раньше молча возвращала пустую строку, тихо ломая iptables-правила.
- **Активность** — [scripts/brain-activity.sh](scripts/brain-activity.sh), почасовой таймер
  (`gateway-brain-activity.timer`). Снэпшот пакетных счётчиков NFQUEUE-правил (`iptables -L
  POSTROUTING -n -v -x` — **обязательно `-x`**, без него большие счётчики сокращаются до `135K` и
  парсинг `int()` падает) → `packets`/`last_active` в `brain-services.json`.
- **Idle-стоп** — [scripts/brain-idle-stop.sh](scripts/brain-idle-stop.sh), отдельный таймер
  **в полдень** (`gateway-brain-idle-stop.timer`), НЕ вместе с nightly (04:00) — nightly и так
  безусловно пере-solve'ит/перезапускает все сущности каждую ночь, стоп в то же окно был бы тут же
  отменён. `systemctl stop brain-nfqws-<domain>` для `last_active` старше `IDLE_STOP_HOURS`
  (default 24), ipset/iptables не трогаются. Реактивация — на ближайшем ночном проходе (04:00);
  живого триггера по трафику нет (детектор игнорирует уже-`isBrainEntity` домены). **Важно:**
  `systemctl start` на остановленный `systemd-run --collect` юнит НЕ работает (юнит уже собран
  сборщиком мусора) — реактивация обязана идти через `brain-apply.sh zapret <d> <strat>`
  (пересоздаёт юнит с нуля), что nightly и делает.
- **no_bypass** — `brain-nightly.sh` для VPS-доменов вне сервисов зовёт `gateway-detector probe
  <d> --socks 127.0.0.1:1081`; verdict `down` (уже готовая семантика prober'а: «direct FAIL + vps
  FAIL») → `status:"no_bypass"` + `checked_at` в записи `autoroute.json`; старше
  `NO_BYPASS_CLEANUP_DAYS` (default 30) — запись удаляется. **`direct` >90 дней (из внешнего ТЗ) —
  сознательно не реализован отдельно**, уже закрыт быстрее и точнее через T42
  (`gateway-recheck.timer`, 2 прогона подряд «работает напрямую»).
- **Грабля (закрыта):** [gateway-ui/autoroute.go](gateway-ui/autoroute.go) хранит записи
  `autoroute.json` в типизированной Go-структуре `entry{...}` — любое поле, не объявленное в
  структуре (`Status`, `CheckedAt`), молча терялось при первой же перезаписи файла из gateway-ui
  (add/remove через UI). Добавлять новые поля в `autoroute.json` из bash — ОБЯЗАТЕЛЬНО добавлять
  их и в `entry` struct, иначе gateway-ui их сотрёт при следующем сохранении.

## Состояние и конфигурация
- Конфигурация деплоя: `config.env` (НЕ в git, см. [../config.example.env](../config.example.env)).
- Параметры берутся в порядке: CLI флаги → env → config.env → интерактив ([install.sh:23-27](../install.sh:23)).
- Деплой с Mac: [../deploy.sh](../deploy.sh) (rsync + ssh, sshpass для пароля).
- Рантайм-состояние на целевой машине: `/opt/xray/`, `/opt/zapret/`, `/opt/zapret-config/`, `/opt/gateway-brain/` (solve.sh/brain-apply.sh/brain-worker.sh/brain-nightly.sh/strategies.json), `/opt/gateway-detector` (бинарь), `/etc/systemd/system/`, `/etc/iptables/rules.v4`, `/etc/gateway/*.json` (autoroute/brain-services/zapret-services/connections/recheck — без общей БД, см. «Мозг» выше).

## Незыблемые инварианты (ломать = баг)
1. `iptables-save` только при остановленном zapret ([install.sh:390](../install.sh:390)).
2. Meta-IP ACCEPT для QUIC ставится ПЕРЕД глобальным DROP ([zapret.sh:15](../zapret/zapret.sh:15)).
3. Основной канал — gRPC :2083, не Vision :443 (ТСПУ режет длинный TLS на :443). См. DECISIONS.
4. Проект нативный — Docker не вводить.
5. config.env не коммитить.
6. Discord voice — жёстко на VPS, «мозг» (solve/nightly) его не пере-солвит.
7. brain-сущности требуют `nat PREROUTING RETURN` (обход xray-tproxy) — без него их NFQUEUE получает 0 пакетов (xray уводит трафик как LOCAL раньше).
8. solve.sh обязан удалять `veth-s` в teardown — иначе следующий прогон ловит `RTNETLINK: File exists` и даёт ложный VPS-вердикт (флак, был найден и закрыт 2026-07-18).
9. Схема `gateway.db` (whitelist/presets) живёт ТОЛЬКО в [scripts/gwdb.py](../scripts/gwdb.py) — gateway-ui её не дублирует своей копией, зовёт тот же скрипт. Менять таблицы — только там.
10. Не добавлять Go-зависимости с транзитивными cgo/C-транспайлерами (modernc.org/sqlite и подобные) без предварительной проверки `df -h /` на целевой машине — стенд/боевая это неттопы с диском 6-10ГБ ЦЕЛИКОМ, не разделом (см. DECISIONS 2026-07-20).
11. Новое поле в `autoroute.json`, которое пишет bash (`brain-nightly.sh` и т.п.) — обязательно добавить и в Go-структуру `entry` ([gateway-ui/autoroute.go](../gateway-ui/autoroute.go)), иначе gateway-ui молча стирает его при следующей перезаписи файла (add/remove через UI). Найдено и закрыто на `status`/`checked_at` (T55).
12. `iptables -L ... -v` без `-x` сокращает большие пакетные/байтовые счётчики (`135K`, `2M`) — любой парсинг счётчиков обязан использовать `-x` (точные числа), иначе `int()` падает на первом набежавшем счётчике (T53).
13. UDP brain-сущности (T57) требуют `mangle PREROUTING ACCEPT` + `filter FORWARD ACCEPT` (позиция 1) вместо `nat PREROUTING RETURN` — у UDP/443 нет xray-redirect, зато есть наш собственный глобальный DROP, который и обходим.
14. `brain-apply.sh do_remove` обязан звать `svc_rules -D` БЕЗУСЛОВНО (не только когда есть очередь) — accept-only UDP-сущности (queue=null) иначе оставляют ACCEPT-правила в iptables навсегда (реальная утечка, найдена и закрыта в T57).
15. Сборка eBPF (`bpf2go`/`clang`) на Debian требует `-I/usr/include/x86_64-linux-gnu` — без этого `asm/types.h` не находится (мультиарх-заголовки лежат не в общем `/usr/include`). См. [detector/ebpfsensor/gen.go](../detector/ebpfsensor/gen.go), T58.
16. В TC/eBPF-программах `bpf_skb_load_bytes(skb, off, buf, N)` с переменным `N` — если clang на уровне C докажет `N>0` и уберёт проверку нижней границы как мёртвый код, верификатор всё равно может потерять эту гарантию после усечения регистра до 32 бит (`R4 invalid zero-sized read`). Фикс — `barrier_var(N)` (макрос уже есть в `bpf_helpers.h`) прямо перед проверкой границ, на месте использования. См. [detector/ebpfsensor/sensor.c](../detector/ebpfsensor/sensor.c), T59.
17. Порядок правил в `iptables PREROUTING` между независимыми systemd-юнитами (zapret/discord-tproxy/game-mode/...) на ребуте НЕ гарантирован — `zapret.service` каждый раз перевставляет свои Meta-QUIC ACCEPT в позицию 1 при старте, отодвигая всех, кто стартовал раньше. Новые правила, которым важна позиция относительно других юнитов, должны либо разруливать конфликт диапазоном/условием (не полагаться на порядок), либо явно это учитывать. См. Game Mode (T-gamemode, DECISIONS 2026-07-23) — решено диапазоном портов, не порядком.
18. `mode: "zapret"` в `zapret-services.json` НЕ работает для TCP-каналов на текущей топологии: `nat PREROUTING` редиректит весь LAN TCP 80/443 в xray (`dokodemo`) РАНЬШЕ, чем пакет доходит до zapret'овских NFQUEUE-правил в `mangle POSTROUTING`, а трафик от xray (LOCAL-origin) явно исключён zapret'овским `ADDRTYPE !LOCAL`. Работает только UDP/QUIC. Только per-domain "brain"-сущности (T44-46) имеют RETURN-carve-out в `nat PREROUTING`, дающий обойти редирект — у статических сервисов такого нет. Переключать статический сервис на `mode: "zapret"` для TCP 80/443 без решения этой проблемы — воспроизведёт зависания (см. Instagram, DECISIONS 2026-07-23).
19. Живые `iptables`-команды (`iptables -D ...`) меняют ТОЛЬКО текущее состояние ядра — `/etc/iptables/rules.v4` (загружается `netfilter-persistent.service` при каждой загрузке, ДО zapret/brain) остаётся прежним и при ребуте молча ВОССТАНОВИТ старое правило. Найдено на реальном ребуте (2026-07-23): эксперимент "снять DISCORD_TPROXY редирект" был сделан только live-командой — после перезагрузки редирект вернулся сам. Любое постоянное изменение firewall-поведения обязано также редактировать `/etc/iptables/rules.v4` (или явно исключать себя из этого файла, если оно управляется отдельным systemd-юнитом с восстановлением из своего state-файла, как brain-сущности/game-mode). `rules.v4` — статический скелет (Meta-QUIC ACCEPT, базовые REDIRECT/MASQUERADE), НЕ включает динамическое состояние (brain-сущности, autoroute, game-mode) — те восстанавливаются отдельными юнитами при каждой загрузке, специально не персистятся в этот файл (см. комментарий в `zapret.service`).
20. `zapret.service`'s `ExecStartPre=iptables -t mangle -F POSTROUTING` флашит ВСЕ правила POSTROUTING на каждый (не только boot) рестарт сервиса, включая brain-сущности/группы — ничего автоматически не переприменяет их после ПРОСТОГО рестарта zapret (только `gateway-brain-restore.service` после ПОЛНОГО ребута). Рестарт zapret.service (напр. после смены доменов/`render-config.sh`) осиротит демоны brain, если следом не вызвать `brain-apply.sh restore`. Найдено 2026-07-23 при отладке консолидации: 126 живых nfqws, но 91 уникальная очередь в правилах — 35 демонов работали вхолостую.
21. Имя ipset ограничено 31 символом (`ipset v7.22`) — схема "имя = сущность на домен" (`brain_<sanitized-domain>`) тихо ломается для длинных доменов (35+ символов после `brain_`-префикса): `ipset create`/`iptables -I ... --match-set` молча/шумно проваливаются, а вызывающий код может НЕ заметить (см. T-consolidate: `restore` печатал "восстановлен" несмотря на реальный сбой). Групповая схема (`brain_grp_<10hex>` = 15 символов) структурно устраняет этот класс — короткие детерминированные имена никогда не превысят лимит. Если где-то ещё встретится "имя = произвольная строка пользовательских данных" (домен/URL/etc) для ipset/iptables chain — закладывать короткий хеш вместо прямой санитизации.
22. `alloc_queue` (выбор свободного номера NFQUEUE) обязан проверять НЕ ТОЛЬКО iptables-правила (`queue-num N` в `-S POSTROUTING`), но и реально запущенные nfqws-процессы (`pgrep -a nfqws | grep -oE 'qnum=[0-9]+'`) — правило и живой процесс могут разойтись (см. инвариант #20, осиротевшие демоны), и попытка забиндить уже занятую ядром очередь падает `nfq_create_queue(): Operation not permitted` (EPERM), а не "правило есть, значит занято".

### eBPF-сенсор (T58-59, в разработке — веха в TASKS.md)
[detector/ebpfsensor/](../detector/ebpfsensor/) — новый компонент, **не заменяет** и не трогает
`detector/watcher/` (боевой pcap-путь) напрямую — `watcher.go` даёт общие методы
`OnTCPPacket`/`OnUDPPacket` (пороги/агрегация не дублируются), которые кормит либо pcap
(`detector watch`, боевая команда), либо eBPF (`detector watch-ebpf`, T59, пока только в тени,
без `--apply`, для сверки с pcap перед переключением). Toolchain для сборки
(`clang llvm libbpf-dev linux-headers-amd64 bpftool`, ~700МБ) установлен на стенде — при
переносе на новую машину/боевую это тоже нужно ставить (`install.sh` пока не обновлён под это).
