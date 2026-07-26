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

## todo — веха: авто-мозг обхода блокировок v2 (whitelist + классификация + trusted-пресеты)

Ложится поверх задокументированной вехи T41-T46 (см. раздел «done — веха: авто-обход + мозг» ниже).
Согласовано с пользователем 2026-07-20 (новое ТЗ + блок-схема). Порядок: T48 (фундамент БД) →
T49/T50/T51 (можно параллельно, но T50 удобнее после T49, т.к. классификация должна уважать whitelist).
**Явно ОТЛОЖЕНО на потом** (согласовано, не делать в этой вехе):
- ~~UDP-перебор в мозге~~ — сделано в T57 (2026-07-21), решение отменено по ходу вехи.
- ~~Консолидация групп~~ — T52 сознательно НЕ мержил демоны (DECISIONS 2026-07-21), но решение
  пересмотрено 2026-07-23 (см. T-consolidate) — теперь мержим по общей стратегии. Авто-стоп
  простаивающих демонов (T54, сделано) / чистка stale-записей (T55, сделано).
- Переписывание сенсора detector/watcher.go с pcap на eBPF.
- Полная миграция zapret-services.json/autoroute.json/brain-services.json/connections.json на SQLite —
  новая БД заводится ТОЛЬКО под whitelist и presets, остальное остаётся как есть.

### T48 · БД: SQLite (gateway.db) — таблицы whitelist + presets · M · done
`/etc/gateway/gateway.db`. Таблицы:
- `whitelist(id, pattern, kind['suffix'|'exact'], note, added_at, source['seed'|'manual'])`
- `presets(id, name, proto['tcp'|'udp'], args, source['standard'|'custom'], trusted, success_count, fail_count, last_result_at)`
  — сид: 20 стандартных flowseal-пресетов из `strategies.json` (source=standard; `trusted` на них
  не влияет на порядок — стандартные ВСЕГДА пробуются первыми, флаг актуален только для custom-тира).
Единственная точка схемы/доступа — **[scripts/gwdb.py](../scripts/gwdb.py)** (stdlib `sqlite3`,
без новых пакетов). Изначально планировался Go-драйвер `modernc.org/sqlite` (pure-Go, без cgo) —
**отклонено по факту**: тянет свой cc/ccgo-транспайлер, реальная сборка на стенде (6.6ГБ диск
ЦЕЛИКОМ) упала `no space left on device`. gateway-ui ([gateway-ui/db.go](gateway-ui/db.go)) зовёт
тот же `gwdb.py` через `exec.Command("python3", ...)`, как и bash-скрипты мозга — ноль новых
Go-зависимостей. См. DECISIONS 2026-07-20 (диск).
**Приёмка (де-факто, стенд):** `/api/presets` отдаёт 39 строк (20 пресетов × tcp/udp) сразу после
рестарта gateway-ui; `/etc/gateway/gateway.db` создан.

### T49 · Whitelist: правило .ru/.рф/.su + курируемый список приоритетнее · M · done
Правило: домен, оканчивающийся на `ru`/`su`/`рф`/`xn--p1ai` (punycode-форма .рф), — в whitelist
автоматически, сид заводится в [gateway-ui/db.go](gateway-ui/db.go) (`defaultWhitelistSeed`) при
каждом старте UI. Явный сид госуслуг/банков/Max НЕ добавлен — все реальные проверенные примеры
(gosuslugi.ru, nalog.ru и т.п.) уже покрыты суффиксом `.ru`, отдельные записи были бы избыточны.
**Приоритет:** курируемый `xray/domains/*.txt` выше whitelist — проверяется `inCuratedRouting()` в
[detector/main.go](detector/main.go) (сейчас там нет ни одного `.ru`-домена, конфликт гипотетический,
но проверено кодом). Проверка — в `detector/main.go` (пропуск анализа ДО флага блока, реализовано в
main.go, НЕ в watcher.go как планировалось — watcher не имел доступа к SNI-уровню принятия решений)
и защитно в `brain-worker.sh` (пропуск домена, даже если он как-то попал в очередь).
**UI:** сделан в T56 (вкладка «Whitelist»).
**Приёмка (де-факто, стенд):** синтетический `test-whitelist-check.ru`, вручную дописанный в
`/etc/gateway/brain-queue`, дал в логе `⚪ test-whitelist-check.ru — whitelist, пропуск` — ни один
пресет не запускался, очередь очистилась. `gwdb.py whitelisted mos.ru` → 1, `youtube.com` → 0.

### T50 · Классификация причины блока — переиспользовать `source` детектора · H · done
Не новый детектор с нуля — у watcher.go УЖЕ есть классификация по сигнатуре (`source`:
`syn-timeout` / `rst-after-clienthello` / `no-response-after-clienthello` / `quic-no-response` / `legacy`),
её просто никто не читает при переборе пресетов. Меняем: `brain-queue` несёт `domain<TAB>source` (не
только домен); `solve.sh`/`brain-worker.sh` смотрят на source:
- `syn-timeout` (похоже на IP-уровневую блокировку — SYN не доходит вообще) → **пропустить перебор
  20 пресетов**, сразу VPS (экономит время; можно переоценить позже ночным проходом).
- `rst-after-clienthello` / `no-response-after-clienthello` (сигнатура DPI по SNI) / `legacy` / нет source
  (ручной ввод, ночная переоценка) → обычный перебор как сейчас.
- **DNS-подмена отдельно НЕ детектируем** — dnscrypt уже защищает локальный резолвинг, а разъезд
  IP клиент/шлюз для мульти-IP CDN уже закрыт фиксом 2026-07-18 (детектор добавляет наблюдаемый
  IP клиента в ipset). Дополнительный модуль был бы дублированием — если окажется, что не хватает,
  вернуться к этому отдельной задачей.
Реализовано в [detector/main.go](detector/main.go) (`enqueueBrain(domain, source)` пишет
`domain\tsource`), [scripts/brain-worker.sh](scripts/brain-worker.sh) (парсит строку, source
без табуляции → `reeval`), [scripts/brain-nightly.sh](scripts/brain-nightly.sh) (дедуп по домену
до табуляции, пишет `\treeval`), [scripts/solve.sh](scripts/solve.sh) (принимает `[source]` вторым
аргументом, после baseline-проверки — если `syn-timeout`, пропускает перебор).
**Приёмка (де-факто, стенд):** `bash solve.sh rutracker.org syn-timeout` (реальный заблокированный
домен, вручную, без brain-apply — безопасно для продакшена) → `source=syn-timeout — похоже на
IP-блокировку, перебор пресетов пропущен` → `VPS`, ноль строк «пробую пресет». `bash solve.sh
rutracker.org rst-after-clienthello` — обычный путь (baseline/перебор), классификация не мешает.
Через полный конвейер: `printf 'test\tsyn-timeout\n' >> brain-queue` → в логе `(source=syn-timeout)`
→ `VPS` мгновенно.

### T51 · Custom/trusted-пресеты — тир после стандартных 20 · M · done
Стандартные 20 — всегда первыми (без изменений, теперь читаются из `presets` через
`gwdb.py presets-list --tier standard`, а не напрямую из `strategies.json` — раньше это
делал `jq` по файлу, `PRESETS` env/переменная убрана из solve.sh/brain-worker.sh за ненадобностью).
Если ни один не сработал — тир custom (изначально пусто, добавляются через
`POST /api/presets action=add` — [gateway-ui/db.go](gateway-ui/db.go)), отсортированные
`trusted DESC, success_count DESC` (сортировка — в `gwdb.py cmd_presets_list`). Первый успех
custom-пресета → `trusted=true`, `success_count++` (`solve.sh` зовёт `gwdb.py preset-mark-success`
сразу по факту успеха, до возврата вердикта). Никакого парсинга `/etc/zapret/custom/*.bat` (таких
файлов на шлюзе нет).
**UI:** сделана в T56 (вкладка «Пресеты»).
**Приёмка (де-факто, стенд):** `preset-add test_custom_a/b` → `presets-list --tier custom`
возвращает оба нетронутыми (trusted=0); `preset-mark-success` на втором → он встаёт ПЕРВЫМ в списке
(trusted DESC). Сквозной прогон: `bash solve.sh rutracker.org rst-after-clienthello` — реальный
блок, standard-тир из БД нашёл `general (ALT)` (id=3) на 2-й попытке, идентично поведению до
миграции с raw JSON на БД (регрессии нет). Custom-тир с реальным доменом, где все 20 стандартных
проваливаются, не тестировался живым трафиком (риск для прод-доменов) — логика идентична
standard-тиру (одна и та же функция `try_tier`), проверена статически + сортировка/mark-success
проверены напрямую через gwdb.py.

