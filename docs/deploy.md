# Развёртывание и эксплуатация

## Что запускается

`docker-compose.prod.yml` поднимает:

| Сервис | Назначение |
|---|---|
| `postgres` | PostgreSQL 16, порт наружу не публикуется, пароль обязателен, том `pgdata`, медленные запросы (>500 мс) пишутся в лог |
| `migrate` | применяет миграции goose и завершается; бот стартует только после его успеха |
| `bot` | сам бот; образ distroless (≈24 МБ, без shell), пользователь `nonroot`, файловая система только для чтения, без capabilities |
| `backup` | раз в сутки делает `pg_dump` в `./backups`, хранит `BACKUP_KEEP_DAYS` (14) дней |
| `caddy` | только с профилем `webhook`: HTTPS с автоматическими сертификатами |

## Первый запуск

На сервере с Docker Compose v2:

```bash
git clone <репозиторий> bot && cd bot
cp .env.example .env
```

Заполните `.env` (минимум):

```ini
BOT_TOKEN=<токен от @BotFather>
ADMIN_IDS=<ваш id>[,<id коллеги>]
CAMPAIGN_START=2026-09-01
CAMPAIGN_END=2026-09-30
POSTGRES_PASSWORD=<openssl rand -hex 24>     # только hex/буквы/цифры: пароль попадает в URL
```

```bash
chmod 600 .env
docker compose -f docker-compose.prod.yml up -d --build
docker compose -f docker-compose.prod.yml ps          # все сервисы healthy / running
docker compose -f docker-compose.prod.yml logs -f bot # {"msg":"bot started", ...}
```

Проверьте: напишите боту `/start`, затем `/admin`.

**Режим получения обновлений.** По умолчанию long polling — ничего больше не нужно. Webhook (если нужен):
`UPDATES_MODE=webhook`, `WEBHOOK_URL=https://bot.example.com/`, `WEBHOOK_SECRET=<openssl rand -hex 16>` (обязателен),
`WEBHOOK_DOMAIN=bot.example.com`, DNS домена на сервер, открытые порты 80/443, запуск с `--profile webhook`.

## Обновление

```bash
git pull
docker compose -f docker-compose.prod.yml up -d --build
```

Миграции применятся автоматически (`migrate`). Бот останавливается корректно (до 30 с): дорабатывает идущие обработчики
и воркер рассылки. Незавершённая рассылка продолжится после старта (очередь в БД). Перед обновлением с миграциями
сделайте резервную копию (ниже). Применённые миграции не редактируются — только новые файлы.

## Резервное копирование и восстановление

Сервис `backup` пишет `backups/bot_<UTC-время>.dump` (формат `pg_dump -Fc`, сжатый). Дамп пишется во временный файл и
переименовывается после успеха, поэтому полуготовых копий не бывает. **Копируйте каталог `backups/` с сервера**
(другой хост, S3, диск): копия на той же машине не защищает от потери машины.

Внеочередная копия:

```bash
docker compose -f docker-compose.prod.yml exec backup /scripts/backup.sh
```

Восстановление **в пустую базу** (скрипт отказывается работать с непустой):

```bash
# 1. остановить бота, чтобы он не писал в базу
docker compose -f docker-compose.prod.yml stop bot

# 2. создать пустую базу рядом и загрузить в неё дамп
docker compose -f docker-compose.prod.yml exec postgres createdb -U bot bot_restored
docker compose -f docker-compose.prod.yml run --rm --no-deps \
  -e PGHOST=postgres -e PGUSER=bot -e PGPASSWORD="$POSTGRES_PASSWORD" -e PGDATABASE=bot_restored \
  -v "$PWD/backups:/backups:ro" -v "$PWD/scripts:/scripts:ro" \
  --entrypoint /scripts/restore.sh backup /backups/bot_20260905T030000Z.dump

# 3. проверить данные, затем переключить бота: поменять имя базы в DATABASE_URL
#    (docker-compose.prod.yml) на bot_restored либо переименовать базы:
docker compose -f docker-compose.prod.yml exec postgres psql -U bot -d postgres \
  -c "ALTER DATABASE bot RENAME TO bot_old" -c "ALTER DATABASE bot_restored RENAME TO bot"
docker compose -f docker-compose.prod.yml start bot
```

**Проверяйте восстановление заранее** (раз в неделю-две, на тестовой машине): копия, которую ни разу не восстанавливали,
не копия. В дампе есть персональные данные (имена победителей до очистки): храните его так же аккуратно, как базу.

## Наблюдаемость

Порт `9090` бота публикуется только на `127.0.0.1` сервера; наружу его не открывайте (при необходимости — через VPN или
reverse proxy с авторизацией).

| Адрес | Что |
|---|---|
| `/healthz` | процесс жив (всегда 200) |
| `/readyz` | готов: база отвечает (иначе 503); его же проверяет `HEALTHCHECK` контейнера (`/bot healthcheck`) |
| `/metrics` | метрики Prometheus |

