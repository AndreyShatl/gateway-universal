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

## todo

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

### T5 · Зафиксировать UUID-опрос в install.sh · L · todo
После T1 порядок опроса портов изменён (gRPC первым), но UUID всё ещё спрашиваются
Vision-первым ([install.sh:170-171](install.sh:170)) с дефолтом grpc=vision. Проверить, что это не путает.
**Критерий приёмки:** порядок опроса UUID согласован с портами либо явно обоснован комментарием.

### T6 · Сделать xray/domains/ источником истины для роутинга · H · done
Было: два разошедшихся списка (инлайн 315 vs txt 363), txt код не читал.
Сделано (вариант б): [xray/build-domains.sh](xray/build-domains.sh) генерит JSON из txt,
install.sh подставляет в `${ROUTING_DOMAINS}` ([install.sh:286-300](install.sh:286)).
23 proxy-домена доположены в txt (без регрессии), steam/dota оставлены в direct.
**Приёмка:** `bash xray/build-domains.sh` → 386 валидных записей; рендер конфига проходит
`python json.load` ✓; proxy=386, direct=11 (steam цел). Реальный `xray -test` — на установке ([install.sh:293](install.sh:293)).
**Решение:** DECISIONS 2026-06-10.
**Изменение поведения:** +71 ранее-не-проксируемый домен теперь через VPS — проверить демонстрацией (G6/G7).
