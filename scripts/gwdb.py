#!/usr/bin/env python3
"""gwdb.py — общий доступ к /etc/gateway/gateway.db (whitelist + strategies/services/history, T48+T-are).

Единственная точка схемы для bash-стороны (solve.sh/brain-worker.sh/brain-apply.sh).
Go-сторона (gateway-ui) открывает тот же файл через modernc.org/sqlite — схема
должна оставаться идентичной, менять оба места вместе.

strategies/services/history — три сущности Adaptive Routing Engine (ТЗ T-are):
strategy = один конкретный набор параметров десинхронизации для движка (zapret/
ciadpi/vps/direct), с накопительным score/confidence; service = группа доменов
и текущая привязанная к ней strategy; history = журнал каждой пробы (успех/
провал/задержка) — источник для decay/score и для confidence-гистерезиса
ночной переоценки. Таблица strategies — бывшая presets (T48), мигрируется
переименованием при первом запуске init на старой БД, без потери накопленной
статистики (trusted/success_count/fail_count).

Использование (каждая подкоманда печатает результат в stdout, TSV где не сказано иное):
  gwdb.py init [--strategies-file PATH]          # создать/мигрировать схему + засеять стандартные
  gwdb.py whitelisted DOMAIN                      # "1" | "0"
  gwdb.py whitelist-list
  gwdb.py whitelist-add PATTERN KIND [NOTE]       # KIND = suffix|exact
  gwdb.py whitelist-remove ID
  gwdb.py strategies-list [--tier standard|custom] [--proto tcp|udp] [--engine zapret|ciadpi|vps|direct]
                                                   # id\tname\tproto\targs\tsource\ttrusted\tsuccess_count\tengine\tscore\tconfidence
  gwdb.py strategy-add NAME PROTO ARGS [--engine E]  # добавить custom-стратегию (untrusted)
  gwdb.py strategy-mark-success ID                # trusted=1, success_count+=1, score+=10 (decay-friendly)
  gwdb.py strategy-mark-fail ID                   # fail_count+=1, score-=5
  gwdb.py strategies-decay [--factor 0.995]       # score *= factor для ВСЕХ стратегий (нужен для decay, T-are)
  gwdb.py strategies-export [--engine E] [--tier standard|custom] > FILE.json
                                                   # портативный JSON (без id/score/history) — для GMP OTA/другого шлюза
  gwdb.py strategies-import FILE.json             # идемпотентный upsert по (engine,proto,args); печатает добавлено/пропущено
  gwdb.py strategy-find PROTO ENGINE ARGS         # id стратегии по точному совпадению или пусто
  gwdb.py history-add STRATEGY_ID DOMAIN success|fail [LATENCY_MS]
                                                   # запись пробы (T-are); engine берётся из strategies
  gwdb.py service-touch DOMAIN STRATEGY_ID        # после подтверждённого успеха — обновить confidence/next_reeval_at
  gwdb.py vps-touch DOMAIN success|fail           # то же самое, но для VPS-fallback доменов (T-vps-hysteresis) —
                                                   # свой, более короткий цикл (макс. 3 дня, не 7), чтобы не
                                                   # прозевать долго момент появления прямого обхода
  gwdb.py service-skip-list                       # домены, которых сегодня можно не трогать (T-are, гистерезис)
  gwdb.py services-list                           # domain\tstrategy_id\tengine\tstrategy_name\tconfidence\tlast_reeval_at\tnext_reeval_at
  gwdb.py strategies-explore --proto P --engine E [-n 10]  # N дольше всего не тестировавшихся (T-explore, гарантия покрытия)
"""
import json
import os
import random
import sqlite3
import sys
import time

DB_PATH = os.environ.get("GWDB_PATH", "/etc/gateway/gateway.db")
ENGINES = ("zapret", "ciadpi", "zapret2", "vps", "direct")

