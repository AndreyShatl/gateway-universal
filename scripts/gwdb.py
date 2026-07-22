#!/usr/bin/env python3
"""gwdb.py — общий доступ к /etc/gateway/gateway.db (whitelist + presets, T48).

Единственная точка схемы для bash-стороны (solve.sh/brain-worker.sh/brain-apply.sh).
Go-сторона (gateway-ui) открывает тот же файл через modernc.org/sqlite — схема
должна оставаться идентичной, менять оба места вместе.

Использование (каждая подкоманда печатает результат в stdout, TSV где не сказано иное):
  gwdb.py init [--strategies-file PATH]         # создать таблицы + засеять стандартные пресеты
  gwdb.py whitelisted DOMAIN                     # "1" | "0"
  gwdb.py whitelist-list
  gwdb.py whitelist-add PATTERN KIND [NOTE]      # KIND = suffix|exact
  gwdb.py whitelist-remove ID
  gwdb.py presets-list [--tier standard|custom] [--proto tcp|udp]
                                                  # id\tname\tproto\targs\tsource\ttrusted\tsuccess_count
  gwdb.py preset-add NAME PROTO ARGS             # добавить custom-пресет (untrusted)
  gwdb.py preset-mark-success ID                 # trusted=1, success_count+=1
  gwdb.py preset-mark-fail ID                    # fail_count+=1
"""
import json
import os
import sqlite3
import sys
import time

DB_PATH = os.environ.get("GWDB_PATH", "/etc/gateway/gateway.db")

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

CREATE TABLE IF NOT EXISTS presets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    proto TEXT NOT NULL CHECK(proto IN ('tcp','udp')),
    args TEXT NOT NULL,
    source TEXT NOT NULL CHECK(source IN ('standard','custom')),
    trusted INTEGER NOT NULL DEFAULT 0,
    success_count INTEGER NOT NULL DEFAULT 0,
    fail_count INTEGER NOT NULL DEFAULT 0,
    last_result_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_presets_name_proto ON presets(name, proto);
"""


def db():
    conn = sqlite3.connect(DB_PATH, timeout=10)
    conn.execute("PRAGMA journal_mode=WAL;")
    conn.execute("PRAGMA busy_timeout=5000;")
    return conn


def cmd_init(args):
    strategies_file = None
    if "--strategies-file" in args:
        strategies_file = args[args.index("--strategies-file") + 1]
    conn = db()
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
                        "INSERT INTO presets(name, proto, args, source, trusted) VALUES (?,?,?,'standard',0)",
                        (name, proto, a),
                    )
                    seeded += 1
                except sqlite3.IntegrityError:
                    pass  # уже есть — не перезаписываем (idempotent init)
        conn.commit()
    print(f"ok: schema ready, {seeded} standard preset rows seeded")


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


def cmd_presets_list(args):
    tier = None
    proto = None
    if "--tier" in args:
        tier = args[args.index("--tier") + 1]
    if "--proto" in args:
        proto = args[args.index("--proto") + 1]
    q = "SELECT id, name, proto, args, source, trusted, success_count FROM presets WHERE 1=1"
    params = []
    if tier:
        q += " AND source=?"
        params.append(tier)
    if proto:
        q += " AND proto=?"
        params.append(proto)
    # стандартные — в исходном порядке (id); custom — доверенные и успешные первыми
    q += " ORDER BY (source='custom'), (source='standard') , CASE WHEN source='custom' THEN -trusted END, CASE WHEN source='custom' THEN -success_count END, id"
    conn = db()
    for row in conn.execute(q, params):
        print("\t".join(str(x) for x in row))


def cmd_preset_add(args):
    name, proto, preset_args = args[0], args[1], args[2]
    conn = db()
    conn.execute(
        "INSERT INTO presets(name, proto, args, source, trusted) VALUES (?,?,?,'custom',0)",
        (name, proto, preset_args),
    )
    conn.commit()
    print("ok")


def cmd_preset_mark_success(args):
    pid = args[0]
    conn = db()
    conn.execute(
        "UPDATE presets SET trusted=1, success_count=success_count+1, last_result_at=? WHERE id=?",
        (time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), pid),
    )
    conn.commit()
    print("ok")


def cmd_preset_mark_fail(args):
    pid = args[0]
    conn = db()
    conn.execute(
        "UPDATE presets SET fail_count=fail_count+1, last_result_at=? WHERE id=?",
        (time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), pid),
    )
    conn.commit()
    print("ok")


COMMANDS = {
    "init": cmd_init,
    "whitelisted": cmd_whitelisted,
    "whitelist-list": cmd_whitelist_list,
    "whitelist-add": cmd_whitelist_add,
    "whitelist-remove": cmd_whitelist_remove,
    "presets-list": cmd_presets_list,
    "preset-add": cmd_preset_add,
    "preset-mark-success": cmd_preset_mark_success,
    "preset-mark-fail": cmd_preset_mark_fail,
}


def main():
    if len(sys.argv) < 2 or sys.argv[1] not in COMMANDS:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    os.makedirs(os.path.dirname(DB_PATH), exist_ok=True)
    COMMANDS[sys.argv[1]](sys.argv[2:])


if __name__ == "__main__":
    main()
