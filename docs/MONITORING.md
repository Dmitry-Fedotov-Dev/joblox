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
| `openline_http_requests_total{route, code}` | Запросы по шаблону адреса и классу ответа (2xx, 4xx, 5xx) |
| `openline_http_request_duration_seconds{route}` | Время ответа, гистограмма |
| `openline_online_visitors` | Онлайн |
| `openline_antifraud_decisions_total{action}` | Решения антифрода: allow, review, block |
| `openline_order_transitions_total{to}` | Заказы, вошедшие в статус |
| `go_*`, `process_*` | Среда Go и процесс |

Стек рядом с магазином:

```bash
docker compose -f monitoring/docker-compose.yml --profile linux-host up -d
```

Grafana на http://127.0.0.1:3003, дашборд OpenLine Shop откроется сам. Порты (Prometheus 9093, Grafana 3003,
node-exporter 9102) выбраны так, чтобы стек жил на одном сервере рядом со стеками kontakt и xk6-sip.

Дашборд правится в генераторе [`monitoring/grafana/gen_dashboard.py`](../monitoring/grafana/gen_dashboard.py),
а не в JSON руками. CI сверяет JSON с генератором:

```bash
python3 monitoring/grafana/gen_dashboard.py monitoring/grafana/dashboards/openline.json
```

## Тревоги

[`monitoring/prometheus/rules/openline.yml`](../monitoring/prometheus/rules/openline.yml), с тестами на
синтетических рядах ([`tests/openline_test.yml`](../monitoring/prometheus/tests/openline_test.yml)):

| Тревога | Условие |
|---|---|
| OpenlineDown | `/metrics` не отвечает 2 минуты |
| OpenlineErrors | Больше 2% ответов 5xx за 5 минут |
| OpenlineSlow | p95 главной страницы выше 0,5 с 10 минут |
| OpenlineFraudWave | Больше 30 отказов антифрода за 15 минут |

```bash
cd monitoring/prometheus && promtool test rules tests/openline_test.yml
```

## Отчёт о нагрузке картинкой

`monitoring/report.sh` сохраняет весь дашборд за окно прогона в один PNG. CI прикладывает его к нагрузочному
тесту (артефакт load-report).