SCHEMA = """
CREATE TABLE IF NOT EXISTS whitelist (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pattern TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('suffix','exact')),
    note TEXT,
    source TEXT NOT NULL DEFAULT 'manual',
    added_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_whitelist_pattern ON whitelist(pattern, kind);

CREATE TABLE IF NOT EXISTS strategies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    proto TEXT NOT NULL CHECK(proto IN ('tcp','udp')),
    args TEXT NOT NULL,
    source TEXT NOT NULL CHECK(source IN ('standard','custom')),
    trusted INTEGER NOT NULL DEFAULT 0,
    success_count INTEGER NOT NULL DEFAULT 0,
    fail_count INTEGER NOT NULL DEFAULT 0,
    last_result_at TEXT,
    engine TEXT NOT NULL DEFAULT 'zapret',
    score REAL NOT NULL DEFAULT 100.0,
    confidence INTEGER NOT NULL DEFAULT 0,
    created_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_strategies_name_proto_engine ON strategies(name, proto, engine);

CREATE TABLE IF NOT EXISTS services (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    group_key TEXT NOT NULL UNIQUE,
    domains TEXT,
    current_strategy_id INTEGER REFERENCES strategies(id),
    confidence INTEGER NOT NULL DEFAULT 0,
    last_reeval_at TEXT,
    next_reeval_at TEXT
);

CREATE TABLE IF NOT EXISTS history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id INTEGER REFERENCES services(id),
    strategy_id INTEGER REFERENCES strategies(id),
    domain TEXT,
    engine TEXT,
    result TEXT NOT NULL CHECK(result IN ('success','fail','partial')),
    latency_ms INTEGER,
    bytes INTEGER,
    tested_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_history_service ON history(service_id, tested_at);
CREATE INDEX IF NOT EXISTS idx_history_strategy ON history(strategy_id, tested_at);
CREATE INDEX IF NOT EXISTS idx_history_domain ON history(domain, tested_at);
"""


def now():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def db():
    conn = sqlite3.connect(DB_PATH, timeout=10)
    conn.execute("PRAGMA journal_mode=WAL;")
    conn.execute("PRAGMA busy_timeout=5000;")
    return conn


def migrate(conn):
    """Переименовывает старую таблицу presets (T48) в strategies при первом
    запуске на существующей БД — до executescript(SCHEMA), чтобы CREATE TABLE
    IF NOT EXISTS strategies не создал пустую таблицу поверх переименованной."""
    has_presets = conn.execute(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name='presets'"
    ).fetchone()
    has_strategies = conn.execute(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name='strategies'"
    ).fetchone()
    if has_presets and not has_strategies:
        conn.execute("ALTER TABLE presets RENAME TO strategies")
        conn.commit()
    if has_strategies or has_presets:
        cols = {r[1] for r in conn.execute("PRAGMA table_info(strategies)")}
        if "engine" not in cols:
            conn.execute("ALTER TABLE strategies ADD COLUMN engine TEXT NOT NULL DEFAULT 'zapret'")
        if "score" not in cols:
            conn.execute("ALTER TABLE strategies ADD COLUMN score REAL NOT NULL DEFAULT 100.0")
        if "confidence" not in cols:
            conn.execute("ALTER TABLE strategies ADD COLUMN confidence INTEGER NOT NULL DEFAULT 0")
        if "created_at" not in cols:
            conn.execute("ALTER TABLE strategies ADD COLUMN created_at TEXT")
        conn.commit()
    has_history = conn.execute(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name='history'"
    ).fetchone()
    if has_history:
        hcols = {r[1] for r in conn.execute("PRAGMA table_info(history)")}
        if "domain" not in hcols:
            conn.execute("ALTER TABLE history ADD COLUMN domain TEXT")
            conn.commit()
    has_services = conn.execute(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name='services'"
    ).fetchone()
    if has_services:
        scols = {r[1] for r in conn.execute("PRAGMA table_info(services)")}
        if "confidence" not in scols:
            conn.execute("ALTER TABLE services ADD COLUMN confidence INTEGER NOT NULL DEFAULT 0")
            conn.commit()
        if "vps_streak" not in scols:
            conn.execute("ALTER TABLE services ADD COLUMN vps_streak INTEGER NOT NULL DEFAULT 0")
            conn.commit()


