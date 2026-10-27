# Тесты

Перед коммитом: `gofmt -l .` пусто, `go vet ./...` чисто, `go test -race -count=2 ./...` зелёный.
CI гоняет то же и ещё сценарии k6 против живого сервера и нагрузку.

## Модульные и функциональные на Go

| Пакет | Что проверяется |
|---|---|
| `internal/profile` | Все готовые профили загружаются; ошибки профиля ловятся (неизвестный ключ, недостижимый статус, неверный исполнитель, нет поля email) |
| `internal/flow` | Какие переходы открыты клиенту и оператору, условия, таймаут |
| `internal/order` | Жизненный цикл на настоящей SQLite: заказ, код, реквизиты, переходы, оплата; цена по запросу; лимиты; чёрный список; оптимистическая блокировка; закрытие по сроку; лимиты кода |
| `internal/antifraud` | Каждое правило отказа и проверки |
| `internal/store` | Сохранение и чтение, повторное открытие базы, журнал нельзя править |
| `internal/bot` | Карточки и кнопки на поддельном сервере Telegram, чужие пользователи, `/price`, пауза |
| `internal/web` | HTTP целиком: витрина, CSRF, путь клиента до реквизитов, ловушка для ботов, быстрая форма, дашборд только на своём адресе |
| `internal/metrics` | Онлайн, запросы в секунду, процентили, `/metrics` |

## Сценарии k6

Против запущенного сервера в режиме `dev` (коды читаются из `/dev/outbox`):

```bash
OPENLINE_ENV=dev OPENLINE_PROFILE=profiles/bakery.yaml OPENLINE_REQUISITES=deploy/requisites.example.yaml ./bin/openline serve &
k6 run -e BASE_URL=http://127.0.0.1:8080 k6/functional/checkout.js
k6 run -e BASE_URL=http://127.0.0.1:8080 k6/functional/antifraud.js
```

Каждый шаг сценария отдельная проверка. Первый проваленный шаг останавливает сценарий, поэтому в JUnit-отчёте
(`-e JUNIT=reports/checkout.xml`) виден именно он.

## Нагрузка

```bash
OPENLINE_TRUSTED_PROXIES=127.0.0.1 OPENLINE_ENV=dev OPENLINE_PROFILE=profiles/bakery.yaml \
  OPENLINE_REQUISITES=deploy/requisites.example.yaml ./bin/openline serve &
k6 run -e BASE_URL=http://127.0.0.1:8080 -e RATE=60 -e DURATION=1m --summary-export reports/summary.json k6/load.js
python3 scripts/load_summary.py reports/summary.json "60 посетителей в секунду"
```

Посетители заходят на витрину, часть смотрит страницы заказов, каждый десятый оформляет заказ. Каждый
виртуальный посетитель приходит со своего адреса в `X-Forwarded-For`, поэтому серверу нужен
`OPENLINE_TRUSTED_PROXIES=127.0.0.1`. Пороги: ошибок меньше 1%, p95 витрины меньше 300 мс, заказа меньше 800 мс.

В CI нагрузка идёт с Prometheus и Grafana, а весь дашборд за прогон сохраняется картинкой.
