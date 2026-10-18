# Бот розыгрышей

Telegram-бот фестивальной акции с партнёрами: промокоды, ежедневные розыгрыши призов партнёров по подписке на канал,
накопление шансов на главный приз, финальный розыгрыш, рассылки и статистика. Написан на Go (telebot v4, PostgreSQL,
pgx, sqlc, goose). Техническое задание — [`docs/spec.md`](docs/spec.md), оно главный источник бизнес-правил.

## Что нужно

- Go 1.25+ и Docker (для базы и для запуска стеком), либо свой PostgreSQL 16;
- токен бота от [@BotFather](https://t.me/BotFather);
- ваш Telegram id (любым ботом вроде @userinfobot) — он станет администратором;
- бот должен быть **администратором** каждого канала или группы партнёра, иначе подписку проверить нельзя.

## Как поднять локально

```bash
cp .env.example .env
# впишите BOT_TOKEN, ADMIN_IDS (ваш id, можно несколько через запятую),
# CAMPAIGN_START и CAMPAIGN_END (первый и последний день акции)
# В internal/texts надо заменить текст в файлах how_to.txt и rules.txt,
#  так как их не было в ТЗ
docker compose up --build        # postgres + миграции + бот
```

Откройте бота и отправьте `/start`: придёт приветствие и меню. Администратору доступна команда `/admin` — список всех
админских команд с форматами.

Без Docker для бота: `docker compose up -d postgres`, затем `make migrate-up DATABASE_URL=...` и `make run`
(переменные читаются из окружения, см. `.env.example`).

## Как провести акцию (шпаргалка администратора)

Админские команды работают только в личной переписке с ботом и только для `ADMIN_IDS`. Поля команд разделяются `|`.

**1. Завести партнёра.** Сделайте бота администратором канала партнёра. Числовой `chat_id` канала — отрицательное число
вида `-1001234567890` (его видно в адресе канала в web.telegram.org: `…/#-1001234567890`). Имя вида `@channel` для
проверки подписки не подходит.

```
/partner_add Кофейня Север | https://t.me/kofe_sever | -1001234567890 | Скидка 20% на второй напиток | кофейне на Арбате | 2026-09-30
/partner_list
/partner_edit 1 | chat_id | -1001234567890        # исправить одно поле
```

**2. Загрузить промокоды.** CSV-файл, один код в строке (первая строка может быть заголовком `code`), отправьте боту
файлом **с подписью** `/promo_upload <id партнёра>`. Бот отсеет дубли и некорректные строки и пришлёт отчёт
(загружено / отброшено и почему). Остатки: `/promo_stats`.

**3. Назначить партнёра на день:**

```
/day_set 2026-09-05 | 1 | 3 | Сертификат на 5000 ₽ и годовая подписка
```

дата, id партнёра, число победителей, призы. Один партнёр на дату; после розыгрыша день менять нельзя.

**4. Ежедневный розыгрыш.** После конца дня (с `AUTO_DRAW=true` бот проводит его сам):

```
/draw_run 2026-09-05        # победители выбираются случайно (crypto/rand), подписка перепроверяется;
                            # админ получает превью, пользователям ничего не уходит
/draw_confirm 2026-09-05    # подтверждение: победителям уходят уведомления
/winners 2026-09-05         # статусы победителей
/export partner_winners 2026-09-05   # CSV для партнёра (после подтверждения)
/export winners 2026-09-05           # CSV для команды
```

**5. Финальный розыгрыш** после последнего дня акции: `/final_run` (превью; проводится один раз), `/final_confirm`
(уведомления победителям с кнопкой «Получить приз»). Победитель вводит имя и фамилию и даёт согласие на передачу данных;
не ответил за `WINNER_RESPONSE_TIMEOUT` (по умолчанию 48 ч) — приз уходит следующему резервисту автоматически.
`/winners final` — статусы, `/winners final csv` — контакты согласившихся.

**6. Рассылка:** `/broadcast all|no_promo|no_entry | текст` — придёт предпросмотр и кнопки «Отправить / Отменить».
Подробности: [`docs/broadcast.md`](docs/broadcast.md). **Статистика:** `/stats` и `/stats csv`
([`docs/stats.md`](docs/stats.md)).

**7. После акции:** выгрузите контакты (`/winners final csv`), затем удалите имена: `/purge_contacts confirm`.

Все действия админов записываются в таблицу `audit_log`.

Примеры промокодов можно найти в [`promo_codes.csv`](/docs/examples/promo_codes.csv)

## Настройки

Все — переменные окружения, полный список с пояснениями в [`.env.example`](.env.example). Главные: `BOT_TOKEN`,
`DATABASE_URL`, `ADMIN_IDS`, `TIMEZONE`, `CAMPAIGN_START`/`CAMPAIGN_END`, `DAY_BOUNDARY` (границы «дня» акции
настраиваются, не зашиты), `MAX_CHANCES`, `WINNER_RESPONSE_TIMEOUT`, `UPDATES_MODE`.

## Разработка

```bash

make build         # go build -o bin/bot ./cmd/bot
make test          # go test ./... (integration tests are skipped without a database)
make test-db       # same, with TEST_DATABASE_URL pointing at the compose Postgres (docker compose up -d postgres first)
make run           # go run ./cmd/bot (needs env, see .env.example)
make lint          # go vet + staticcheck (pinned, run through go run)
make load-test     # 1000 simultaneous presses against the compose Postgres (docs/load-test.md)
make test-perf     # statistics on 100 000 users (opt-in, slow)
docker compose -f docker-compose.prod.yml up -d --build   # production stack (docs/deploy.md)
make migrate-up    # goose up   (needs DATABASE_URL)
make migrate-down  # goose down (one step)
make sqlc          # regenerate internal/storage/db from migrations + internal/storage/queries
docker compose up --build   # postgres + migrate + bot (needs .env with BOT_TOKEN)
docker compose --profile webhook up --build   # adds Caddy (HTTPS on WEBHOOK_DOMAIN -> bot:8080); set UPDATES_MODE=webhook, WEBHOOK_URL
go test ./internal/config -run TestLoadErrors   # single test
```

Устройство кода и соглашения — в [`CLAUDE.md`](CLAUDE.md); схема БД — [`docs/schema.md`](docs/schema.md).

## Продакшен

Образы на distroless, отдельный compose-файл, резервные копии, метрики Prometheus и healthcheck описаны в
[`docs/deploy.md`](docs/deploy.md)

## Описание архитектуры

- `cmd/bot` — entry point only: load config, open pool, build bot, run until SIGTERM/SIGINT, then stop the bot and close the pool.
- `internal/config` — env-based config with validation at startup (all errors reported together).
- `internal/domain` — entities and rules, no I/O. `Calendar.DayOf(t)` maps an instant to a campaign day (a midnight-UTC `time.Time` date) using TIMEZONE and DAY_BOUNDARY; use it everywhere instead of ad-hoc date math.
- `internal/texts` — every user-facing string (constants and templates). Long texts are embedded `.txt` files (`how_to.txt`, `rules.txt`). Do not inline Russian copy in handlers.
- `internal/storage` — pgx pool (`storage.go`); sqlc queries in `queries/` (one file per entity), generated code in `db/`. Schema comes from `migrations/`; overview in `docs/schema.md`.
- `internal/service` — use cases, orchestrating domain and storage. `Campaign.Active` returns today's campaign day and partner, or `ErrCampaignNotStarted` / `ErrCampaignFinished` / `ErrNoCampaignDay`.
- `internal/handlers` — telebot v4 handlers; thin, they call services. Menu buttons are declared in one route table in `Register`. `userOf(c)` returns the sender's `db.User` (internal `id`, needed for foreign keys) stored by the user middleware. Global middleware order: recover → logging → per-update context (15s timeout, read with `ctxOf`) → user upsert (refreshes `last_seen_at`, clears `is_blocked`) → blocked-by-user handling (marks `is_blocked`, swallows `tele.ErrBlockedByUser`). Handlers that call `c.Send` simply return its error and let the middleware deal with a blocked user.
- `internal/obs` — Prometheus metrics (own text-format registry, no client library), `/healthz`, `/readyz`, the instrumented router that counts and times every route. `internal/secret` redacts credentials (bot token, DB password, webhook secret) from logs, admin-visible errors and errors stored in the DB; wrap any error that may carry a Telegram URL with `secret.Wrap`. `internal/subscription` rate-limits getChatMember, waits out 429 and has the cached/merged checker. `internal/loadtest` is the opt-in load test.
- `internal/admin` — admin-only features, gated by `ADMIN_IDS` (`Guard` middleware: non-admins are silently ignored and logged; private chats only). `Service` does the work and audits each action in the same transaction; handlers parse `|`-separated arguments (`args.go`) and report problems as `*InputError` with detailed Russian text from `internal/texts/admin.go`.
- `internal/scheduler` — time-driven jobs: `AutoDraw` draws finished days when `AUTO_DRAW=true` (never confirms). `internal/notify` sends messages to users (winner notifications, admin alerts).

Dependencies point inward: handlers/admin/scheduler → service → domain/storage.
