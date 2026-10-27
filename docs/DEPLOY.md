# Развёртывание

## Что понадобится

| | Цена | Зачем |
|---|---|---|
| VPS: 1 ядро, 1 ГБ памяти, Linux | около 5 долларов в месяц | Сервер. Программе хватает нескольких десятков МБ памяти |
| Домен | около 10 долларов в год | Адрес сайта и HTTPS |
| Почта с SMTP | от 0 | Коды подтверждения и статусы |
| Telegram-бот | бесплатно | Операторы |

## С Docker (рекомендуется)

1. Направьте домен на сервер: запись A на IP сервера у регистратора домена.
2. Установите Docker на сервер (`curl -fsSL https://get.docker.com | sh`).
3. Скопируйте репозиторий на сервер и заполните настройки:

```bash
git clone https://github.com/Dmitry-Fedotov-Dev/openline-shop && cd openline-shop
cp deploy/.env.example deploy/.env && nano deploy/.env
cp deploy/requisites.example.yaml deploy/requisites.yaml && nano deploy/requisites.yaml && chmod 600 deploy/requisites.yaml
```

4. Выберите профиль: `PROFILE_FILE` в `deploy/.env` (готовый из `profiles/` или свой).
5. Запустите:

```bash
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml logs -f shop
```

Caddy сам получит сертификат для `DOMAIN`. Бот пришлёт сообщение о запуске с отпечатком реквизитов.

Если оплата переводом не нужна (только при получении), уберите строку с `requisites.yaml` из
`deploy/docker-compose.yml` и `transfer` из `payment.methods` профиля.

## Без Docker

Соберите бинарник (`CGO_ENABLED=0 go build -o bin/openline ./cmd/openline`), скопируйте на сервер и установите
сервис по комментариям в [`deploy/openline.service`](../deploy/openline.service). HTTPS даст Caddy на хосте:
[`deploy/Caddyfile.host`](../deploy/Caddyfile.host).

## Дашборд на сервере

Дашборд слушает только 127.0.0.1. С вашего компьютера:

```bash
ssh -N -L 9180:127.0.0.1:9180 root@сервер
```

Затем откройте http://localhost:9180/admin.

## Резервные копии

Все данные в одном файле SQLite (в Docker: том `openline_shop-data`). Безопасная копия без остановки:

```bash
docker run --rm -v openline_shop-data:/data -v "$PWD":/backup alpine \
  sh -c 'apk add -q sqlite && sqlite3 /data/openline.db ".backup /backup/openline-$(date +%F).db"'
```

Храните копии вне сервера. Файл реквизитов и `.env` копируйте отдельно и так же бережно.

## Cloudflare

Программа написана на Go и работает как обычный сервер, поэтому Cloudflare Workers и Pages её не запустят.
Cloudflare можно поставить перед VPS как DNS и прокси (оранжевое облако):

- добавьте диапазоны адресов Cloudflare в `OPENLINE_TRUSTED_PROXIES`, иначе лимиты будут считать всех
  посетителей одним адресом;
- учтите, что в некоторых странах Cloudflare блокируют, и тогда сайт станет недоступен для части клиентов.

Cloudflare Tunnel подходит, если сервер дома и у него нет белого IP.

## Обновление

```bash
git pull && docker compose -f deploy/docker-compose.yml up -d --build
```

База обновляет схему сама при запуске. Перед обновлением сделайте резервную копию.
