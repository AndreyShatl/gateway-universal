# TASKS — задачи проекта

Формат: ID · название · статус (todo/in_progress/done/blocked) · уровень (L/M/H) · критерий приёмки · решение.
Статусы обновляются СРАЗУ после события, не в конце сессии.

---

## done

### T1 · Исправить устаревшие подписи портов в install.sh · L · done
Промпты опроса помечали Vision как «основной», gRPC как «для Meta» — противоречило роутингу.
**Приёмка:** [install.sh:168-169](install.sh:168) — gRPC спрашивается первым как ОСНОВНОЙ, Vision как запасной. ✓
**Решение:** DECISIONS 2026-06-10.

### T2 · Развернуть каркас процесса (CANON/CLAUDE/GOALS/TASKS/DECISIONS) · M · done
**Приёмка:** файлы созданы, CANON ссылается на реальные файл:строка. ✓

---

## todo — UI: навигация и новые разделы

### T18 · Каркас навигации дашборда (адаптивный сайдбар) · M · done
Дашборд разложен по разделам с боковым меню (sticky-сайдбар на широком экране; на узком —
верхняя горизонтальная полоса через @media). Разделы: Обзор (ping/статус/exit IP/smoke),
Подключение (заглушка T19), Домены, Zapret (заглушка T20), Сеть (IP роутера), Сервисы (рестарт/логи).
Активный раздел запоминается в localStorage.
**Приёмка (стенд):** 6 разделов в HTML, переключение работает, все API живы (status/domains/router/restart/logs),
сервис active. Выкатано на живой UI.

### T19 · Подключение: импорт vless:// ссылки (как в VPN-клиентах) · H · done
[gateway-ui/connection.go](gateway-ui/connection.go): GET /api/connection — текущее подключение
READ-ONLY с маскировкой секретов (чтобы случайно не сбить); POST link=vless://… → parseVless
(Reality, grpc/tcp) → запись в config.env → render-config + restart xray. Раздел «Подключение» в UI:
текущее (read-only dl) + поле вставки ссылки.
**Приёмка (стенд):** GET отдаёт маскированные addr/порты/sni/uuid/pubkey/sid; POST валидной ссылкой
(реконструированной из config.env) применяется идемпотентно, xray active; невалидная→400.

### T20 · Zapret: просмотр работающих стратегий + домены · H · done
[gateway-ui/zapret.go](gateway-ui/zapret.go): GET /api/zapret парсит РЕАЛЬНО запущенный nfqws
(`pgrep -a nfqws`, сегменты по --new) → стратегии {queue, proto, ports, l7, desync, repeats,
fooling, hostlist + домены}. Раздел Zapret в UI: карточки стратегий, домены под `<details>`. Только чтение.
**Приёмка (стенд):** 7 стратегий из живого nfqws (instagram 69 / discord 21 / general 20 / youtube 43 /
udp…), desync совпадает с реальными аргументами процесса.
**На потом (T21, если нужно):** правка стратегий и поиск.

## todo — Zapret: поиск рабочих стратегий (архитектура в DECISIONS 2026-06-11)

### T22 · Бэкенд поиска стратегий (обёртка blockcheck, фон, хранение) · H · done
[gateway-ui/scan.go](gateway-ui/scan.go): API /api/scan (GET статус), /api/scan/start, /api/scan/stop.
Раннер пишет run.sh (env DOMAINS/ENABLE_*/SCANLEVEL/PARALLEL, QNUM=59780, авто-сборка mdig/tpws
при отсутствии), запуск `systemd-run --unit=gateway-scan --collect`. Состояние в /etc/gateway/scan/
(job.json, scan.log); рабочие стратегии парсятся из лога (regex "working strategy found for ipv\d+ DOMAIN : STRAT").
Гейт: ip_forward=1 + zapret active. После ребута unit нет + job=running → interrupted.
**Приёмка (стенд):** старт→фон, лог стримит реальный перебор blockcheck; стоп→stopped/inactive;
изоляция подтверждена — SMOKE PASS 9/9 во время скана (клиенты/xray/zapret целы).

