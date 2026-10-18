# Схема БД

PostgreSQL, миграции goose в `migrations/`, запросы sqlc в `internal/storage/queries/`. Первичные ключи — суррогатные `BIGINT IDENTITY`. Статусы — `TEXT` с `CHECK`. Все моменты времени — `timestamptz` (UTC). Единственное исключение: `campaign_days.day` — `DATE`, локальная календарная дата кампании (по `TIMEZONE` и `DAY_BOUNDARY`).

## Связи

```
users ─┬─< promo_issues >─┬─ campaign_days >── partners
       ├─< daily_entries >┤        │
       ├─< activity_days >┘        └─< daily_winners >── users
       ├─< daily_winners
       └─< final_winners >── final_draw
promo_codes >── partners; promo_issues ── promo_codes (1:1)
broadcasts, audit_log — самостоятельные
```

## Таблицы

| Таблица | Назначение | Ключевые ограничения |
|---|---|---|
| `users` | Пользователи бота | `telegram_id` UNIQUE; `is_blocked` для рассылок; `last_seen_at` |
| `partners` | Партнёры | `chat_id` канала/группы для `getChatMember` (бот должен быть там админом); текст и условия промокода; `promo_valid_until` |
| `campaign_days` | День акции и его партнёр | `day` UNIQUE (один партнёр на дату); `winners_count > 0`; `status`: `pending → drawn → confirmed → notified` |
| `promo_codes` | Пул промокодов партнёра | `UNIQUE (partner_id, code)`; привязка `assigned_to_user_id` и `assigned_at` задаются только вместе; частичный индекс по свободным кодам |
| `promo_issues` | «Пользователь получил промокод в день X» | `UNIQUE (user_id, campaign_day_id)`; `promo_code_id` UNIQUE |
| `promo_requests` | Нажатия «Получить промокод» (для статистики) | `UNIQUE (user_id, campaign_day_id)`; `requests` — число нажатий, `issued` — выдавался ли код |
| `daily_entries` | Заявка в розыгрыш дня | `UNIQUE (user_id, campaign_day_id)`; `subscribed_checked_at` — момент успешной проверки подписки; `excluded_at`/`excluded_reason` — участник выбыл из розыгрыша (`unsubscribed`), строка остаётся |
| `activity_days` | Активные дни = шансы | `UNIQUE (user_id, campaign_day_id)`; лимит `MAX_CHANCES` проверяет сервис |
| `daily_winners` | Победители и резерв дня | `UNIQUE (campaign_day_id, user_id)`; `status`: `selected → notified → claimed / forfeited` |
| `final_draw` | Запуск финального розыгрыша (одна строка: уникальный индекс) | `status`: `drawn → confirmed → notified`; протокол: `held_at`, `held_by_telegram_id`, `drum_tickets`, `drum_users`, `winners_count`, `reserves_count`, `prize_tickets` |
| `final_winners` | Победители финала | `UNIQUE (final_draw_id, user_id)`; `tickets_count` — билеты пользователя в барабане (> 0); `reserve_position` — место в очереди резерва (только у резервистов); диалог получения приза: `dialog_step` (`name`/`consent`), `forfeit_reason`, `promoted_at`, `contact_purged_at`, `consent_given_at`; `contact_name` только вместе с `consent_given_at`; `status`: `selected → notified → contact_received / forfeited`; `deadline` — срок ответа (`WINNER_RESPONSE_TIMEOUT`) |
| `broadcasts` | Пуш-рассылки (п. 7) | `status`: `draft → sending → done / cancelled`; `segment`: `all`, `no_promo`, `no_entry`; `campaign_day_id` — день, к которому привязаны дневные сегменты. Итоги считаются по `broadcast_recipients`, а не хранятся |
| `broadcast_recipients` | Очередь получателей рассылки | `UNIQUE (broadcast_id, user_id)`; `status`: `pending → sending → sent / failed / blocked`; `attempts`, `last_error`, `claimed_at` (для возврата в очередь после падения воркера) |
| `audit_log` | Действия админов | `admin_telegram_id`, `action`, `entity_type/entity_id`, `details` JSONB |

## Как это используется

- **Промокод:** `ClaimPromoCode` забирает свободный код атомарно (`FOR UPDATE SKIP LOCKED`), `CreatePromoIssue` фиксирует выдачу. Повторное нажатие в тот же день читает код через `GetPromoIssueCode`. Пустой пул не блокирует участие в розыгрыше.
- **Тексты партнёра в сообщении с промокодом:** `partners.promo_text` подставляется после слова «на» («Используй его до {дата} на {promo_text}.»), поэтому хранится в предложном падеже: «сайте example.ru», «точках продаж». `promo_valid_until` показывается датой в `TIMEZONE`; `promo_terms` в сообщении не используется.
- **Один шанс в день:** и промокод, и заявка вызывают `AddActivityDay`; повтор в тот же день молча игнорируется (`ON CONFLICT DO NOTHING`).
- **Дата выдачи промокода** хранится как `campaign_days.day` через `promo_issues.campaign_day_id`, отдельной колонки в `promo_codes` нет.
- **Финальный розыгрыш:** билеты считает `ListChancesPerUser` (число активных дней пользователя).
- **Истёкший срок ответа:** `ListExpiredFinalWinners` (индекс `status, deadline`) находит победителей для передачи приза резерву.
- **Статистика:** `CountUsers`, `CountNewUsersByDay`, `CountIssuedPromoCodes`, `CountFreePromoCodes`, `CountDailyEntries`. Число запросов промокода в схеме не хранится (только выдачи).
