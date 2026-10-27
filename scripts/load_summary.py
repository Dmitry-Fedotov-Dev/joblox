#!/usr/bin/env python3
"""Markdown summary of a k6 load run (--summary-export) for the CI run page.

  scripts/load_summary.py reports/summary.json "60 посетителей в секунду, 1 минута" [note]

Exits 1 when a threshold was crossed, so the step fails together with k6.
"""
import json
import sys


def ms(v):
    return f"{v:.1f} мс" if v < 10 else f"{v:.0f} мс"


def main():
    path, title = sys.argv[1], sys.argv[2]
    note = sys.argv[3] if len(sys.argv) > 3 else ""
    m = json.load(open(path, encoding="utf-8"))["metrics"]
    reqs = m.get("http_reqs", {})
    failed = m.get("http_req_failed", {})
    checks = m.get("checks", {})
    rows = [
        ("Запросов", f"{reqs.get('count', 0):,} ({reqs.get('rate', 0):.1f} в секунду)".replace(",", " "), None),
        ("Ошибочных ответов", f"{failed.get('value', 0) * 100:.2f}%", "http_req_failed"),
        ("Проверки", f"{checks.get('passes', 0)} из {checks.get('passes', 0) + checks.get('fails', 0)}", "checks"),
    ]
    for name, label in (("GET /", "Витрина"), ("POST /order", "Оформление заказа"), ("GET /o/{token}", "Страница заказа")):
        d = m.get(f"http_req_duration{{name:{name}}}", {})
        if d:
            rows.append((f"{label}: p95 / медиана", f"{ms(d.get('p(95)', 0))} / {ms(d.get('med', 0))}", f"http_req_duration{{name:{name}}}"))
    dropped = m.get("dropped_iterations", {}).get("count", 0)
    rows.append(("Пропущенные итерации", str(dropped), None))

    crossed, total = [], 0
    for metric, data in m.items():
        for expr, bad in (data.get("thresholds") or {}).items():
            total += 1
            if bad:  # summary-export: true means the threshold was crossed
                crossed.append(f"{metric} {expr}")
    head = f"### ❌ Нагрузка: пробито {len(crossed)} из {total} порогов" if crossed else f"### ✅ Нагрузка: {total} порогов соблюдены"
    print(head + "\n\n" + title + "\n\n| Метрика | Значение | Порог |\n|---|---|---|")
    for label, value, metric in rows:
        th = (m.get(metric, {}).get("thresholds") or {}) if metric else {}
        mark = ("❌" if any(th.values()) else "✅") if th else ""
        print(f"| {label} | {value} | {mark} |")
    if crossed:
        print("\nПробиты: " + "; ".join(f"`{c}`" for c in crossed))
    if note:
        print("\n" + note)
    return 1 if crossed else 0


if __name__ == "__main__":
    sys.exit(main())