### T52 · Пул очередей: расширить + падать громко при исчерпании · L · done
Найдено при подготовке T53-55 (2026-07-21): на стенде (2 ядра, 1.9ГБ RAM, из них 783МБ свободно)
уже 25 живых `brain-nfqws-*` (память не проблема — ~2МБ/процесс, ~55МБ суммарно), но
`alloc_queue()` в [scripts/brain-apply.sh:21-27](scripts/brain-apply.sh:21) перебирает всего
`QBASE..QBASE+50` (210-260) — 51 слот, уже занята половина. При исчерпании `alloc_queue` молча
возвращает пустую строку, `svc_rules` получает пустой `$q` — тихая порча iptables-правил без
единой строки в логе. **Изоляция (реш. 2026-07-18) остаётся как есть** — не мержим демоны, только
раздвигаем потолок и ловим исчерпание явно.
**Приёмка (де-факто, стенд):** `QPOOL=500` в [scripts/brain-apply.sh:16](scripts/brain-apply.sh:16),
`alloc_queue` при неудаче пишет в stderr и `return 1`, `do_zapret` ловит через `||` и не создаёт
сущность (`bash -n` + `brain-apply.sh list` прошли без ошибок после деплоя).

### T53 · Активность сущностей: снэпшот счётчиков → last_active · M · done
Фундамент под idle-stop (T54). Новый systemd-таймер (почасовой) читает пакетные счётчики
NFQUEUE-правила каждой сущности (`iptables -t mangle -L POSTROUTING -v -n`), сравнивает с
предыдущим снэпшотом (новое поле `packets` в `brain-services.json`); счётчик вырос — обновить
`last_active` (ISO-таймстемп). Ничего не останавливает, только копит данные.
Реализовано как отдельный почасовой таймер [systemd/gateway-brain-activity.timer](systemd/gateway-brain-activity.timer)
→ [scripts/brain-activity.sh](scripts/brain-activity.sh). **Грабля найдена и закрыта:** `iptables -v`
без `-x` сокращает большие счётчики (`135K`), `int()` падал — добавлен `-x` (точные числа).
**Приёмка (де-факто, стенд):** ручной прогон обновил `packets`/`last_active` во всех записях
`brain-services.json` (проверено на `nnmclub.to`: `packets: 795`, `last_active` = момент прогона).

### T54 · Авто-стоп простаивающих >24ч (настраиваемо) · M · done
Отдельный таймер [systemd/gateway-brain-idle-stop.timer](systemd/gateway-brain-idle-stop.timer)
(полдень, **сознательно НЕ вместе с nightly в 04:00** — nightly и так пере-solve'ит/перезапускает
ВСЕ сущности каждую ночь безусловно; если стопать в то же окно, тот же ночной проход тут же
отменит стоп) → [scripts/brain-idle-stop.sh](scripts/brain-idle-stop.sh): `systemctl stop
brain-nfqws-<domain>` для сущностей с `last_active` (T53) старше `IDLE_STOP_HOURS` (default 24).
ipset и iptables-правила **не трогаются** (ТЗ 8.4). **Ограничение v1 (осознанно):** «запуск по
триггеру трафика» из ТЗ не реализован — детектор игнорирует уже-brain-managed домены (петля-guard
`isBrainEntity`), живой триггер потребовал бы отдельного слушателя. Реактивация — на ближайшем
ночном проходе (04:00), т.е. простой демон стоит примерно с полудня до 4 утра следующих суток.
Если суточная задержка окажется проблемой — отдельная задача на live-триггер.
**Приёмка (де-факто, стенд, синтетический тест на `te-st.org`):** искусственно состарил
`last_active` → `2020-01-01` → прогон `brain-idle-stop.sh` → `systemctl is-active
brain-nfqws-te_st_org` = `inactive`, `ipset list brain_te_st_org` и `iptables -t mangle -S
POSTROUTING | grep te_st_org` — оба на месте (NFQUEUE-правило и ACCEPT не удалены). Другая
сущность (`nnmclub.to`) не затронута. **Важная находка:** `systemctl start` на остановленный
transient-юнит (`systemd-run --collect`) НЕ работает — юнит уже собран сборщиком мусора
(`Unit ... not found`); реактивация возможна только через `brain-apply.sh zapret <d> <strat>`
(пересоздаёт юнит с нуля) — ИМЕННО так и делает nightly, значит реактивация корректна по
конструкции. te-st.org восстановлен вручную после теста (`brain-apply.sh zapret te-st.org ...`),
продакшен не пострадал.