def cmd_init(args):
    strategies_file = None
    if "--strategies-file" in args:
        strategies_file = args[args.index("--strategies-file") + 1]
    conn = db()
    migrate(conn)
    conn.executescript(SCHEMA)
    conn.commit()
    seeded = 0
    if strategies_file and os.path.exists(strategies_file):
        data = json.load(open(strategies_file))
        for item in data:
            name = item.get("name")
            for proto in ("tcp", "udp"):
                a = item.get(proto)
                if not a:
                    continue
                try:
                    conn.execute(
                        "INSERT INTO strategies(name, proto, args, source, trusted, engine, created_at) "
                        "VALUES (?,?,?,'standard',0,'zapret',?)",
                        (name, proto, a, now()),
                    )
                    seeded += 1
                except sqlite3.IntegrityError:
                    pass  # уже есть — не перезаписываем (idempotent init)
        conn.commit()
    print(f"ok: schema ready, {seeded} standard strategy rows seeded")


def cmd_whitelisted(args):
    domain = args[0].lower().strip().rstrip(".")
    conn = db()
    rows = conn.execute("SELECT pattern, kind FROM whitelist").fetchall()
    for pattern, kind in rows:
        pattern = pattern.lower().lstrip(".")  # паттерны хранятся БЕЗ ведущей точки
        if kind == "exact" and domain == pattern:
            print("1")
            return
        if kind == "suffix" and (domain == pattern or domain.endswith("." + pattern)):
            print("1")
            return
    print("0")


def cmd_whitelist_list(args):
    conn = db()
    for row in conn.execute("SELECT id, pattern, kind, COALESCE(note,''), source, added_at FROM whitelist ORDER BY id"):
        print("\t".join(str(x) for x in row))


def cmd_whitelist_add(args):
    pattern, kind = args[0].lower().lstrip("."), args[1]
    note = args[2] if len(args) > 2 else ""
    conn = db()
    conn.execute(
        "INSERT OR IGNORE INTO whitelist(pattern, kind, note, source, added_at) VALUES (?,?,?, 'manual', ?)",
        (pattern, kind, note, time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())),
    )
    conn.commit()
    print("ok")


def cmd_whitelist_remove(args):
    conn = db()
    conn.execute("DELETE FROM whitelist WHERE id=?", (args[0],))
    conn.commit()
    print("ok")


def strategy_utility(conn, sid, base_score):
    """utility (T-are, урезанный вариант без VPS-cost) — score, скорректированный
    реальными данными из history: success_rate (только если проб >=3, иначе одна
    случайная неудача/удача не должна перевешивать статистику) + штраф за среднюю
    задержку успешных проб. НЕ трогает score — тот отдельно живёт для decay,
    это отдельный, вычисляемый на лету критерий сортировки."""
    rows = conn.execute("SELECT result, latency_ms FROM history WHERE strategy_id=?", (sid,)).fetchall()
    total = len(rows)
    u = base_score
    if total >= 3:
        succ = sum(1 for r, _ in rows if r == "success")
        u += (succ / total) * 20 - 10  # 100% успех -> +10, 50% -> 0, 0% -> -10
    lat = [l for r, l in rows if r == "success" and l is not None and l > 0]
    if lat:
        u -= (sum(lat) / len(lat)) / 100.0  # 500мс -> -5, 2000мс -> -20
    return u