### T23 · UI: раздел поиска стратегий в Zapret · M · done
В разделе Zapret карточка «Поиск рабочих стратегий»: форма (домены, чекбоксы HTTP/TLS1.2/TLS1.3/QUIC,
уровень quick/standard/force, параллельность), Старт (заблокирован если предусловия не ок) / Стоп,
статус + список найденных рабочих (накопительно) + лог под `<details>`, авто-опрос каждые 5с пока идёт.
**Приёмка (стенд):** форма в HTML, /api/scan can_start=true, старт/стоп/опрос работают (проверено в T22).

### T24 · Применить найденную стратегию (per сервис/прото оверрайды) · H · done
zapret.sh: desync каждого блока из DESYNC_<BLOCK> (TCP instagram/discord/general/youtube,
UDP instagram/general/discord), дефолты калиброванные, оверрайды из /etc/gateway/zapret-overrides.env.
API ([gateway-ui/zapret.go](gateway-ui/zapret.go)): GET /api/zapret/strategies, POST /api/zapret/strategy
(set desync / action=reset) → рестарт zapret. UI: карточка «Стратегии по сервисам» (textarea per блок,
Сохранить/Сброс) + «Применить в блок» из результатов поиска (вставляет для проверки перед сохранением).
**Приёмка (стенд):** set tcp_general repeats=8 → override-файл + nfqws repeats=8 + zapret active;
reset → убрано; smoke 9/9; UI отдаёт 7 блоков.

### T25 · Обновление zapret из апстрима (bol-van) · M · done
API ([gateway-ui/zapret.go](gateway-ui/zapret.go)): GET /api/zapret/version (коммит+описание+статус),
POST /api/zapret/update → фон через systemd-run (unit gateway-zupdate): git fetch --depth 1 +
reset --hard FETCH_HEAD + make nfq/mdig/tpws + рестарт zapret, лог в /etc/gateway/zupdate.log.
UI: карточка «Движок zapret» (версия, кнопка Обновить с подтверждением, прогресс+лог). Стратегии/домены не трогаются.
**Приёмка (стенд):** версия отображается; обновление отработало в фоне (rc=0, пересборка),
zapret active, smoke 9/9 (апстрим уже был на последней — конвейер сработал вхолостую корректно).

## todo — Zapret: динамические сервисы

### T26 · Движок: zapret.sh генерируется из services.json · H · done
zapret.sh: функция build_proto читает [zapret/services.json](zapret/services.json) через jq,
генерит nfqws-сегменты (--new на сервис) + NFQUEUE на очереди 200(tcp)/201(udp). Сид мигрирует
текущие блоки (instagram/discord/general/youtube/quic_fallback). install ставит jq + копирует
services.json (сид + /etc/gateway/zapret-services.json). Заменяет статические блоки и T24-overrides.env.
**Приёмка (стенд):** 2 nfqws (q200/q201), сегменты идентичны прежним (hostlist/l7/desync), smoke 9/9.
**Баг по ходу:** read с IFS=tab схлопывал пустые поля → разделитель сменён на .

### T27 · CRUD сервисов в UI (заменяет редактор T24) · H · done
API GET/POST /api/zapret/services ([gateway-ui/zapret.go](gateway-ui/zapret.go)) — читает/пишет
весь список в /etc/gateway/zapret-services.json (валидация) → рестарт zapret. UI «Сервисы zapret»:
список с inline-правкой (имя, домены, каналы tcp/udp: порты+l7+desync), «+ Сервис»/«+ Канал»/удаление,
«Сохранить всё». Старый редактор фикс-блоков и /api/zapret/strategy* убраны. Из поиска — «Скопировать стратегию».
**Приёмка (стенд):** GET 5; POST +тестовый сервис → iptables 8444 + hostlist материализован + smoke PASS;
восстановление сида → smoke PASS.
**Хвост:** в Go остались неиспользуемые upsert/removeConfigVar + overrides (от T24) — подчистить при случае.

### T28 · Домены zapret автоисключаются из VPS-роутинга · M · done
Домен в zapret-сервисе должен идти напрямую (локальный DPI-обход), а не в туннель.
build-domains.sh ([xray/build-domains.sh](xray/build-domains.sh)) вычитает домены из zapret services.json
(jq) из VPS-списка. handleServices (POST) перегенерирует xray-конфиг + рестарт xray + zapret.
**Нюанс:** вычитаются только точные домены; широкий geosite:* не трогается (убирать вручную).
**Приёмка (стенд):** youtube/instagram/discord (в zapret) → 0 в proxy-mux; anthropic (не в zapret) остался;
POST сервисов перегенерил xray; smoke 9/9.

