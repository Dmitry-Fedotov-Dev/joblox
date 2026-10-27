#!/usr/bin/env python3
# Генератор monitoring/grafana/dashboards/openline.json — дашборд OpenLine Shop.
# Правится здесь, а не в JSON руками; CI (dashboards-sync) сверяет JSON с генератором:
#   python3 monitoring/grafana/gen_dashboard.py monitoring/grafana/dashboards/openline.json
import json, sys

DS = {"type": "prometheus", "uid": "prometheus"}
panels, pid, y = [], [0], [0]


def nid():
    pid[0] += 1
    return pid[0]


def row(title):
    panels.append({"type": "row", "title": title, "collapsed": False, "id": nid(),
                   "gridPos": {"x": 0, "y": y[0], "w": 24, "h": 1}, "panels": []})
    y[0] += 1


def tgt(expr, legend="__auto", i=0):
    return {"refId": chr(65 + i), "datasource": DS, "expr": expr, "legendFormat": legend, "range": True, "instant": False}


def stat(title, desc, expr, x, w=4, unit="none", color="blue", steps=None, dec=0):
    panels.append({"type": "stat", "title": title, "description": desc, "id": nid(), "datasource": DS,
        "gridPos": {"x": x, "y": y[0], "w": w, "h": 4}, "targets": [tgt(expr)],
        "fieldConfig": {"defaults": {"unit": unit, "decimals": dec, "color": {"mode": "thresholds"},
            "thresholds": {"mode": "absolute", "steps": steps or [{"color": color, "value": None}]}}, "overrides": []},
        "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                    "colorMode": "background", "graphMode": "area", "textMode": "value", "justifyMode": "center"}})


def ts(title, desc, exprs, x, w=12, h=8, unit="none", stack=False):
    d = {"unit": unit, "min": 0, "custom": {"lineWidth": 1, "fillOpacity": 15, "showPoints": "never",
         "stacking": {"mode": "normal" if stack else "none", "group": "A"}}}
    panels.append({"type": "timeseries", "title": title, "description": desc, "id": nid(), "datasource": DS,
        "gridPos": {"x": x, "y": y[0], "w": w, "h": h},
        "targets": [tgt(e, l, i) for i, (e, l) in enumerate(exprs)],
        "fieldConfig": {"defaults": d, "overrides": []},
        "options": {"legend": {"displayMode": "table", "placement": "bottom", "calcs": ["lastNotNull", "max"]},
                    "tooltip": {"mode": "multi", "sort": "desc"}}})


W = "$window"
I = 'instance=~"$instance"'
J = f'job="openline",{I}'

row("Сейчас")
stat("Магазин", "Отвечает ли сервер: Prometheus забирает /metrics с адреса администратора.",
     f'min(up{{{J}}}) or vector(0)', 0, steps=[{"color": "red", "value": None}, {"color": "green", "value": 1}])
stat("Онлайн", "Посетители, которых сервер видел за последние 2 минуты (случайная кука, не человек).",
     f'sum(openline_online_visitors{{{I}}}) or vector(0)', 4, color="purple")
stat("Запросов в секунду", "Все HTTP-запросы витрины.",
     f'sum(rate(openline_http_requests_total{{{I}}}[{W}])) or vector(0)', 8, unit="reqps", dec=1)
stat("Главная, p95", "95% запросов главной страницы быстрее этого времени.",
     f'histogram_quantile(0.95, sum by (le) (rate(openline_http_request_duration_seconds_bucket{{route="GET /{{$}}",{I}}}[{W}])))',
     12, unit="s", dec=3, steps=[{"color": "green", "value": None}, {"color": "yellow", "value": 0.3}, {"color": "red", "value": 0.5}])
stat("Ошибки 5xx", "Доля ответов с ошибкой сервера. Больше 2% — тревога OpenlineErrors.",
     f'(sum(rate(openline_http_requests_total{{code="5xx",{I}}}[{W}])) / clamp_min(sum(rate(openline_http_requests_total{{{I}}}[{W}])), 0.001)) or vector(0)',
     16, unit="percentunit", dec=2, steps=[{"color": "green", "value": None}, {"color": "yellow", "value": 0.005}, {"color": "red", "value": 0.02}])
stat("Отклонено за час", "Попытки, которые антифрод не пропустил: боты, чёрный список, лимиты.",
     f'sum(increase(openline_antifraud_decisions_total{{action="block",{I}}}[1h])) or vector(0)', 20, color="orange")
y[0] += 4

row("Трафик")
ts("Запросы по адресам", "Запросы в секунду по шаблону адреса (GET /, POST /order, страница заказа …).",
   [(f'sum by (route) (rate(openline_http_requests_total{{{I}}}[{W}]))', "{{route}}")], 0, unit="reqps", stack=True)
ts("Время ответа", "p50 и p95 по адресам.",
   [(f'histogram_quantile(0.95, sum by (le, route) (rate(openline_http_request_duration_seconds_bucket{{{I}}}[{W}])))', "p95 {{route}}"),
    (f'histogram_quantile(0.5, sum by (le, route) (rate(openline_http_request_duration_seconds_bucket{{{I}}}[{W}])))', "p50 {{route}}", )],
   12, unit="s")
y[0] += 8
ts("Ответы по классам", "2xx, 3xx (переходы после форм), 4xx (ошибки формы, лимиты), 5xx (ошибки сервера).",
   [(f'sum by (code) (rate(openline_http_requests_total{{{I}}}[{W}]))', "{{code}}")], 0, unit="reqps", stack=True)
ts("Онлайн", "Посетители за последние 2 минуты.",
   [(f'sum(openline_online_visitors{{{I}}})', "онлайн")], 12)
y[0] += 8

row("Заказы")
ts("Решения антифрода", "allow — сразу дальше, review — ждёт оператора, block — отклонено без создания заказа.",
   [(f'sum by (action) (increase(openline_antifraud_decisions_total{{{I}}}[{W}]))', "{{action}}")], 0, stack=True)
ts("Переходы по статусам", "Сколько заказов вошло в каждый статус профиля.",
   [(f'sum by (to) (increase(openline_order_transitions_total{{{I}}}[{W}]))', "{{to}}")], 12, stack=True)
y[0] += 8

row("Сервер")
ts("Процессор", "Загрузка процессора сервера (node-exporter) и процесса магазина.",
   [('100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[' + W + '])))', "сервер, %"),
    (f'100 * rate(process_cpu_seconds_total{{{J}}}[{W}])', "магазин, % ядра")], 0, unit="percent")
ts("Память", "Занято на сервере и у процесса магазина.",
   [('node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes', "сервер"),
    (f'process_resident_memory_bytes{{{J}}}', "магазин")], 12, unit="bytes")
y[0] += 8

row("Нагрузочный тест (k6)")
K = 'testid=~"$testid"'
ts("k6: запросы в секунду", "Запросы генератора нагрузки (k6 -o experimental-prometheus-rw).",
   [(f'sum(rate(k6_http_reqs_total{{{K}}}[{W}]))', "запросов в секунду")], 0, unit="reqps")
ts("k6: время ответа p95", "Как его видит генератор нагрузки, по имени запроса.",
   [(f'histogram_quantile(0.95, sum by (le, name) (rate(k6_http_req_duration_seconds{{{K}}}[{W}])))', "{{name}}")], 12, unit="s")
y[0] += 8

dash = {
    "uid": "openline", "title": "OpenLine Shop", "tags": ["openline"], "timezone": "browser",
    "schemaVersion": 39, "version": 1, "editable": True, "refresh": "10s",
    "time": {"from": "now-1h", "to": "now"},
    "templating": {"list": [
        {"name": "instance", "label": "Сервер", "type": "query", "datasource": DS, "query": 'label_values(up{job="openline"}, instance)',
         "includeAll": True, "multi": True, "current": {"text": "All", "value": "$__all"}, "refresh": 2, "allValue": ".*"},
        {"name": "window", "label": "Окно", "type": "custom", "query": "1m,5m,15m", "current": {"text": "1m", "value": "1m"},
         "options": [{"text": v, "value": v, "selected": v == "1m"} for v in ("1m", "5m", "15m")]},
        {"name": "testid", "label": "Прогон k6", "type": "query", "datasource": DS, "query": "label_values(k6_http_reqs_total, testid)",
         "includeAll": True, "current": {"text": "All", "value": "$__all"}, "refresh": 2, "allValue": ".*"},
    ]},
    "panels": panels,
}

out = sys.argv[1] if len(sys.argv) > 1 else "monitoring/grafana/dashboards/openline.json"
with open(out, "w", encoding="utf-8", newline="\n") as f:
    json.dump(dash, f, ensure_ascii=False, indent=2)
    f.write("\n")