def cmd_strategies_list(args):
    tier = None
    proto = None
    engine = None
    if "--tier" in args:
        tier = args[args.index("--tier") + 1]
    if "--proto" in args:
        proto = args[args.index("--proto") + 1]
    if "--engine" in args:
        engine = args[args.index("--engine") + 1]
    q = ("SELECT id, name, proto, args, source, trusted, success_count, engine, score, confidence "
         "FROM strategies WHERE 1=1")
    params = []
    if tier:
        q += " AND source=?"
        params.append(tier)
    if proto:
        q += " AND proto=?"
        params.append(proto)
    if engine:
        q += " AND engine=?"
        params.append(engine)
    conn = db()
    rows = conn.execute(q, params).fetchall()
    # стандартные — в исходном порядке (id, не трогаем — курируемый список flowseal);
    # custom (сюда попадают ВСЕ ciadpi-стратегии) — доверенные первыми, дальше по
    # utility (score + success_rate/latency из history, не голый score). Третий
    # ключ — random(): без него непроверенные стратегии (все с одинаковым utility,
    # т.к. history пуста) сортировались бы стабильно по id — при обрезке через
    # `head -N` в solve.sh (CIADPI_MAX_TRY) одни и те же первые N пробовались бы
    # ВСЕГДА, а остальные не попробовались бы никогда. Рандомизация каждого вызова
    # гарантирует, что со временем перебор доберётся до всех кандидатов.
    standard = [r for r in rows if r[4] == "standard"]
    custom = [r for r in rows if r[4] == "custom"]
    standard.sort(key=lambda r: r[0])
    custom.sort(key=lambda r: (-r[5], -strategy_utility(conn, r[0], r[8]), random.random()))
    for row in standard + custom:
        print("\t".join(str(x) for x in row))


def cmd_strategies_export(args):
    """strategies-export [--engine E] [--tier standard|custom] > file.json — выгрузка
    ПОРТАТИВНОГО описания стратегий (name/proto/args/source/trusted/engine) для
    распространения на другой шлюз (например через GMP OTA). Сознательно БЕЗ
    id/score/confidence/success_count/fail_count/last_result_at/created_at —
    это локальное накопленное состояние конкретного шлюза, не часть "рецепта"
    стратегии и не должно переноситься/перезаписываться при импорте."""
    engine = None
    tier = None
    if "--engine" in args:
        engine = args[args.index("--engine") + 1]
    if "--tier" in args:
        tier = args[args.index("--tier") + 1]
    q = "SELECT name, proto, args, source, trusted, engine FROM strategies WHERE 1=1"
    params = []
    if engine:
        q += " AND engine=?"
        params.append(engine)
    if tier:
        q += " AND source=?"
        params.append(tier)
    q += " ORDER BY engine, source, id"
    conn = db()
    rows = conn.execute(q, params).fetchall()
    out = [
        {"name": r[0], "proto": r[1], "args": r[2], "source": r[3], "trusted": bool(r[4]), "engine": r[5]}
        for r in rows
    ]
    print(json.dumps(out, ensure_ascii=False, indent=2))


def cmd_strategies_import(args):
    """strategies-import FILE.json — идемпотентный импорт (upsert по естественному
    ключу engine+proto+args — это и есть "тот же рецепт десинхронизации", id
    локален и не переносится). Уже существующая стратегия НЕ трогается (её
    накопленные score/confidence/history — заслуженная локальная статистика
    этого шлюза, импорт её не должен обнулять). Печатает добавлено/пропущено."""
    path = args[0]
    with open(path, "r", encoding="utf-8") as f:
        items = json.load(f)
    conn = db()
    added = 0
    skipped = 0
    for it in items:
        engine = it.get("engine", "zapret")
        proto = it["proto"]
        strat_args = it["args"]
        existing = conn.execute(
            "SELECT id FROM strategies WHERE engine=? AND proto=? AND args=?",
            (engine, proto, strat_args),
        ).fetchone()
        if existing:
            skipped += 1
            continue
        conn.execute(
            "INSERT INTO strategies(name, proto, args, source, trusted, engine, created_at) VALUES (?,?,?,?,?,?,?)",
            (it.get("name", "imported"), proto, strat_args, it.get("source", "custom"), int(it.get("trusted", False)), engine, now()),
        )
        added += 1
    conn.commit()
    print(f"ok: добавлено={added} пропущено(уже есть)={skipped}")