### T29 · Featured-сервисы с переключателем VPN/Zapret · H · done
Сервис получил поля `featured` + `mode` (vps|zapret). YouTube/Discord/Instagram — featured
(дефолт mode=vps), со своими стратегиями и полными доменами. Переключатель в UI (рамка «Ключевые»),
прочие — во второй рамке (zapret-only). mode=vps → домены в VPS-туннель (build-domains добавляет);
mode=zapret → сегмент nfqws + исключение из VPS. Сохранение перегенерирует xray+zapret.
**Приёмка (стенд):** дефолт — featured в VPS (zapret крутит только general+quic); youtube.com в VPS,
twitch исключён; toggle youtube→zapret → youtube в nfqws + вне VPS; обратно ок; smoke 9/9.
**Примечание:** install.sh при установке перерендеривает /opt/zapret-config/zapret.sh (mode-фильтр) — на боевом применится при следующем install/деплое.
**Доработка по фидбеку:** переключатели VPN/Zapret ключевых сервисов вынесены в сайдбар (рамка ⭐Ключевые),
применяются сразу; в карточке VPN-режима zapret-поля (каналы) скрыты; channels не затираются при scrape в VPN.

### T30 · YouTube/Discord/Instagram — отдельные вкладки, 3 режима, поиск · H · done
Ключевые сервисы вынесены в свои вкладки навигации (YouTube/Discord/Instagram). В каждой:
3-режимный переключатель VPN / Zapret / Напрямую, полный список доменов сервиса, поля стратегий
(каналы tcp/udp) только в режиме Zapret, кнопка «Найти стратегии» (scan по доменам сервиса).
Режимы: vps→в VPS-роутинг; zapret→nfqws+исключение из VPS; direct→исключение из VPS и не в zapret.
build-domains: include=vps, exclude=не-vps. zapret.sh: сегменты только mode==zapret. Дефолт featured=vps.
Вкладка Zapret → «Прочие сервисы» (без featured). Сайдбар-панель убрана.
**Приёмка (стенд):** 3 вкладки/секции; toggle youtube vps→direct → не в VPS и не в zapret (materialized=только general); smoke 9/9.

### T31 · Доводка вкладок сервисов (фидбек) · M · done
1) убрано поле имени в featured-вкладках (имя в заголовке); 2) домены featured — только просмотр
(read-only `<details>`); 3,4) поиск стратегий перенесён в каждую вкладку сервиса (виджет с чекбоксами
HTTP/TLS12/TLS13/QUIC + уровень быстрый/стандартный/глубокий + лог рабочих) — единый фоновый job,
домены берутся из сервиса; 5) тултипы (title) на чекбоксах и уровнях с кратким+подробным пояснением.
Виджет переиспользуемый (Zapret + 3 вкладки). scrapeSvc не трогает имя/домены featured.
**Приёмка (стенд):** маркеры в HTML, smoke 9/9.
**Доводка 2 (owner):** поиск помечается владельцем (id сервиса или "zapret"), хранится в job;
виджет показывает результаты/лог только если job.owner совпадает с владельцем раздела, иначе
«идёт поиск в другом разделе». Проверено: owner=discord в статусе, smoke 9/9.

### T32 · Подключение: несколько хостов + переключение/удаление · H · done
[gateway-ui/connections.go](gateway-ui/connections.go): /api/connections (GET список масок,
POST add|activate|delete). Хранилище /etc/gateway/connections.json (0600). add парсит vless,
activate пишет VPS_* в config.env + render + restart xray + помечает active. UI вкладки Подключение:
текущее (read-only) + список хостов (активировать/удалить) + форма добавления (имя + vless).
**Приёмка (стенд):** add→list→activate (xray active, active=True)→delete; smoke 9/9.

### T33 · Zapret показывает все стратегии КРОМЕ featured · M · done
handleZapret исключает из «работающих стратегий» сегменты featured-сервисов
(youtube/discord/instagram): по hostlist-id и по l7 (discord voice). Их стратегии видны в их вкладках.
**Приёмка (стенд):** discord=zapret → в Zapret остаются general + quic(all), discord (tcp+udp-l7) исключён; smoke 9/9.

