#!/usr/bin/env python3
"""autoroute-collapse.py — схлопывает случайные UUID-поддомены с общим хвостом
в autoroute.json до одной записи-представителя (T-collapse-uuid, 2026-08-04).

Зачем: некоторые CDN (замечено на *.fbcdn.net) генерируют per-сессионные
поддомены вида "<uuid>-netseer-ipaddr-assoc.xz.fbcdn.net" — кардинальность
почти неограничена (новый UUID почти на каждый визит). Раньше каждый такой
поддомен становился ОТДЕЛЬНОЙ записью в autoroute.json навсегда (для доменов,
успешно работающих через VPS, прунинга не было вообще, только 30-дневная
чистка полностью неработающих no_bypass) — список и, соответственно, ночная
очередь на переоценку (brain-nightly.sh) росли без предела.

Идеальное решение — не плодить дубли уже на входе (в gateway-detector, который
и добавляет записи через applier.Apply) — соответствующий код есть в
detector/applier/applier.go (collapseSuffix), но сам detector сейчас собран
через eBPF-биндинги, для пересборки которых на шлюзе не хватает
сгенерированных bpf2go-файлов (см. историю чата T-collapse-uuid) — трогать
боевой бинарь детектора блокировок без возможности безопасно проверить сборку
рискованно. Поэтому здесь — тот же схлопывающий алгоритм, но применяется
ПОСТФАКТУМ к уже накопленному autoroute.json, штатно вызывается из
brain-nightly.sh перед сборкой списка vps[] на переоценку. Самоисцеляющийся:
раз в сутки подчищает то, что успел добавить старый (не умеющий схлопывать)
detector, независимо от того, будет ли он когда-нибудь пересобран.

Использование: autoroute-collapse.py [--file /etc/gateway/autoroute.json]
Печатает: "схлопнуто: N записей -> M групп" или "нечего схлопывать".
"""
import argparse
import fcntl
import json
import os
import re
import sys

UUID_PREFIX_RE = re.compile(
    r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-(.+)$", re.IGNORECASE
)


def collapse_suffix(addr):
    m = UUID_PREFIX_RE.match(addr)
    return m.group(1) if m else None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--file", default="/etc/gateway/autoroute.json")
    args = ap.parse_args()

    if not os.path.exists(args.file):
        print("нет файла, нечего схлопывать")
        return

    with open(args.file, "r+") as f:
        fcntl.flock(f, fcntl.LOCK_EX)
        try:
            data = json.load(f)
            entries = data.get("entries", [])

            by_suffix = {}
            kept = []
            removed = 0
            for e in entries:
                addr = e.get("addr") if isinstance(e, dict) else e
                suffix = collapse_suffix(addr or "")
                if suffix is None:
                    kept.append(e)
                    continue
                if suffix in by_suffix:
                    # уже есть представитель этого хвоста — эту запись выкидываем
                    removed += 1
                    continue
                by_suffix[suffix] = addr
                if isinstance(e, dict):
                    e["collapsed_suffix"] = suffix
                kept.append(e)

            if removed == 0:
                print("нечего схлопывать")
                return

            data["entries"] = kept
            f.seek(0)
            json.dump(data, f, ensure_ascii=False, indent=1)
            f.truncate()
            print(f"схлопнуто: удалено {removed} дублирующих UUID-поддоменов, осталось {len(kept)} записей")
        finally:
            fcntl.flock(f, fcntl.LOCK_UN)


if __name__ == "__main__":
    main()