def cmd_strategies_explore(args):
    """strategies-explore --proto P --engine E [-n N] — N стратегий, ДОЛЬШЕ ВСЕГО
    не тестировавшихся (по MAX(tested_at) из history; никогда не тестированные —
    первыми, как самые приоритетные). НЕ то же самое, что "низкий score":
    новая стратегия стартует с score=100 (максимум) и выглядела бы "лучшей" при
    сортировке по score, а старая, давно и постоянно проваливающаяся — с низким
    (угасшим) score, но её и так продолжают трогать. score отражает УСПЕШНОСТЬ,
    а не ДАВНОСТЬ проверки — для гарантии, что каждая стратегия рано или поздно
    попробуется, нужен отдельный критерий (T-explore)."""
    proto = None
    engine = None
    n = 10
    if "--proto" in args:
        proto = args[args.index("--proto") + 1]
    if "--engine" in args:
        engine = args[args.index("--engine") + 1]
    if "-n" in args:
        n = int(args[args.index("-n") + 1])
    q = ("SELECT s.id, s.name, s.proto, s.args, s.source, s.trusted, s.success_count, s.engine, s.score, s.confidence, "
         "(SELECT MAX(tested_at) FROM history h WHERE h.strategy_id = s.id) AS last_tested "
         "FROM strategies s WHERE s.source='custom'")
    params = []
    if proto:
        q += " AND s.proto=?"
        params.append(proto)
    if engine:
        q += " AND s.engine=?"
        params.append(engine)
    conn = db()
    rows = conn.execute(q, params).fetchall()
    rows.sort(key=lambda r: (r[10] is not None, r[10] or ""))
    for row in rows[:n]:
        print("\t".join(str(x) for x in row[:10]))


def cmd_strategy_add(args):
    name, proto, strategy_args = args[0], args[1], args[2]
    engine = "zapret"
    if "--engine" in args:
        engine = args[args.index("--engine") + 1]
    if engine not in ENGINES:
        print(f"bad engine: {engine} (ожидается одно из {ENGINES})", file=sys.stderr)
        sys.exit(2)
    conn = db()
    conn.execute(
        "INSERT INTO strategies(name, proto, args, source, trusted, engine, created_at) "
        "VALUES (?,?,?,'custom',0,?,?)",
        (name, proto, strategy_args, engine, now()),
    )
    conn.commit()
    print("ok")


def cmd_strategy_mark_success(args):
    sid = args[0]
    conn = db()
    conn.execute(
        "UPDATE strategies SET trusted=1, success_count=success_count+1, score=score+10, last_result_at=? WHERE id=?",
        (now(), sid),
    )
    conn.commit()
    print("ok")


def cmd_strategy_find(args):
    proto, engine, strat_args = args[0], args[1], args[2]
    conn = db()
    row = conn.execute(
        "SELECT id FROM strategies WHERE proto=? AND engine=? AND args=?",
        (proto, engine, strat_args),
    ).fetchone()
    print(row[0] if row else "")


def cmd_history_add(args):
    sid, domain, result = args[0], args[1], args[2]
    latency = args[3] if len(args) > 3 and args[3] else None
    conn = db()
    row = conn.execute("SELECT engine FROM strategies WHERE id=?", (sid,)).fetchone()
    engine = row[0] if row else None
    conn.execute(
        "INSERT INTO history(strategy_id, domain, engine, result, latency_ms, tested_at) VALUES (?,?,?,?,?,?)",
        (sid, domain, engine, result, latency, now()),
    )
    conn.commit()
    print("ok")