Prometheus:

```yaml
scrape_configs:
  - job_name: giveaway-bot
    static_configs:
      - targets: ["127.0.0.1:9090"]
```

Метрики бота:

| Метрика | Смысл |
|---|---|
| `bot_updates_total{handler,status}` | обработанные обновления по обработчикам; `status="error"` — ошибки |
| `bot_update_duration_seconds{handler}` | время обработки (гистограмма) |
| `bot_updates_in_flight` | обновления в обработке прямо сейчас |
| `bot_updates_duplicate_total` | отброшенные повторные доставки обновления |
| `bot_panics_total` | перехваченные паники |
| `bot_subscription_checks_total{result}` | проверки подписки: `subscribed`, `not_subscribed`, `error`, `cache_hit` |
| `bot_subscription_check_duration_seconds{result}` | задержка вызова `getChatMember` |
| `bot_telegram_rate_limited_total{source}` | ответы 429 от Telegram |
| `bot_broadcast_messages_total{result}` | рассылка: `sent`, `blocked`, `retry`, `failed`, `rate_limited` |
| `bot_db_pool_*` | пул соединений: занято, свободно, всего, максимум, число ожиданий |
| `go_goroutines`, `go_memstats_alloc_bytes`, `process_start_time_seconds` | рантайм |

Что стоит настроить в алертах: `/readyz` недоступен более минуты; `rate(bot_updates_total{status="error"}[5m])` растёт;
`increase(bot_panics_total[10m]) > 0`; `bot_db_pool_acquired_conns` близок к `bot_db_pool_max_conns` долго (мало
`DB_MAX_CONNS`); `increase(bot_telegram_rate_limited_total[5m]) > 0` (нагрузка упирается в лимиты Telegram);
`increase(bot_subscription_checks_total{result="error"}[5m])` (чаще всего бот не администратор канала партнёра);
`bot_updates_duplicate_total` скачком (проблемы с webhook). Метрики `/metrics` собираются собственным кодом в формате
text exposition 0.0.4, внешняя библиотека не нужна.

Логи — JSON в stdout (`docker compose logs bot`), уровни info/warn/error; ротация настроена в compose. Токен бота, пароль
БД и секрет webhook из логов вырезаются. В логах нет имён, username, текстов сообщений и промокодов — только числовые id.

## Безопасность (кратко)

- Секреты — только в окружении (`.env` с правами 600, не в git: он в `.gitignore`). Токен/пароль скрываются в логах,
  сообщениях админам и ошибках, сохраняемых в БД.
- Админские команды: каждая проходит `admin.Guard` (только `ADMIN_IDS`, только личный чат); посторонним бот не отвечает.
  Это проверяется тестом, который обходит все зарегистрированные маршруты.
- Кнопки пользователей не доверяют данным колбэка: действуют только на запись отправителя. Кнопки админа тоже под `Guard`.
- Webhook без `WEBHOOK_SECRET` не запускается.
- База не публикуется, контейнеры без capabilities и с read-only файловой системой.
- Персональные данные: только имя и фамилия победителей финала, с явным согласием, удаляются при отказе/просрочке и по
  `/purge_contacts confirm` после акции.

## Ёмкость и настройка

Подробности и цифры — [`docs/load-test.md`](load-test.md). Коротко: узкое место — лимит Telegram на `getChatMember`
(`TELEGRAM_CHECKS_PER_SECOND`, по умолчанию 25/с на всех пользователей вместе), а не база. Одновременный наплыв 1000
разных пользователей на «Проверить подписку» разбирается за ~40 с; тем, кто не уложился в 15 с, бот отвечает «слишком
много обращений, попробуйте через минуту». Промокоды выдаются 1000 пользователям за ~1 с.

## Один экземпляр

Запускайте **один** экземпляр бота. Планировщики и воркер рассылки безопасны при нескольких экземплярах (блокировки в БД),
но защита от повторной доставки, антифлуд 2 с и лимит `getChatMember` живут в памяти процесса, и при нескольких
экземплярах лимит к Telegram умножится. Для отказоустойчивости достаточно `restart: unless-stopped`: после падения бот
поднимается за секунды, состояние хранится в БД.

## Неполадки

| Симптом | Причина / что делать |
|---|---|
| бот не стартует: `create bot: telegram: Unauthorized` | неверный `BOT_TOKEN` |
| `Не удалось проверить, попробуй позже` в ответ на проверку подписки | бот не администратор канала партнёра, неверный `chat_id` (`/partner_list`, `/partner_edit`), либо лимит Telegram; смотрите `bot_subscription_checks_total{result="error"}` и лог `subscription check failed` |
| `invalid configuration: ...` | ошибка в `.env`; сообщение перечисляет все проблемы сразу |
| `/readyz` отвечает 503 | база недоступна: `docker compose ps postgres`, логи `postgres` |
| рассылка не идёт | `/broadcast_status`; воркер в логе `broadcast worker`; не нажата кнопка «Отправить» |