### T34 · Доводка: редактирование хостов, анимация поиска, чистка обзора · M · done
PR #42. Подключения: «Текущий хост» из config.env управляем + кнопка «Изменить»; индикатор
хода поиска (спиннер/полоса) с параметрами; в Обзоре статус только xray/zapret/gateway-ui.
**Приёмка (стенд):** «Текущий хост» (active) в списке; smoke 9/9.

### T35 · Починка залипания статуса поиска + кэш Safari · H · done
PR #43. Корень: код выставлял `el.className='msg'`, затирая служебный класс `sw-pre`/`sw-msg`,
после чего pollScan падал на null и UI замерзал. Сохраняем структурные классы + null-guard.
`Cache-Control: no-store` на HTML и всех API (Safari кэшировал GET /api/scan). Авто-подхват версии
вкладкой (mtime бинаря). Отзывчивый «Стоп».
**Приёмка (стенд):** диагностика через консоль Safari; статус обновляется непрерывно, «Стоп» за ~1с.

### T36 · Привязка статических VPS-правил к режиму сервиса · H · done
PR #44. Тумблер сервиса не управлял статикой в шаблоне (Meta-IP-блок, geosite:facebook/instagram
в DNS и роутинге) — Instagram уходил на VPS в обход zapret. Помечены `"comment":"svc:<id>"`,
render-config (`--services`) jq-фильтром убирает их, если сервис не в режиме vps.
**Приёмка (стенд):** instagram=zapret → Meta-IP/geosite=0, xray -test VALID; discord=vps не тронут.

### T37 · Не заворачивать локальный трафик шлюза в nfqws · M · done
PR #45. POSTROUTING NFQUEUE ловил и форвард клиентов, и собственный трафик шлюза — desync рвал
его соединения (curl со шлюза таймаутил, ломались git-обновление/exit-IP/blockcheck). Добавлен
`-m addrtype ! --src-type LOCAL`.
**Приёмка (стенд):** stop zapret → curl 302/0.14s; с фиксом при работающем zapret — снова 302; клиенты не задеты.