def cmd_service_touch(args):
    """service-touch DOMAIN STRATEGY_ID — вызывается ПОСЛЕ подтверждённого успеха
    (домен уже пробивается, полный перебор не понадобился/понадобился и нашёл
    рабочую стратегию). НЕ вызывается для VPS/DIRECT — те всегда должны
    перепроверяться каждую ночь (мы хотим узнать, как только для них появится
    обход), гистерезис имеет смысл только для "уже работает и работает давно".
    confidence считается по streak подряд успешных проб этого домена в history
    (последние 20 записей): 0/30/70/100 — соответствующий next_reeval_at
    отодвигается на 0/1/3/7 суток (T-are, confidence-гистерезис)."""
    domain, sid = args[0], args[1]
    conn = db()
    rows = conn.execute(
        "SELECT result FROM history WHERE domain=? ORDER BY tested_at DESC LIMIT 20", (domain,)
    ).fetchall()
    streak = 0
    for (result,) in rows:
        if result == "success":
            streak += 1
        else:
            break
    if streak >= 10:
        confidence, interval_days = 100, 7
    elif streak >= 5:
        confidence, interval_days = 70, 3
    elif streak >= 1:
        confidence, interval_days = 30, 1
    else:
        confidence, interval_days = 0, 0
    ts = time.time()
    next_reeval = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(ts + interval_days * 86400))
    conn.execute(
        "INSERT INTO services(group_key, domains, current_strategy_id, confidence, last_reeval_at, next_reeval_at) "
        "VALUES (?,?,?,?,?,?) "
        "ON CONFLICT(group_key) DO UPDATE SET current_strategy_id=excluded.current_strategy_id, "
        "confidence=excluded.confidence, last_reeval_at=excluded.last_reeval_at, next_reeval_at=excluded.next_reeval_at",
        (domain, domain, sid, confidence, now(), next_reeval),
    )
    conn.commit()
    print(f"ok: confidence={confidence} next_reeval={next_reeval}")


def cmd_vps_touch(args):
    """vps-touch DOMAIN success|fail — гистерезис для доменов, работающих через
    VPS-fallback (T-vps-hysteresis, 2026-08-04). service-touch для них никогда
    не вызывался (см. его докстринг) — brain-nightly.sh безусловно гонял ВСЕ
    vps[]-домены каждую ночь навсегда, без пруна (только 30-дневная чистка
    полностью неработающих no_bypass). При росте числа таких доменов (например
    CDN плодит новые случайные UUID-поддомены) это давало неограниченно
    растущую ночную очередь. Здесь свой, отдельный от service-touch streak
    (колонка vps_streak, не завязан на history — тот таблица про попытки
    ZAPRET/CIADPI-стратегий, для VPS семантически не то). Максимальный
    интервал подняли с 3 до 7 дней (2026-08-14, по запросу пользователя —
    ночная переоценка занимала ~12ч и продолжала расти; хронически-VPS
    домены вроде googlevideo.com троттлятся, а не блокируются, повторная
    проверка каждые несколько дней всё равно ничего не изменит)."""
    domain, result = args[0], args[1]
    conn = db()
    row = conn.execute("SELECT vps_streak FROM services WHERE group_key=?", (domain,)).fetchone()
    streak = (row[0] if row else 0) + 1 if result == "success" else 0
    if streak >= 10:
        confidence, interval_days = 100, 7
    elif streak >= 5:
        confidence, interval_days = 70, 1
    elif streak >= 1:
        confidence, interval_days = 30, 0.5
    else:
        confidence, interval_days = 0, 0
    ts = time.time()
    next_reeval = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(ts + interval_days * 86400))
    conn.execute(
        "INSERT INTO services(group_key, domains, current_strategy_id, confidence, vps_streak, last_reeval_at, next_reeval_at) "
        "VALUES (?,?,NULL,?,?,?,?) "
        "ON CONFLICT(group_key) DO UPDATE SET current_strategy_id=NULL, confidence=excluded.confidence, "
        "vps_streak=excluded.vps_streak, last_reeval_at=excluded.last_reeval_at, next_reeval_at=excluded.next_reeval_at",
        (domain, domain, confidence, streak, now(), next_reeval),
    )
    conn.commit()
    print(f"ok: confidence={confidence} streak={streak} next_reeval={next_reeval}")


