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

## todo — хвосты / на будущее

### T16 · Кросс-компиляция release-бинарников gateway-ui (ARM) · M · todo
Сейчас install собирает gateway-ui на месте через go (требует Go на устройстве). Для Raspberry Pi
и тонких клиентов нужны готовые бинарники. Сделать сборку под linux/amd64 + arm64 + arm (v7),
класть в gateway-ui/dist/gateway-ui-<ARCH> (install уже умеет их подхватывать — [install.sh](install.sh)).
**Критерий приёмки:** скрипт сборки (gateway-ui/build-release.sh) выдаёт бинарники под 3 арх;
install на устройстве без Go ставит UI из dist/.

### T17 · Усилить хеш пароля UI (sha256 → bcrypt/argon2) · L · todo
Сейчас пароль в /etc/gateway/ui.conf — salt+sha256 ([gateway-ui/main.go](gateway-ui/main.go)). Для
админ-доступа лучше KDF. Перейти на bcrypt/argon2 (golang.org/x/crypto — появится зависимость,
нужен go.sum/вендоринг для офлайн-сборки).
**Критерий приёмки:** новый формат хеша, обратная совместимость или миграция; сборка офлайн не ломается.

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
  - cloudflare (в списке) → VPS (egress 2a0d:d940:...), ipify (не в списке) → провайдер (195.178.4.131).
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
G2 direct=195.178.4.133 vs через VPS=2a0d:d940:1a:1813::2 ✓.

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