### T55 · no_bypass-статус + очистка устаревших записей · M · done
`brain-nightly.sh` теперь для каждого VPS-домена из `${vps[@]}` (не покрытого zapret-сервисом)
зовёт `gateway-detector probe <d> --socks 127.0.0.1:1081` (verdict `down` = «direct FAIL + vps
FAIL» — переиспользуем готовую логику prober'а, не дублируем) → `status:"no_bypass"` +
`checked_at` в записи `autoroute.json`; verdict `blocked` (VPS всё ещё нужен и работает) снимает
статус, если был, и обновляет `checked_at`. Порог очистки — `NO_BYPASS_CLEANUP_DAYS` (default 30, env).
**`direct` >90 дней (ТЗ 8.5) — сознательно НЕ реализован отдельно**: уже закрыт более быстрым и
точным механизмом T42 (`gateway-recheck.timer`) — снимает адрес после 2 прогонов подряд «работает
напрямую», не ждёт 90 дней. Дублировать не стали, соответствие зафиксировано в CANON.

**Найдена и закрыта серьёзная грабля (не из плана):** [gateway-ui/autoroute.go:117-123](gateway-ui/autoroute.go:117)
хранит записи `autoroute.json` в типизированной Go-структуре `entry{Addr,Added,Source,Port,Clean}`
БЕЗ полей `status`/`checked_at` — при любой перезаписи файла из gateway-ui (add/remove через UI,
POST на `/api/autoroute`) Go молча ронял оба новых поля (unmarshal → marshal без неизвестных полей).
Добавлены `Status`/`CheckedAt` в структуру (с `omitempty`) — без этого T55 работал бы только до
первого клика в UI. Пофиксено и передеплоено (`go build` + `systemctl restart gateway-ui`).

**Приёмка (де-факто, стенд, полный цикл на реальных данных):**
1. Прогон `brain-nightly.sh` (31 VPS-домен, ~5с/домен, реальные network-пробы) нашёл 2 реально
   мёртвых (`ec2-*.compute.amazonaws.com`, не отвечают ни напрямую, ни через VPS) →
   `status:no_bypass` + `checked_at`. Лог: `🚫 no_bypass: помечено 2, снято 0, проверено
   VPS-рабочих 26, удалено устаревших 0`.
2. `GET /api/autoroute` (после фикса Go-структуры) отдаёт оба поля нетронутыми; `POST
   action=add` (реальная запись на диск через `writeAutoRoute`) — поля пережили round-trip.
3. Искусственно состарил `checked_at` обеих записей → `2020-01-01` → прогон логики очистки →
   `removed=2`, `no_bypass remaining: 0` в файле (220 записей осталось). Синтетический
   мусор от тестирования (`test-classify-iptest.example` и т.п.) вычищен из прод-файла вручную.

### T56 · UI: убрать ручное управление (youtube/discord/instagram/zapret/поиск), добавить whitelist+presets · H · done
Пользователь: featured-вкладки (YouTube/Discord/Instagram) и «Zapret (прочие)» были нужны только
для РУЧНОГО переключения VPS/zapret/напрямую — теперь это делает мозг, вкладки не нужны. Убрано:
- [gateway-ui/scan.go](gateway-ui/scan.go) целиком (поиск рабочих стратегий, `/api/scan*`).
- `handleServices`/`handleStrategies`/`handleZapret` + `zService`/`readServices`/`validateServices`
  + helpers (`flagVal`/`splitByNew`/`baseName`/`readLines`) из [gateway-ui/zapret.go](gateway-ui/zapret.go)
  — CRUD сервисов, каталог пресетов, просмотр запущенных стратегий.
- Роуты `/api/scan`, `/api/scan/start`, `/api/scan/stop`, `/api/zapret`, `/api/zapret/services`,
  `/api/strategies` (проверено: все 404 после деплоя).
- `//go:embed strategies.json` в main.go (мёртв — `gwdb.py` читает файл с диска напрямую, T48).
- Вкладки YouTube/Discord/Instagram/Zapret(прочие) + вся их JS/CSS в dashboard.html (~340 строк).
**НЕ тронуто (важно):** `zapret-services.json` как источник конфигурации для
`zapret.sh`/`render-config.sh`/Монитора — редактор убран, файл и то, что его читает, работает
как раньше (Discord=vps, YouTube/Instagram=zapret зафиксированы как есть).
**Оставлено и перенесено**, не удалено: карточка «Движок zapret» (`handleZapretVersion`/
`handleZapretUpdate`, обновление nfqws из апстрима bol-van — это не про ручное переключение
роутинга) переехала из вкладки Zapret во вкладку «Управление».
**Добавлено:** вкладки Whitelist и Пресеты (использует готовый API из T48/T49/T51) — список,
добавление, удаление для whitelist; список (standard/custom, trusted-бейдж, success_count) +
форма добавления custom-пресета.
**Попутно:** `checkVer` (авто-релоад вкладки после деплоя) раньше дёргался только из `/api/scan` —
перевесил на `/api/monitor` (добавил `"ver"` в ответ [monitor.go](gateway-ui/monitor.go)), иначе
авто-релоад сломался бы вместе с поиском. Вычищен мёртвый CSS (`.scanw`,`.zap-strats`,`.mode-tog`,
`.side-feat`,`.st-card` и т.п. — ни разу не использовались в оставшейся разметке).
**Найдена и исправлена ошибка деплоя (не в коде, в процессе):** `scp .../static/dashboard.html`
в общую директорию (без указания `static/` в пути назначения) кладёт файл по basename в корень
`gateway-ui/`, не в `gateway-ui/static/` — `go:embed static/*.html` тогда молча продолжает
встраивать СТАРЫЙ файл. Синтетическая проверка (`grep data-nav` в собранном дашборде) поймала это
сразу после первого деплоя, до того как было объявлено «готово».
**Приёмка (де-факто, стенд):** `curl .../` → `<a data-nav=...>` ровно 9 штук (было 11, -4 +2);
`/api/scan`, `/api/zapret/services`, `/api/strategies`, `/api/zapret` → 404; `/api/zapret/version`
→ 200 с реальными данными (коммит `1a1fc38`); `/api/whitelist`, `/api/presets` → 200 с данными.

### T57 · UDP/QUIC-обход в мозге (было только TCP) · H · done
Найдено при разборе живого лога: домен с `source=quic-no-response` получал TCP-фикс от мозга
(solve.sh always TCP) — иногда «случайно» помогает (браузер падает на TCP), но не чинит саму
UDP-блокировку. Разобрался в реальных iptables: **UDP/443 у нас САМИХ глобально DROP** кроме
Meta-подсетей ([zapret/zapret.sh:63-70](zapret/zapret.sh:63), "заставляет браузеры падать на
TCP") — для НЕ-Meta доменов `quic-no-response` почти всегда означает наш же DROP, не внешний
DPI-блок. Юзер выбрал строить настоящий UDP-обход (не оставлять как есть).
- **Активная проверка `prober.Probe` всегда била по TCP** — для чисто-QUIC кандидата TCP почти
  всегда открыт → verdict=ok → кандидат тихо терялся, до мозга не доходил вообще. Добавлена
  отдельная ветка `quicBlocked()` в [detector/main.go](detector/main.go) — `curl --http3-only`
  напрямую (без VPS-сравнения, SOCKS5 не тащит QUIC) — до общего TCP-пути.
- **solve.sh** ([scripts/solve.sh](scripts/solve.sh)): `source=quic-no-response` → `PROTO=udp`,
  своя тест-очередь `QNUM_UDP=59782`, `curl --http3-only` вместо обычного curl. **Ключевой
  нюанс:** раз блок обычно наш же DROP, "работает после снятия своего же DROP" — это НЕ "DIRECT/
  ничего не делать" (как для TCP), а отдельный вердикт `ZAPRET udp accept-only` (пустая
  стратегия) — постоянный ACCEPT нужен всё равно, иначе прод-трафик по-прежнему дропается.
  Полноценный перебор UDP-пресетов (standard→custom, `try_tier` теперь общая для обоих
  протоколов) остаётся на случай, если ACCEPT сам по себе не помог (настоящий внешний DPI-блок
  QUIC) — этот путь не тестировался живым трафиком (нет под рукой домена с реальным внешним
  QUIC-блоком), но код идентичен уже провalidated TCP-пути.
- **brain-apply.sh**: `svc_rules`/`do_zapret`/`start_daemon`/`state_put`/`do_restore` теперь
  proto-осведомлены. TCP — как было (`nat PREROUTING RETURN` обход xray). UDP — вместо RETURN:
  `mangle PREROUTING ACCEPT` + `filter FORWARD ACCEPT` (позиция 1, перед глобальным DROP), мимо
  очереди/nfqws, если стратегия пустая (accept-only).
- **brain-worker.sh**: формат вердикта расширен `ZAPRET<TAB>proto<TAB>name<TAB>args` (было 3 поля).
**Найден и закрыт баг очистки по пути:** `do_remove` вызывал `svc_rules -D` только если была
очередь (`[ -n "$q" ] && ...`) — accept-only сущности (queue=null) при удалении оставляли ACCEPT-
правила в iptables навсегда (утечка, обнаружено на реальном тесте). Фикс: `svc_rules -D` зовётся
всегда, сама решает по `$q`, чистить ли ещё и NFQUEUE-часть.
**Приёмка (де-факто, стенд, реальный трафик, без риска для прод-доменов):**
1. `bash solve.sh cloudflare.com quic-no-response` (безопасно — Cloudflare не в бою) →
   `прямой доступ клиента: HTTP=301` → `ZAPRET udp accept-only` (внешне не заблокирован, только
   наш DROP мешал).
2. Полный конвейер: `printf 'cloudflare.com\tquic-no-response\n' >> brain-queue` → лог `✅
   cloudflare.com → zapret/udp` → `brain-services.json`: `{"proto":"udp","queue":null,
   "strategy":""}` → `mangle PREROUTING`/`filter FORWARD` ACCEPT на `brain_cloudflare_com`
   реально стоят, `systemctl is-active brain-nfqws-cloudflare_com` = `inactive` (демон и не
   создавался — как задумано).
3. `brain-apply.sh remove cloudflare.com` (после фикса) — оба ACCEPT-правила и ipset снялись
   одним вызовом, проверено `iptables -S` (пусто) + `ipset list` (`does not exist`).

---

## todo — веха: eBPF-сенсор (взамен pcap, согласовано 2026-07-22)

Пользователь подключил Mac напрямую в роутер (LAN, интернет не зависит от шлюза) — можно
безопасно шатать шлюз, включая перезагрузки, ради полного ТЗ. Порядок: сначала PoC (доказать,
что связка вообще работает на этом ядре/железе), потом перенос сигналов один за другим, не
трогая боевой pcap-путь (`detector watch`), пока новый не сравняется по надёжности.

### T58 · PoC: clang → cilium/ebpf → TCX на реальном железе · M · done
Тулчейн (`clang llvm libbpf-dev linux-headers-amd64 bpftool`, ~700МБ, диск был 3.0ГБ) встал
нормально — до катастрофы как с modernc.org/sqlite не дошло, но диск подсел до 1.4ГБ на пике;
почистили (`apt autoremove`, `journalctl --vacuum-size=50M`, `go clean -modcache`) → 2.4ГБ свободно.
**Найдена и закрыта грабля сборки:** clang не находил `asm/types.h` (нужен явный
`-I/usr/include/x86_64-linux-gnu` для мультиарх-заголовков Debian) — без этого bpf2go падает
с `fatal error: 'asm/types.h' file not found`.
Новый компонент [detector/ebpfsensor/](detector/ebpfsensor/) (не трогает существующий
`detector/watcher/`, отдельная CLI-команда `gateway-detector ebpf-poc --iface X --seconds N`):
`count.c` (минимальная TCX-ingress программа, считает пакеты в BPF-массив) +
`sensor.go` (`cilium/ebpf` — `link.AttachTCX`, `ebpf.AttachTCXIngress`, ядро 6.12 поддерживает
tcx с 6.6+, `clsact`/netlink не нужен).
**Приёмка (де-факто, стенд, реальный интерфейс enp2s0):** счётчик реально растёт на живом
трафике (269→458→664→965→1214 пакетов за 5 секунд), без единой ошибки загрузки/атача.

### T59 · Перенос сигналов детектора в eBPF (по одному, сверяя с pcap) · H · in-progress
Сигналы для переноса (см. [detector/watcher/watcher.go](detector/watcher/watcher.go) и
[quic.go](detector/watcher/quic.go)): `syn-timeout` (SYN без SYN-ACK, порог 5 попыток/окно),
`rst-after-clienthello`/`no-response-after-clienthello` (TLS ClientHello без ответа/с RST),
`quic-no-response` (QUIC Initial без ответа), `udp-no-reply` (UDP без ответа, не авто-применяется).
Каждый сигнал — TC-программа с состоянием во флоу-BPF-map (hash map keyed по 5-tuple) +
ring buffer для событий в userspace, где переиспользуется существующая Go-логика
threshold/aggregation из watcher.go (агрегация точно НЕ переписывается с нуля — меняется
только источник событий: pcap.PacketSource → ring buffer reader).
**План проверки:** гонять `detector watch` (pcap, боевой) и новый eBPF-путь ПАРАЛЛЕЛЬНО
(eBPF — в тени, без `--apply`) какое-то время, сверять, что оба ловят одни и те же блоки, прежде
чем переключать боевой путь. Не начинать без явного отмашки — большой объём, по кусочку.

Реализовано: [detector/watcher/watcher.go](detector/watcher/watcher.go) — `Init()` +
`OnTCPPacket`/`OnUDPPacket` вынесены из `handle()`/`handleUDP()` (те же пороги/агрегация,
только источник событий параметризован); [detector/ebpfsensor/sensor.c](detector/ebpfsensor/sensor.c) —
TCX-классификатор (`bpf_skb_load_bytes`, TCP SYN/SYN-ACK все порты, RST/TLS-ClientHello/UDP-QUIC
порт 443) через `BPF_MAP_TYPE_RINGBUF`; [reader.go](detector/ebpfsensor/reader.go) — декодирует
ring buffer, зовёт `OnTCPPacket`/`OnUDPPacket`; [detector/main.go](detector/main.go) —
`buildCandidateHandler` вынесен, общий для `watch` (pcap) и новой `watch-ebpf` (T59) команды.

**Грабля (verifier):** `bpf_skb_load_bytes(skb, off, buf, copy)` с `copy` как переменной — clang
на уровне C доказывает `copy>0` (после `if (plen==0) return`) и убирает нижнюю проверку границы
как мёртвый код; верификатор при этом теряет доказательство после усечения до 32 бит →
`R4 invalid zero-sized read`. Фикс — `barrier_var(copy)` (уже есть в `bpf_helpers.h`) перед
проверкой границ, заставляет верификатор перепроверить диапазон на месте использования.

**Приёмка (де-факто, стенд, 2026-07-22 16:16 → 2026-07-23, идёт 8ч+):** `watch-ebpf --iface enp2s0`
собран и запущен в тени (без `--apply`) параллельно с боевым `detector watch --apply` (systemd),
без падений. QUIC: eBPF-путь поймал `QUIC-блок без SNI на 142.251.127.119` и `172.217.116.4`
СЕКУНДА В СЕКУНДУ с боевым pcap-путём (сверено по `journalctl -u gateway-detector.service`) —
оба корректно пропустили (IP CDN, нельзя маршрутить по домену). TCP: `scbh.yandex.net` (дважды,
16:52/17:03) и `clck.yandex.net` (17:05) совпали секунда-в-секунду, оба пути одинаково не
добавили (`prober=ok`).

**Расхождение (не решено):** `93.158.177.1` — pcap в 16:52:44 сказал `prober=ok, не добавляю`;
eBPF в 16:52:50 (через 6с) сказал `БЫ добавил: блок подтверждён (direct=dial tcp: i/o timeout)`.
Разные вызовы prober в разное время к голому IP без SNI — вероятнее всего реальная сетевая
флуктуация (транзиентная блокировка/разблокировка), а не расхождение в логике детекции сигнала,
но это НЕ подтверждено — сигналы-первопричины (SYN/RST/ClientHello) у обоих путей не сверялись
напрямую, только конечный вердикт prober'а. Считать открытым вопросом.

**Обновление (2026-07-23 00:31):** первый живой TCP-сигнал сошёлся у обоих путей: `74.208.128.53`,
`54.38.206.87`, `141.95.38.210` — pcap сказал "мгновенно в VPS" (уже в автообходе), eBPF сказал
"блок подтверждён" (через prober) — оба независимо согласны, что адрес нужно обходить, разница
меньше 10с между путями. Процесс `watch-ebpf` стабилен 8ч+ (память не растёт, ~30МБ RSS).

**Переключено на боевой путь (2026-07-23 00:42, по явному решению владельца, до полной сверки
всех типов сигналов — принятый риск):** `gateway-detector.service` теперь запускает
`watch-ebpf --apply` вместо `watch --apply` (тот же бинарь `/opt/gateway-detector`, просто другая
подкоманда). Старый pcap-бинарь сохранён как `/opt/gateway-detector.bak-pcap-pre-ebpf` для отката.
**Откат:** `systemctl stop gateway-detector`, поменять `ExecStart` обратно на `watch --apply` в
`/etc/systemd/system/gateway-detector.service` (или восстановить бинарь из бэкапа, если нужен
именно старый код), `systemctl daemon-reload && systemctl start gateway-detector`.
**Приёмка сразу после переключения:** сервис active, процесс реально `watch-ebpf --apply=true`,
ipset `gw_autoroute` не обнулился (282 записи, как до переключения), xray/brain-worker остались
active. **Ещё не подтверждено живьём после переключения:** синтетический тест на заведомо
заблокированный домен для явного `syn-timeout`/`rst`/`no-response-after-clienthello` — до сих пор
не пойман ни разу ни в тени, ни теперь в бою. Следить в ближайшие дни за autoroute.json/логами на
предмет отличий поведения от pcap-эпохи.

### T-consolidate · Консолидация brain-сущностей: группа=стратегия, не группа=домен · H · done
Владелец пересмотрел решение 2026-07-18/21 (`очередь=сущность`, DECISIONS 2026-07-21) после того,
как за один день (ночная переоценка T-static-brain + органический трафик) число демонов выросло
с ~10 до 126 (~260МБ). Явный запрос: "консолидация очень важна... если найдётся общая стратегия
для пула доменов — их в один демон... даже если это займёт больше времени ночью". Указан и способ
группировки: активный перебор пресетов для каждой группы доменов (не просто эвристика).
**Архитектура (не домен → сущность, а СТРАТЕГИЯ → группа доменов):** ipset/очередь/nfqws-демон
теперь один на ГРУППУ (ключ — hash(proto+strategy), 10 hex, `grp_<hash>`), а не на домен. RETURN/
ACCEPT-carve-out (T44-46/T57, не менялся) работает на ipset ГРУППЫ — сколько угодно доменов могут
разделять один и тот же carve-out и одну и ту же nfqws-десинхронизацию, если у них одинаковая
рабочая стратегия. `svc_rules()` (правила) не изменилась вообще — она уже была ipset-агностична.
**Состояние** (`brain-services.json`) сменило форму: было `[{domain, mode, proto, queue, strategy}]`
(запись на домен), стало `[{group_id, proto, strategy, queue, domains:[...]}]` (запись на группу).
**CLI `brain-apply.sh` НЕ ИЗМЕНИЛСЯ** (`zapret <d> <proto> <strat>`/`vps <d>`/`remove <d>`/`list`/
`restore`) — `brain-worker.sh` не пришлось трогать в части вызовов apply, вся группировка спрятана
внутри `do_zapret`/`ensure_group`. Новые действия: `groups` (весь список групп), `group-of <d>`,
`move <d> <group_id>`.
**Побочно найдены и закрыты два реальных доолгоживущих бага (не моих, существовавших с T44-46):**
1. `zapret.service`'s `ExecStartPre=iptables -t mangle -F POSTROUTING` флашит ВСЕ правила
   POSTROUTING на каждый рестарт сервиса, включая brain-сущности — а ничего не переприменяло их
   после ПРОСТОГО рестарта (только после полного ребута, через `gateway-brain-restore.service`).
   Сегодняшние рестарты zapret (Instagram-фикс, обновление доменов) осиротили часть демонов
   (правило снесено, процесс жив). Обнаружено при отладке: 126 живых nfqws, но только 91 уникальная
   очередь в правилах iptables — 35 демонов работали вхолостую.
2. Имя ipset ограничено 31 символом (`ipset v7.22`) — `brain_<санитизированный_домен>` для длинных
   доменов (`brain_api16_access_wf_sg_pangle_io` — 35 симв.) тихо проваливался при создании,
   `brain-apply.sh restore` печатал "восстановлен" несмотря на реальный сбой. 34 из 125 доменов
   имели имя длиннее лимита — сильно пересекается с найденным (1) списком. Групповая схема
   (`brain_grp_<10hex>` = 15 символов) СТРУКТУРНО устраняет этот класс багов — короткие имена
   никогда не превысят лимит.
Также найден и закрыт нюанс `alloc_queue`: сканировал только iptables-правила на занятые очереди —
не видел "осиротевшие" (баг 1) живые демоны без правила, коллизировал с ними
(`nfq_create_queue(): Operation not permitted`, EPERM — не "занято правилом", а реально занято
ядром). Фикс — дополнительно сканировать `pgrep -a nfqws | grep -oE 'qnum=[0-9]+'`.
**solve.sh v5:** добавлен режим `--test-args <domain> <proto> <args>` — проверить ОДНУ конкретную
стратегию для домена (без полного перебора тиров), нужен для "не искать заново, если уже есть
рабочая группа с такой стратегией".
**brain-worker.sh v2 — новый порядок обработки домена** (тот же CLI очереди, что и раньше):
1. Если домен уже в группе — ОДИН быстрый тест (`--test-args`) её текущей стратегии; работает —
   ничего не делаем (не гоняем полный перебор просто так каждую ночь).
2. Не работает (или домен новый) — пробуем ВСЕ существующие группы (тот же proto, от крупной к
   мелкой, `--test-args`) — нашли, присоединяемся, полного перебора не было.
3. Ничего не подошло — старый полный перебор пресетов (как раньше); `ZAPRET` → `ensure_group`
   находит группу по ТОЧНОМУ совпадению строки стратегии или создаёт новую.
**brain-nightly.sh v2:** починен баг (`x['domain']` не существует в новом групповом state —
`KeyError` был бы неизбежен на первом же ночном прогоне) — теперь читает `domains[]` из каждой
группы. Вся "сначала своя группа, потом другие, потом полный перебор" логика — в brain-worker.sh
(process_domain), nightly просто кладёт домены в очередь, как и раньше.
**Миграция (одноразовая, `scripts/brain-apply.sh`+снос старых сущностей старым скриптом —
`brain-apply-v1-per-domain.sh.bak` сохранён для справки):** снесены все 125 старых per-domain
сущностей, пересобраны заново через новый `brain-apply.sh zapret` (что уже само по себе делает
group-by-exact-match). Активный перебор (T-consolidate п.3 задачи) для единственного не
влившегося домена (`network-lc.ru`, уникальный `multisplit`) — протестирован против двух больших
групп через `--test-args`, не подошёл, остался отдельной группой (корректно, это выброс).
**Приёмка (де-факто, стенд, 2026-07-23):**
- **125 доменов → 3 группы** (110-113 TCP на общей "general ALT"-подобной стратегии, 11-15 UDP
  QUIC-fake, 1 outlier) вместо 125 отдельных.
- **126 демонов → 5** (3 группы + 2 статических zapret-сервиса); **память 260МБ → 9.2-9.4МБ**
  (~28×).
- Живой трафик подтверждён сразу после миграции: реальные пакеты идут через новые
  консолидированные правила (`iptables -v` счётчики росли на глазах, LAN-клиенты).
- Все 3 пути `brain-worker.sh` v2 проверены живьём на реальной очереди: (a) `nnmclub.to`/
  `rutor.info` — своя группа всё ещё работает, без полного перебора; (b) `static.criteo.net` —
  новый домен, нашёл существующую группу через `--test-args`, без полного перебора; (c)
  `kinozal.tv` — ни одна существующая группа не подошла, полный перебор запущен, тоже не пробил,
  корректный VPS-фолбэк.
- `restore` (эмуляция ребута: демоны убиты, правила и ipset вручную снесены, state оставлен)
  проверен в изоляции на тестовых доменах — обе группы (TCP и UDP) корректно пересобрались.
**Откат:** бэкапы старых скриптов на стенде — `brain-apply-v1-per-domain.sh.bak`,
`brain-worker-v1.sh.bak`, `brain-nightly-v1.sh.bak`; снапшот старого (per-domain) state —
`/root/brain-services-OLD-snapshot-pre-consolidate.json`. Обратная миграция вручную не
автоматизирована (не ожидается нужной — новая схема строго лучше по всем измеренным метрикам).
**Побочная поломка найдена и закрыта в тот же день:** [gateway-ui/monitor.go](gateway-ui/monitor.go)
(`brainQueues`/`acceptOnlyEntities`) читал `brain-services.json` напрямую со СТАРОЙ схемой (поле
`domain`) — не упало бы (Go тихо оставляет zero-value), но вкладка «Мониторинг» показывала бы
пустые названия доменов на очередях. Исправлено на новую групповую схему (`domains []string`),
`brainQueues` теперь отдаёт домены группы через запятую. Проверено живьём через `/api/monitor` —
реальные домены на очередях 210/211/212. **Известный косметический минус:** для группы 211
(113 доменов) строка в UI очень длинная (не обрезается) — можно улучшить позже (напр. "113
доменов, показать все"), не блокирует функциональность.

### T-static-brain · Ночная проверка zapret для youtube/discord/instagram · M · done
Владелец предложил (2026-07-23): обновить домены youtube/discord/instagram + завести ночную
проверку — пробовать zapret для доменов этих статических сервисов, VPS как fallback (сейчас они
работают ТОЛЬКО через VPS/geosite-роутинг, пассивный детектор их не видит — раз VPS-путь и так
работает, нет сигнала "блок", детектор ничего не enqueue'ит).
**Расследование (важное открытие):** ручной прогон `solve.sh` против реальных доменов Instagram
нашёл рабочий пресет ("general ALT": `--dpi-desync=fake,fakedsplit --dpi-desync-repeats=6
--dpi-desync-fooling=ts ...`) для 4 из 5 протестированных доменов (`i.instagram.com`,
`graph.instagram.com`, `scontent-fra3-1.cdninstagram.com`, `static.cdninstagram.com`,
`scontent.xx.fbcdn.net`) — т.е. Instagram НЕ принципиально непробиваем через zapret, просто
статический захардкоженный пресет в `zapret-services.json` (agressивный `multidisorder`+
`repeats=11`+`autottl`) был неудачным выбором, а до proper per-domain перебора дело не доходило
(см. T-instagram-vps ниже — структурная причина, почему TCP через статический `mode: zapret`
вообще не работает на этой топологии).
**Решение (проще, чем казалось):** НЕ трогать `mode`/статический VPS-роутинг вообще — просто
докидывать домены youtube/discord/instagram (из `.domains[]` в zapret-services.json) в ТУ ЖЕ
очередь `/etc/gateway/brain-queue`, что использует пассивный детектор — существующий
`brain-worker.sh` (уже в работе как systemd-сервис) обрабатывает их идентично органически
обнаруженным кандидатам: `ZAPRET` → создаёт brain-сущность (RETURN в `nat PREROUTING` ставится
ВЫШЕ статического VPS-роутинга по порядку правил — реально уходит с VPS без изменения статического
конфига), `VPS`/`DIRECT` → домен остаётся как был (VPS-путь как страховка). Ноль дублирования
логики solve/apply.
Новый файл: [scripts/brain-static-reeval.sh](scripts/brain-static-reeval.sh) — для каждого домена
youtube/discord/instagram (whitelist-safe, пропускает уже-сущности) кладёт `domain\treeval` в
очередь под тем же `flock`, что и `enqueueBrain` в detector/main.go. Systemd:
[gateway-brain-static-reeval.service](systemd/gateway-brain-static-reeval.service) +
[.timer](systemd/gateway-brain-static-reeval.timer) — **05:00**, НЕ 04:00 (там уже
`gateway-brain-nightly`+`gateway-recheck`, оба тоже дёргают `solve.sh` через общий netns `solvns`
+ фиксированные тестовые очереди 59781/59782 — одновременный запуск подерётся за один ресурс).
**Приёмка (де-факто, стенд, 2026-07-23):** ручной запуск скрипта поставил 120 доменов в очередь
(youtube+discord+instagram, за вычетом уже-сущностей), воркер их разгребает без сбоев — первый же
результат: `✅ m.youtube.com → zapret/tcp (низкий пинг)`, новая brain-сущность реально создалась.
Таймер включён (`systemctl enable`), сработает по расписанию в 05:00.
**Обновление доменов (тот же день):** apple.txt/microsoft.txt не имели строки `geosite:xxx`
(в отличие от telegram/tiktok/whatsapp/linkedin/twitter-x/amazon-aws, у которых она уже была) —
добавил `geosite:apple`/`geosite:microsoft`, категория подтягивается автоматически из geosite.dat.
**Найдено при уборке диска (2026-07-23, позже в тот же день):** geosite.dat/geoip.dat, которые
я якобы обновил утром (при расследовании зависаний Instagram), НА САМОМ ДЕЛЕ не обновились —
`mv geosite.dat.new geosite.dat` либо не выполнился, либо результат не проверился (md5 файла
совпадал с его же `.bak`, т.е. это была старая версия от 31 декабря все эти часы). Функционально
это не аукнулось, потому что курируемые литеральные списки доменов (`xray/domains/*.txt`) и так
покрывали нужные домены независимо от geosite.dat. Применил `.new`-файлы по-настоящему при уборке,
xray перезапущен, проверено (Instagram/YouTube/Apple — 200 через VPS). instagram-meta.txt/
discord.txt/youtube-google.txt —
добавлены отдельные проверенные вручную домены (`fb.watch`, `discord.gift`, `youtubekids.com` и
т.п.), сверено с officialным geosite (facebook/instagram category там на 90% тайпосквот-мусор —
отфильтровано вручную, не слито bulk). `config.json` пересобран (503→532 домена), xray
перезапущен, живьём проверено (Instagram/Apple 200 через VPS).

### T-instagram-vps · Instagram: mode=vps вместо zapret (был сломан структурно) · M · done
Кто-то (не в этой сессии) переключил `instagram.mode` на `"zapret"`, вызвав зависания на телефоне.
Причина — структурная, не просто "нестабильно": `nat PREROUTING` редиректит весь LAN TCP 80/443
в xray РАНЬШЕ, чем пакет доходит до zapret'овских NFQUEUE-правил, а трафик от xray (LOCAL-origin)
явно исключён zapret'овским `ADDRTYPE !LOCAL` — TCP-обход для статических сервисов физически не
применяется (0 совпавших пакетов на NFQUEUE, проверено `iptables -L POSTROUTING -v`). Полная
запись — [DECISIONS.md 2026-07-23](DECISIONS.md), CANON инвариант #18. Вернул `mode: "vps"`,
пересобрал `config.json`, перезапустил xray — подтверждено (`taking detour [proxy-mux]`,
curl быстрый). См. также T-static-brain выше — теперь Instagram ДОПОЛНИТЕЛЬНО проверяется ночью
per-domain на предмет реального zapret-обхода (brain-сущность имеет приоритет над VPS), без
изменения `mode: vps` как страховки.

### T-gamemode · Game Mode: бланкет-ACCEPT для эфемерных портов игровых серверов · M · done
Решение (выбрано пользователем из 3 вариантов): не VPS-роутинг, не detect+auto-bypass через brain —
блáнкет ACCEPT для всего диапазона портов 1024-65535, трафик вообще не анализируется и не
десинхронизируется (минимальная задержка для игр). [iptables/game-mode.sh](iptables/game-mode.sh) —
`apply <off|tcp|udp|both>`, идемпотентно вставляет/убирает ACCEPT в `mangle PREROUTING`+`FORWARD`
(LAN source), состояние — одно слово в `/etc/gateway/game-mode.conf`. UDP-диапазон намеренно
ограничен `1024:49999` (не до 65535) — 50000:65535 занят голосом Discord
([iptables/discord-tproxy.sh](iptables/discord-tproxy.sh), сознательно закреплён на VPS-туннеле,
T46) — пересечение исключено диапазоном, а не порядком правил (порядок между zapret/discord/
game-mode на ребуте не гарантирован, см. существующую грабли ниже). systemd
[game-mode.service](systemd/game-mode.service) restore-на-боевую (читает state-файл, по образцу
discord-tproxy.service). gateway-ui: [gamemode.go](gateway-ui/gamemode.go) — `GET/POST
/api/game-mode`, шеллаут в `/opt/gateway/game-mode.sh`; раздел «Game Mode» в дашборде (группа
«Мозг» в сайдбаре), select Off/TCP/UDP/TCP+UDP + кнопка «Применить». install.sh — установка
рядом с discord-tproxy (тот же паттерн: копия скрипта в `/opt/gateway`, юнит, enable+restart).
**Известная грабля (не моя, существующая):** после ребута `zapret.service` перевставляет свои
Meta-QUIC ACCEPT-правила в позицию 1 (`iptables -I PREROUTING 1`) КАЖДЫЙ раз при старте — если
он стартует позже discord-tproxy/game-mode, отодвигает их вниз по списку. Для этой задачи
не критично (диапазоны не пересекаются), но для будущих доработок PREROUTING — иметь в виду
нестабильный порядок правил между сервисами при ребуте.
**Приёмка (де-факто, стенд, 2026-07-23):** `go build` чистый; `game-mode.sh both/udp/tcp/off`
проверены живьём — `iptables -t mangle -L PREROUTING`/`-L FORWARD` показывают ожидаемые ACCEPT
для `1024:49999` (udp) и `1024:65535` (tcp), `off` убирает всё дочиста; DISCORD_TPROXY-правило
(50000:65535) осталось нетронутым до и после. `/api/game-mode` GET/POST протестирован через
реальный login+cookie (curl с Mac по LAN — localhost на стенде сам зафаерволен для 8088, только
LAN-подсеть). Дашборд отдаёт `data-nav="gamemode"` в разметке. gateway-ui задеплоен и активен.
По умолчанию оставлен `off` (пользователь явно не просил включать сейчас).

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

## done — веха: авто-обход + «мозг» (catch-up 2026-07-20)

Эти задачи были реализованы и закоммичены (30+ коммитов, `feat(autoroute)`/`feat(brain)`) без
единой строчки в TASKS/DECISIONS/CANON — CLAUDE.md требует обновлять доки СРАЗУ, это нарушено.
Ниже — восстановленная задним числом хронология по реальным коммитам, чтобы состояние проекта
снова жило в файлах, а не в истории сессий. Новые задачи по доработке этой вехи — см. следующий
раздел «todo — веха: авто-мозг обхода блокировок v2».

### T41 · Детектор + «Авто-обход»: обнаружение блокировок и VPS-fallback · H · done
Коммиты: c69e500, 0e21cf3, b564262, 4e41345, 0972eb1, 1410f9b, c7ff4cb, c0dc3b1, 30de58f, c2ce60e,
88041e7, 31fbbc9, 05437ad, 021b6b8 (PR #55–#59 + прямые). Отдельный Go-бинарь [detector/](detector/):
prober (активная проверка прямой/VPS) + watcher (пассивный pcap-детект, TCP synThreshold=5/60с,
UDP udpThreshold=8/10с — [watcher.go:54,76](detector/watcher/watcher.go:54), QUIC/SNI отдельно) +
applier (autoroute.json + ipset `gw_autoroute`, без рестарта xray). UI: два НЕЗАВИСИМЫХ тумблера
`detect`/`route` ([gateway-ui/autoroute.go](gateway-ui/autoroute.go)), не единый переключатель.
Детект IP-блоков на всех портах (игровые серверы), UDP игровых серверов через TPROXY.
**Приёмка (де-факто, боевая):** systemd `gateway-detector.service` active; новые блоки появляются
в `autoroute.json` с `source`; тумблер `route` реально включает/выключает заворот в ipset.

### T42 · «Авто-обход»: подвкладка «Перепроверка» · M · done
Коммиты: 41685ee, 9de9d2e (T42 — номер уже стоит в комментарии кода, [recheck.go:12](gateway-ui/recheck.go:12)).
Независимое от `detect/route` расписание (`gateway-recheck.timer`, дефолт 04:00): прогоняет весь
список автообхода напрямую vs через VPS, убирает адрес, если он работает напрямую **два прогона
подряд** (антифлак). Это частичная «ночная самоочистка» — но только для VPS-списка, до
zapret-сущностей мозга (T44-46) не достаёт.
**Приёмка (де-факто):** `gateway-recheck.timer` active; `/etc/gateway/recheck.json` копит
last_run/last_removed/last_pending.

### T43 · Шифрованный DNS (dnscrypt-proxy DoH) · M · done
Коммиты: 35324f8, 771ace6. [dns/setup-dnscrypt.sh](dns/setup-dnscrypt.sh) + `gateway-dns-redirect.sh`.
Резолвинг не читается/не подменяется на пути детектора и прокси. Фикс: сокет с FreeBind переживает
ребут без `network-online` зависимости. **На боевой (192.168.1.106) стоит, на тестовом стенде — нет**
(асимметрия окружений, учитывать при переносе выводов между стендом и боевой).
**Приёмка (де-факто, боевая):** `dnscrypt-proxy.service` active, DNS резолвится через DoH.

### T44 · «Мозг»: solve.sh — перебор готовых пресетов через netns-фейк-клиента · H · done
Коммиты: 48b370e (wip), 3242263 (v3 рабочее ядро), 30506b3 (фикс флака). [scripts/solve.sh](scripts/solve.sh).
Ключевой инсайт: собственный трафик стенда десинхронизируется nfqws НЕ так, как форвардный
клиентский (боевые правила исключают `addrtype !LOCAL`) — оба варианта харнесса (OUTPUT и
POSTROUTING напрямую) давали ложный VPS-вердикт даже для доменов, реально живущих на zapret.
Решение: netns `solvns` + veth, форвард+MASQUERADE через WAN — трафик неотличим от настоящего
LAN-клиента. Тестовая очередь QNUM=59781 изолирована от боевых 200/201. Второй найденный флак:
остаток `veth-s` после teardown ломает следующий netns-setup (`RTNETLINK: File exists`) → тоже
ложный VPS; фикс — явный `ip link del veth-s` в teardown.
**Приёмка (де-факто, е2е на стенде):** rutor.info/nnmclub/rutracker.org → ZAPRET стабильно 3/3 прогона.

### T45 · «Мозг»: brain-apply.sh — применитель zapret-сущности per-домен · H · done
Коммиты: ba0e5b5, df9f441 (фикс `d unbound`). [scripts/brain-apply.sh](scripts/brain-apply.sh).
Архитектура «очередь = сервис = сущность»: каждый домен — своя очередь NFQUEUE (`alloc_queue`,
от 210+), свой ipset `brain_<domain>`, свои iptables-правила. Обязателен `nat PREROUTING RETURN`
для ipset сущности — без него весь TCP 80/443 уходит в xray tproxy REDIRECT как LOCAL раньше,
чем доходит до правил сущности (найдено на реальном клиенте: rutor.info открылся только после
добавления RETURN). `restore` пересоздаёт сущности из `brain-services.json` после ребута.
**Приёмка (де-факто, е2е реальным клиентом без VPN):** rutor.info — блок → solve нашёл стратегию →
brain-apply создал сущность (очередь 210 + ipset) → трафик desync, сайт открылся.

### T46 · «Мозг»: воркер-очередь + ночной проход + авто-триггер + restore · H · done
Коммиты: e44e0ac, 0411e1a, c38e430, f9b4589. [scripts/brain-worker.sh](scripts/brain-worker.sh),
[scripts/brain-nightly.sh](scripts/brain-nightly.sh). Воркер потребляет `/etc/gateway/brain-queue`
(atomic pop, flock) → solve → brain-apply. Детектор при новом блоке enqueue'ит домен автоматически.
Найденные и закрытые в бою баги: петля (детектор пере-ловил уже обработанные домены — фикс: skip
если уже сущность/автообход/hostlist), мульти-IP CDN (Cloudflare/DoH на клиенте резолвит другие IP,
чем стенд — фикс: детектор добавляет наблюдаемый IP клиента в ipset), коллизии очередей, `d unbound`
в do_remove. Discord voice сознательно закреплён на VPS — ни solve, ни nightly его не трогают
(через zapret не пробивается). **На момент T46 ночной проход НЕ делал** консолидацию/idle-stop/
чистку stale — только пересчитывал управляемые домены. Позже добавлены: idle-stop (T54), чистка
stale/no_bypass (T55), консолидация по общей стратегии (T-consolidate, 2026-07-23).
**Обновление 2026-07-23:** VPS-закрепление снято в порядке эксперимента (DISCORD_TPROXY-хук убран,
`discord-tproxy.service` отключён от автозапуска) — DPI прямо сейчас Discord voice не режет,
подтверждено живьём. Статус экспериментальный, не финальный — см. DECISIONS 2026-07-23, там же
команда отката.
**Приёмка (де-факто, е2е):** nnmclub→zapret, kinozal→VPS автономно, без участия человека;
restore пересоздал сущности после ребута.

### T47 · Известный техдолг (не блокирует, но накопился) · L · done
Из аудита gateway-ui (2026-07-20): `overrides`/`--zapret-overrides` в main.go — помечено «(устар.)»
от T24, ничем не читалось; `upsertConfigVar`/`removeConfigVar` в router.go — объявлены, нигде не
вызывались. Оба удалены (2026-07-21). **Устарело к моменту чистки:** «мозг не имеет API/вкладки» —
это закрыто T56 (Whitelist/Пресеты) ещё до этой задачи; не переделывал, просто отметил здесь.
Состояние по-прежнему размазано по независимым JSON без общей схемы/истории событий — принятое
решение (DECISIONS 2026-07-20), не техдолг.
**Приёмка (де-факто):** `grep -rn overrides gateway-ui/*.go` и `grep -rn
'upsertConfigVar\|removeConfigVar' gateway-ui/*.go` — ноль совпадений. `go build` чистый,
gateway-ui задеплоен и активен, `/api/status` отвечает 200.

### T-ui-brain · UI: видимость "мозга" (группы/итоги) + логи детектора/воркера · M · done
Обсудили с владельцем чистку/добавления в UI после T-consolidate. Сделано:
1. **Найдена и починена реальная опасность**, не только косметика: кнопка «Рестарт zapret» в
   разделе «Управление» могла молча осиротить все brain-демоны (CANON #20 — `zapret.service`
   флашит `mangle POSTROUTING` при каждом рестарте, ничего не переприменяет после простого
   рестарта) — **не тронул** саму кнопку в этой сессии (владелец ушёл в магазин до обсуждения
   исправления), см. "на потом" ниже.
2. **Мониторинг: карточка "Мозг: группы по стратегии"** — [gateway-ui/monitor.go](gateway-ui/monitor.go)
   `brainTotals()`/`brainGroupSummaries()`, новые поля `/api/monitor`: `brain_totals`
   (groups/domains/daemons/memory_mb) и `brain_groups` (по группе: proto/queue/count/domains).
   UI: строка с 4 цифрами + список групп, каждая — `<details>` с доменами по клику (было бы
   нечитаемо одной строкой через запятую в старой таблице "Активные профили" — эта проблема сама
   же и была найдена при обсуждении, T-consolidate поменял `service` на comma-joined список).
3. **Логи: добавлены детектор и мозг** — `gateway-ui/status.go` `manageable` карта:
   `gateway-detector` (обычный journalctl, весь его `log.Printf` туда и идёт) + 
   `gateway-brain-worker` (СПЕЦИАЛЬНЫЙ случай — воркер пишет подробный per-domain лог не в
   stdout/journalctl, а в файл `/var/log/gateway-brain.log`; `handleLogs` для этого сервиса
   делает `tail` файла, не `journalctl -u`, иначе показал бы только "воркер запущен" и тишину).
**Приёмка (де-факто, стенд, 2026-07-26):** `/api/monitor` живьём отдаёт `brain_totals`
(`{groups:4,domains:166,daemons:6,memory_mb:13.5}` на момент проверки — органический рост с 3/125
после T-consolidate, ночная переоценка нашла новую группу) и `brain_groups` с реальными доменами;
`/api/logs?service=gateway-detector` и `?service=gateway-brain-worker` оба возвращают осмысленный
живой текст. `go build` чистый, задеплоено, `gateway-ui` active.
**На потом (не сделано в этой сессии, владелец ушёл до обсуждения):**
- Кнопка «Рестарт zapret» должна либо предупреждать про риск осиротения brain-демонов, либо
  автоматически звать `brain-apply.sh restore` сразу после рестарта (безопаснее — просто чинить
  самому, раз `restore` идемпотентен и уже проверен).
- "Активные zapret-профили" (старая таблица, `nfqProfiles()`) для консолидированных групп
  всё ещё показывает `service` как comma-joined список доменов одной строкой — не так плохо, как
  могло бы быть (у большой группы 147 доменов), но можно тоже свернуть в `<details>` по аналогии
  с новой карточкой.

### T-zapret-autoupdate · Еженедельное автообновление движка zapret · M · done
Владелец спросил, обновляется ли движок zapret (bol-van/zapret) — оказалось, механизм ЕСТЬ
(кнопка «Обновить движок» в UI), но только РУЧНОЙ, и не нажимался ~7 недель (стенд был на
`1a1fc38` от 2026-06-06, апстрим ушёл вперёд до `87e0586` от 2026-07-21). При разборе нашли ту же
опасность, что и в "Рестарт zapret" (обсуждали ранее) — обновление тоже заканчивается
`systemctl restart zapret.service`, который флашит `mangle POSTROUTING` (CANON #20) и молча
осиротит все brain-группы (T-consolidate), если не восстановить их следом.
**Решено (владелец):** обновлять раз в неделю, **автоматически**, но так, чтобы демоны не падали.
**Сделано:**
1. И кнопка UI (`gateway-ui/zapret.go` `handleZapretUpdate`), и generic-рестарт (`status.go`
   `handleRestart`, для `service=zapret`) теперь ВСЕГДА зовут `brain-apply.sh restore` через
   `sleep 2` после `systemctl restart zapret.service` — рестарт zapret из UI (кнопкой ли, через
   обновление ли) больше не осиротняет brain-группы.
2. Новый [scripts/zapret-auto-update.sh](scripts/zapret-auto-update.sh) — та же логика для
   systemd-таймера (без HTTP/авторизации): `git fetch` → если новый коммит есть → `reset --hard`
   → пересборка (`nfq`/`mdig`/`tpws`) → `systemctl restart zapret.service` → `brain-apply.sh
   restore`. Если сборка не удалась — откат на предыдущий коммит И пересборка отката (не
   оставляет битые бинарники). Лог — `/var/log/gateway-zupdate-auto.log`.
3. [systemd/gateway-zapret-autoupdate.service](systemd/gateway-zapret-autoupdate.service) +
   [.timer](systemd/gateway-zapret-autoupdate.timer) — **воскресенье 02:00** (не 04:00/05:00,
   где уже brain-nightly/recheck/static-reeval — buffer, чтобы restore успел устаканиться до
   ночных brain-задач, не соревнуясь за netns/тестовую очередь solve.sh).
**Приёмка (де-факто, стенд, 2026-07-26):** таймер включён и запланирован (следующее воскресенье).
Ручной прогон скрипта СРАЗУ (не ждать неделю, раз уже 7 недель отставания) — обновил
`1a1fc38 → 87e0586`, все 4 brain-группы (166 доменов) корректно восстановились сразу после
рестарта zapret, `zapret.service`/`gateway-brain-worker.service`/`gateway-ui.service` все active.

## todo — хвосты / на будущее
- ~~Проверка отказоустойчивости (реальный ребут)~~ — **сделано 2026-07-23**: полный аудит enabled-
  юнитов + реальная перезагрузка стенда. Всё восстановилось штатно (xray/zapret/brain-worker/
  gateway-ui/game-mode/таймеры, ipset автообхода 347 записей, 121 brain-сущность), КРОМЕ:
  Discord-эксперимент (снятие VPS-редиректа) не пережил ребут — `/etc/iptables/rules.v4`
  (грузится `netfilter-persistent` при каждой загрузке) содержал старое правило, живая команда
  `iptables -D` его не трогала. Нашли и исправили — см. DECISIONS 2026-07-23, CANON #19.
- **install.sh не разворачивает "мозг" вообще** (найдено 2026-07-23): `solve.sh`, `brain-apply.sh`,
  `brain-worker.sh`, `brain-nightly.sh` жили ТОЛЬКО на стенде (`/opt/gateway-brain/`), не в `scripts/`
  репо. **Частично закрыто в тот же день** (T-consolidate, при переписывании на групповую модель) —
  все 4 файла перенесены в `scripts/` репо в актуальной (v2, групповой) редакции. Ещё не сделано:
  `install.sh` по-прежнему не копирует их и не ставит systemd-юниты (в отличие от
  `gateway-detector`/`gateway-recheck`, которые ЕСТЬ в install.sh) — чистая установка на новую
  машину всё ещё НЕ получит "мозг". Нужно отдельной задачей дописать install.sh секцию по образцу
  `gateway-detector`. **Заодно добавить в ту же задачу** (2026-07-26): `scripts/zapret-auto-update.sh`
  + `systemd/gateway-zapret-autoupdate.service`/`.timer` (T-zapret-autoupdate) — сейчас тоже
  только на стенде, не в install.sh.
- ~~Game mode~~ — **сделано 2026-07-23**: см. T-gamemode ниже.
- ~~Instagram зависает~~ — **сделано 2026-07-23**: `instagram.mode` был `zapret` (кто-то переключил
  не в этой сессии), вернул на `vps`. Структурная причина — см. CANON инвариант #18, DECISIONS
  2026-07-23. Открытый вопрос на будущее: сделать `mode: "zapret"` рабочим для TCP-каналов
  статических сервисов потребовало бы RETURN-carve-out в `nat PREROUTING` как у brain-сущностей,
  но для целых CDN-диапазонов (не единичный IP) это нетривиально — не делать без отдельного решения.
- ~~addrtype-фикс на боевой (T37)~~ — **снято 2026-07-22**: машина 192.168.1.106 больше не
  существует (владелец подтвердил). Фикс остаётся в шаблоне (`install.sh`) для будущих деплоев,
  просто применять сейчас некуда.
- ~~accept-only UDP-сущности невидимы в Мониторе~~ — **сделано 2026-07-22**: новая секция
  «Accept-only, без десинхронизации» в Мониторе ([gateway-ui/monitor.go](gateway-ui/monitor.go)
  `acceptOnlyEntities()`, `/api/monitor` поле `accept_only`). Заодно нашёл и починил регрессию из
  T56 — CSS-класс `.st-badge` (использовался в существующем рендере сервисов/протоколов) был
  случайно удалён при чистке мёртвого CSS, восстановлен. Проверено живьём: тестовая
  accept-only-сущность (cloudflare.com) появилась в `/api/monitor.accept_only`, после `remove` —
  пропала.

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