def cmd_service_skip_list(args):
    """service-skip-list — домены, у которых next_reeval_at ещё не наступил
    (можно НЕ гонять сегодня даже дешёвый быстрый тест). Список ИСКЛЮЧЕНИЙ,
    не список "кого проверять" — домен, ещё ни разу не тронутый service-touch,
    в этот список не попадёт и будет проверен как обычно (безопасный дефолт)."""
    conn = db()
    rows = conn.execute(
        "SELECT group_key FROM services WHERE next_reeval_at IS NOT NULL AND next_reeval_at > ?", (now(),)
    ).fetchall()
    for (d,) in rows:
        print(d)


def cmd_services_list(args):
    conn = db()
    rows = conn.execute(
        "SELECT s.group_key, s.current_strategy_id, st.engine, st.name, s.confidence, s.last_reeval_at, s.next_reeval_at "
        "FROM services s LEFT JOIN strategies st ON st.id = s.current_strategy_id "
        "ORDER BY s.next_reeval_at DESC"
    ).fetchall()
    for row in rows:
        print("\t".join("" if x is None else str(x) for x in row))


def cmd_strategies_decay(args):
    # Угасание score (ТЗ Adaptive Routing Engine): без него давно неподтверждённая,
    # но когда-то успешная стратегия навсегда сидит выше свежепроверенной — мозг
    # никогда не "забывает". Раз в сутки (brain-nightly.sh) домножаем ВСЕ score на
    # factor<1 — успех/провал (+10/-5) по-прежнему считаются от ТЕКУЩЕГО (уже
    # угасшего) значения, так что реально используемые стратегии сами держат
    # score высоким, а неиспользуемые тихо сползают к нулю.
    factor = 0.995
    if "--factor" in args:
        factor = float(args[args.index("--factor") + 1])
    conn = db()
    conn.execute("UPDATE strategies SET score = score * ?", (factor,))
    conn.commit()
    n = conn.execute("SELECT COUNT(*) FROM strategies").fetchone()[0]
    print(f"ok: decay x{factor} применён к {n} стратегиям")


def cmd_strategy_remove(args):
    sid = args[0]
    conn = db()
    conn.execute("DELETE FROM strategies WHERE id=? AND source='custom'", (sid,))
    conn.commit()
    print("ok")


def cmd_strategy_mark_fail(args):
    sid = args[0]
    conn = db()
    conn.execute(
        "UPDATE strategies SET fail_count=fail_count+1, score=MAX(score-5, 0), last_result_at=? WHERE id=?",
        (now(), sid),
    )
    conn.commit()
    print("ok")


COMMANDS = {
    "init": cmd_init,
    "whitelisted": cmd_whitelisted,
    "whitelist-list": cmd_whitelist_list,
    "whitelist-add": cmd_whitelist_add,
    "whitelist-remove": cmd_whitelist_remove,
    "strategies-list": cmd_strategies_list,
    "strategies-export": cmd_strategies_export,
    "strategies-import": cmd_strategies_import,
    "strategy-add": cmd_strategy_add,
    "strategy-remove": cmd_strategy_remove,
    "strategies-decay": cmd_strategies_decay,
    "service-touch": cmd_service_touch,
    "vps-touch": cmd_vps_touch,
    "service-skip-list": cmd_service_skip_list,
    "services-list": cmd_services_list,
    "strategies-explore": cmd_strategies_explore,
    "strategy-find": cmd_strategy_find,
    "history-add": cmd_history_add,
    "strategy-mark-success": cmd_strategy_mark_success,
    "strategy-mark-fail": cmd_strategy_mark_fail,
}


def main():
    if len(sys.argv) < 2 or sys.argv[1] not in COMMANDS:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    os.makedirs(os.path.dirname(DB_PATH), exist_ok=True)
    COMMANDS[sys.argv[1]](sys.argv[2:])


if __name__ == "__main__":
    main()
