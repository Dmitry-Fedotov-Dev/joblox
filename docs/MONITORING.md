# Мониторинг

## Встроенный дашборд

http://127.0.0.1:9180/admin на сервере (снаружи через `ssh -L`, см. [DEPLOY.md](DEPLOY.md)). Работает без
Prometheus и Grafana, обновляется каждые 2 секунды.

| Плитка | Откуда |
|---|---|
| Онлайн | Посетители (случайная кука), которых сервер видел за 2 минуты. Открытая страница отмечается раз в 30 секунд |
| Запросов в секунду | За 10 секунд, минуту и 5 минут, плюс график за 2 минуты |
| Время ответа | 95-й процентиль и медиана по последним 2048 запросам |
| Ошибки 5xx | За 5 минут |
| Процессор, память | `/proc` на Linux: загрузка всех ядер, load average, занятая память |
| Процесс | Память, которую держит процесс, число горутин, время работы |
| Заказы за сутки | Новые, открытые, на проверке, оплаченные и выручка |

## Prometheus и Grafana

Метрики на `http://127.0.0.1:9180/metrics`:

| Метрика | Что считает |
|---|---|
| `joblox_http_requests_total{route, code}` | Запросы по шаблону адреса и классу ответа (2xx, 4xx, 5xx) |
| `joblox_http_request_duration_seconds{route}` | Время ответа, гистограмма |
| `joblox_online_visitors` | Онлайн |
| `joblox_antifraud_decisions_total{action}` | Решения антифрода: allow, review, block |
| `joblox_order_transitions_total{to}` | Заказы, вошедшие в статус |
| `go_*`, `process_*` | Среда Go и процесс |

Стек рядом с магазином:

```bash
docker compose -f monitoring/docker-compose.yml --profile linux-host up -d
```

Grafana на http://127.0.0.1:3003, дашборд Joblox откроется сам. Порты (Prometheus 9093, Grafana 3003,
node-exporter 9102) выбраны так, чтобы стек жил на одном сервере рядом со стеками kontakt и xk6-sip.

Дашборд правится в генераторе [`monitoring/grafana/gen_dashboard.py`](../monitoring/grafana/gen_dashboard.py),
а не в JSON руками. CI сверяет JSON с генератором:

```bash
python3 monitoring/grafana/gen_dashboard.py monitoring/grafana/dashboards/joblox.json
```

## Тревоги

[`monitoring/prometheus/rules/joblox.yml`](../monitoring/prometheus/rules/joblox.yml), с тестами на
синтетических рядах ([`tests/joblox_test.yml`](../monitoring/prometheus/tests/joblox_test.yml)):

| Тревога | Условие |
|---|---|
| JobloxDown | `/metrics` не отвечает 2 минуты |
| JobloxErrors | Больше 2% ответов 5xx за 5 минут |
| JobloxSlow | p95 главной страницы выше 0,5 с 10 минут |
| JobloxFraudWave | Больше 30 отказов антифрода за 15 минут |

```bash
cd monitoring/prometheus && promtool test rules tests/joblox_test.yml
```

## Отчёт о нагрузке картинкой

`monitoring/report.sh` сохраняет весь дашборд за окно прогона в один PNG. CI прикладывает его к нагрузочному
тесту (артефакт load-report).