### T38 · Готовые стратегии flowseal + панель применения · H · done
PR #46. Каталог `gateway-ui/strategies.json` (20 стратегий из flowseal/zapret-discord-youtube:
general+ALT..ALT12+FAKE TLS AUTO+SIMPLE FAKE, по именам файлов, TCP/UDP-сегменты, токен `$FAKE`).
`/api/strategies` (embed). Панель «Готовые стратегии» справа в каналах: → TCP/UDP/оба. zapret.sh:
`$FAKE`→/opt/zapret/files/fake. install.sh кладёт fake-болванки. Глазик показа пароля на входе.
**Приёмка (стенд):** /api/strategies 20 пресетов; все $FAKE/*.bin существуют; токен разворачивается.

### T39 · Доводка панели готовых стратегий · M · done
PR #47. Featured-сервисы: каналы фиксированы (proto залочен, без +/×) — стратегии всегда в нужный
канал. Стиль панели под UI. Авто-рост поля desync (TCP+UDP, пересчёт при показе вкладки).
Применение без ре-рендера — скролл списка не сбрасывается. Выравнивание рамок.
**Приёмка (стенд):** подтверждено пользователем; поля раскрываются, скролл сохраняется.

### T40 · Кнопка «Сохранить» в карточке сервиса · M · done
PR #48. У featured-сервисов кнопка 💾 Сохранить во всех режимах; тумблер VPN/Zapret/Напрямую
больше не применяется мгновенно — «изменено, нажмите Сохранить». Применение стратегии тоже
сохраняется по кнопке. saveServices принимает целевой элемент сообщения.
**Приёмка (стенд):** подтверждено пользователем.

### T-deploy · Боевая (192.168.1.106) приведена к актуальному состоянию · — · done
Накат на боевую (2026-06-17/18): новый бинарь gateway-ui (aarch64), fake-болванки, токен `$FAKE`
в live zapret.sh (БЕЗ addrtype — по решению пользователя), services.json мигрирован (featured/mode).
Найден и закрыт пробел деплоя: на боевой был старый `build-domains.sh` (без exclude-логики) —
zapret-домены не вычитались из VPS, YouTube шёл через туннель; залит свежий, xray перерендерен.
Репо боевой синхронизирован с main. Бэкапы: `/root/gw-backup-*/rollback.sh`, `/root/gw-repo-backup-*.tar.gz`.

## todo — хвосты / на будущее
- **Game mode** (порт-диапазон 1024-65535, тумблер Off/TCP/UDP/TCP+UDP) — обсуждён, ещё не сделан.
- **addrtype-фикс на боевой** (T37) — намеренно НЕ применён к live zapret.sh; применить при следующем install.sh или по решению.

### T16 · Кросс-компиляция release-бинарников gateway-ui · M · done
[gateway-ui/build-release.sh](gateway-ui/build-release.sh) собирает amd64/arm64/armv7l (CGO off, -s -w).
Релиз v0.1.0 опубликован (3 ассета). install.sh скачивает gateway-ui-$(uname -m) из releases/latest
во временный файл + mv (иначе "Text file busy" при переустановке поверх запущенного бинарника),
фолбэк — сборка из исходников. + procps в apt-зависимостях.
**Решение по доступу:** репозиторий сделан ПУБЛИЧНЫМ (anonymous curl к release работает).
Перед публикацией из HEAD убраны инфра-IP (не креды; config.env/UUID/пароли в истории не светились).
**Приёмка (стенд):** install → «скачан из release (x86_64)», сервис active, /healthz ok, вход 303;
смена арх покрыта (бинарники под aarch64/armv7l в релизе). Go на устройстве не нужен.

### T17 · Усилить хеш пароля UI (sha256 → bcrypt) · L · done
Пароль теперь bcrypt (golang.org/x/crypto/bcrypt v0.31.0, go 1.22). Новый ui.conf — bcrypt-хеш
($2a$…); старый формат salt:sha256 распознаётся и работает (обратная совместимость).
**Приёмка (стенд):** новый пароль→$2a$, верный→303/неверный→401; legacy sha256-conf→верный 303/неверный 401.
**Примечание:** офлайн-сборки на устройстве больше нет (T16 — бинарник из релиза), вендоринг не нужен.

## todo — веха: веб-интерфейс gateway-ui (архитектура в DECISIONS 2026-06-10)

Порядок: T8→T9 (предусловия) → T10 (каркас) → T11–T13 (функции) → T14 (install) → T15 (доки).

### T8 · Вынести рендер config.json в xray/render-config.sh · M · done
Создан [xray/render-config.sh](xray/render-config.sh): резолв VPS_ADDR→IP, build-domains, envsubst,
`xray -test` по временному .json до атомарной подмены (невалидный конфиг больше не затирает рабочий).
install.sh зовёт его ([install.sh:286](install.sh:286)).
**Приёмка (стенд 192.168.1.132):** install → `xray config valid`, proxy=386 (идентично прежнему),
temp прибран, SMOKE PASS. Локально JSON proxy=386/direct=11/rules=10.
**Баг по ходу:** mktemp без .json → `xray -test` «failed to get format»; пофикшено суффиксом .json.

### T9 · build-domains.sh читает доп. каталог /etc/gateway/domains/ · M · done
build-domains.sh принимает несколько каталогов и дедуплицирует между ними; по умолчанию
xray/domains + /etc/gateway/domains. render-config.sh передаёт оба (--user-domains-dir).
**Приёмка (стенд):** с /etc/gateway/domains/local.txt → 387 (тестовый домен в proxy-mux, xray -test ок),
без него → 386; несуществующий каталог не ломает (локально проверено).

### T10 · Каркас gateway-ui (Go): сервер, auth, embed · H · done
Создан [gateway-ui/](gateway-ui/) (Go, stdlib-only): HTTP `--listen`, вход по паролю
(salt:sha256 в /etc/gateway/ui.conf, первый пароль из env GATEWAY_UI_PASSWORD), сессии-cookie,
embed.FS-шаблоны, /healthz, /api/ping, дашборд с заглушками T11–T13.
**Приёмка (стенд, Go 1.24):** build OK; /healthz=ok; без сессии→303 /login; неверный пароль→401;
верный→303+cookie; /api/ping под auth→{"status":"ok"}; дашборд отдаётся.
**Осталось на потом:** systemd-юнит и bind-на-LAN+iptables — в T14; sha256→bcrypt/argon2 — позже.

### T11 · UI: поле IP роутера · M · done
Логика fix-gateway вынесена в [systemd/apply-fix-gateway.sh](systemd/apply-fix-gateway.sh) (общий код
с install.sh). UI: GET/POST `/api/router-ip` ([gateway-ui/router.go](gateway-ui/router.go)) — читает/пишет
ROUTER_IP в config.env (атомарно, права сохраняются) + зовёт apply-скрипт; форма в дашборде.
**Приёмка (стенд):** GET=192.168.1.1; POST невалидный→400 без смены маршрута; POST валидный→200,
fix-gateway applied, маршрут цел, интернет жив, config.env обновлён.

### T12 · UI: списки доменов · M · done
[gateway-ui/domains.go](gateway-ui/domains.go): GET/POST `/api/domains` (action=add|remove) →
правит /etc/gateway/domains/local.txt → render-config.sh → restart xray. Валидация домена/geosite,
дедуп, откат файла при неудачном применении. Список с удалением в дашборде.
**Приёмка (стенд, живой xray):** add невалидный→400; add домен→200, proxy 386→387, в конфиге,
xray active; повтор→«без изменений»; remove→proxy 387→386. Домены вне репо → переживают передеплой (T9).
**Доработка по фидбеку (batch + дедуп с дефолтами):** ввод пачкой (строки/запятые/пробелы);
ответ разбит на added/skipped_present/skipped_default(уже в xray/domains курируемых)/invalid.
Проверено: 2 новых добавлены, youtube.com→skipped_default, мусор→invalid, повтор→skipped_present.
**Доработка 2 (вид):** GET /api/domains отдаёт ещё `defaults` (курируемые, A-Z); в дашборде они
под сворачиваемой вкладкой `<details>` (read-only), пользовательские и дефолтные — отсортированы A-Z.

### T13 · UI: статус и управление · M · done
[gateway-ui/status.go](gateway-ui/status.go): /api/status (сервисы+nfqws), /api/exit-ip
(провайдер vs VPS), /api/restart (whitelist xray/zapret/fix-gateway/discord-tproxy),
/api/smoke (tests/smoke.sh), /api/logs (journalctl). Карточка управления в дашборде.
**Приёмка (стенд):** status отдаёт состояния; exit-ip=провайдер+VPS; restart xray→active,
запрещённый→400; smoke гоняет реальный smoke.sh (честно вернул FAIL сразу после рестарта,
PASS когда xray устоялся); logs отдаёт N строк journalctl.

### T14 · install.sh: интеграция gateway-ui · M · done
install.sh: INSTALL_WEB_UI/WEB_UI_PORT/WEB_UI_PASSWORD, --no-web-ui/--web-ui-port. Бинарник —
prebuilt из gateway-ui/dist/gateway-ui-$ARCH или сборка на месте (если есть go). Пароль из
WEB_UI_PASSWORD или случайный (печатается). Юнит [systemd/gateway-ui.service](systemd/gateway-ui.service)
с LAN-only через ExecStartPre iptables (не пишем в rules.v4). --init-password в бинарнике.
**Приёмка (стенд):** сервис active; /healthz с LAN→ok, с loopback→заблокирован; iptables
ACCEPT 192.168.0.0/16 + DROP остальное на :8088; вход верным паролем→303, неверным→401.
**На потом:** кросс-компиляция release-бинарников под arm (сейчас сборка на месте через go).

### T15 · Доки и smoke под gateway-ui · L · done
README: раздел «Веб-интерфейс (gateway-ui)» (адрес, пароль, возможности, LAN-only, смена пароля).
AGENT: gateway-ui в компонентах + /etc/gateway/domains. smoke.sh: проверка gateway-ui.service active
+ /healthz с LAN (условно — пропуск если не установлен). Мелочь по фидбеку: из выпадашки логов
убраны oneshot-сервисы (fix-gateway/discord-tproxy), остались xray/zapret.
**Приёмка (стенд):** smoke 9/9 PASS (вкл. gateway-ui active + /healthz LAN).

---

### T7 · Протестировать ветку process-scaffold на тестовом стенде · M · done
**Контекст:** выделенный тестовый стенд — Debian 13 trixie x86_64, IP=192.168.1.132.
Домашний боевой шлюз НЕ трогать. Доступ по SSH (пароль, в git не хранится).
**Как тестировали:** апгрейд на месте (rsync кода без config.env + idempotent install,
устройские креды сохранены). Бэкап старого конфига: `/opt/xray/config.json.bak.1781119654`.
**Результат (2026-06-10) — PASS:**
- install: `Routing domains: 386 entries`, `xray config valid`, healthcheck все ✓.
- Селективный роутинг подтверждён через socks-инбаунд:
  - cloudflare (в списке) → VPS (egress = IP VPS), ipify (не в списке) → провайдер (IP провайдера).
- steam = 11 direct-доменов (без регрессии).
**Откат при необходимости:** `cp /opt/xray/config.json.bak.1781119654 /opt/xray/config.json && systemctl restart xray`.
**Процедура (чистая установка с нуля — честный тест fresh-деплоя):**
```bash
git checkout process-scaffold            # на Mac, в репозитории
# config.env должен быть заполнен (VPS_ADDR, UUID, Reality-ключи)
bash deploy.sh --host root@СТЕНД_IP --uninstall   # снести предыдущую версию
bash deploy.sh --host root@СТЕНД_IP                # поставить с нуля с этой ветки
```
**Критерий приёмки:**
- install выводит `Routing domains: 386 entries` + `xray config valid`.
- G1–G4 (healthcheck) PASS.
- G6: с клиента заблокированное → IP VPS. G7: обычный РФ-сайт → IP провайдера.
- D1–D3: Instagram/YouTube/Discord-voice работают.
- Особо: 71 новый домен через VPS ничего не замедлил/не сломал.
**После теста:** решить — мержить в main или скорректировать (часть из 71 увести в direct).

### T3 · Сквозной smoke-тест «шлюз поднимается» · M · done
Создан [tests/smoke.sh](tests/smoke.sh): один прогон → вердикт PASS/FAIL, exit 0/1.
Проверяет G1 (порты :12345/:1080), G2 (egress через VPS != прямой), G3 (nfqws), G4 (NFQUEUE),
+ active сервисов. Локально на шлюзе или удалённо `bash tests/smoke.sh --host root@IP`.
**Приёмка (2026-06-10, стенд 192.168.1.132):** 7/7 PASS, exit 0;
G2 direct=IP провайдера vs через VPS=egress VPS ✓.

### T4 · Сверить остальную прозу с кодом · L · done
Прошёл README/AGENT/docs. Стратегии zapret в STRATEGIES.md и пути установки — совпадают с кодом.
Найдено и исправлено 3 расхождения:
1. TROUBLESHOOTING.md: диагностика `grep -A2 geosite:instagram` сломана после T6 (домены в одном
   правиле) → заменена на python-парсинг JSON. Подтверждено на стенде (старая → пусто).
2. discord-tproxy.service реально ставится ([install.sh:435](install.sh:435)), но отсутствовал в обзорах
   README/AGENT → добавлен в «Что ставится» и в компоненты AGENT.
3. После T6 пополнение доменов через xray/domains/*.txt не было в пользовательской доке →
   добавлен раздел README «Добавить сайт/сервис в обход».
**Приёмка:** правки в README.md, AGENT.md, docs/TROUBLESHOOTING.md.

### T5 · Зафиксировать UUID-опрос в install.sh · L · done
Опрос UUID переставлен: сначала gRPC (основной), Vision-UUID подхватывает дефолт из gRPC
([install.sh:170-171](install.sh:170)). Порядок согласован с портами; типовой случай «один inbound»
работает (Vision = gRPC по умолчанию). Касается только интерактива — non-interactive (config.env) не затронут.
**Приёмка:** `bash -n install.sh` OK; порядок ADDR→gRPC-порт→Vision-порт→gRPC-UUID→Vision-UUID.

### T6 · Сделать xray/domains/ источником истины для роутинга · H · done
Было: два разошедшихся списка (инлайн 315 vs txt 363), txt код не читал.
Сделано (вариант б): [xray/build-domains.sh](xray/build-domains.sh) генерит JSON из txt,
install.sh подставляет в `${ROUTING_DOMAINS}` ([install.sh:286-300](install.sh:286)).
23 proxy-домена доположены в txt (без регрессии), steam/dota оставлены в direct.
**Приёмка:** `bash xray/build-domains.sh` → 386 валидных записей; рендер конфига проходит
`python json.load` ✓; proxy=386, direct=11 (steam цел). Реальный `xray -test` — на установке ([install.sh:293](install.sh:293)).
**Решение:** DECISIONS 2026-06-10.
**Изменение поведения:** +71 ранее-не-проксируемый домен теперь через VPS — проверить демонстрацией (G6/G7).
