-- name: CreateDailyEntry :one
-- Returns no rows when the user is already registered for the day.
INSERT INTO daily_entries (user_id, campaign_day_id, subscribed_checked_at)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, campaign_day_id) DO NOTHING
RETURNING *;

-- name: GetDailyEntry :one
SELECT * FROM daily_entries WHERE user_id = $1 AND campaign_day_id = $2;

-- name: CountDailyEntries :one
SELECT count(*) FROM daily_entries WHERE campaign_day_id = $1;

-- name: AddActivityDay :one
-- Returns no rows when the day was already counted as active.
INSERT INTO activity_days (user_id, campaign_day_id)
VALUES ($1, $2)
ON CONFLICT (user_id, campaign_day_id) DO NOTHING
RETURNING *;

-- name: CountActivityDays :one
SELECT count(*) FROM activity_days WHERE user_id = $1;

-- name: ListActivityDays :many
SELECT cd.day, cd.partner_id, p.name AS partner_name
FROM activity_days ad
JOIN campaign_days cd ON cd.id = ad.campaign_day_id
JOIN partners p ON p.id = cd.partner_id
WHERE ad.user_id = $1
ORDER BY cd.day;

-- name: ListUserDays :many
-- Every campaign day the user did something on: took the day's promo code
-- and/or registered in its draw. counted is true when the day earned a chance.
SELECT cd.day,
       p.id   AS partner_id,
       p.name AS partner_name,
       EXISTS (SELECT 1 FROM promo_issues pi
               WHERE pi.user_id = @uid AND pi.campaign_day_id = cd.id) AS has_promo,
       EXISTS (SELECT 1 FROM daily_entries de
               WHERE de.user_id = @uid AND de.campaign_day_id = cd.id) AS has_entry,
       EXISTS (SELECT 1 FROM activity_days ad
               WHERE ad.user_id = @uid AND ad.campaign_day_id = cd.id) AS counted
FROM campaign_days cd
JOIN partners p ON p.id = cd.partner_id
WHERE EXISTS (SELECT 1 FROM promo_issues pi2 WHERE pi2.user_id = @uid AND pi2.campaign_day_id = cd.id)
   OR EXISTS (SELECT 1 FROM daily_entries de2 WHERE de2.user_id = @uid AND de2.campaign_day_id = cd.id)
ORDER BY cd.day;

-- name: ListDayParticipants :many
SELECT u.telegram_id, u.username, u.first_name, de.subscribed_checked_at, de.excluded_reason
FROM daily_entries de
JOIN users u ON u.id = de.user_id
WHERE de.campaign_day_id = $1
ORDER BY de.id;

-- name: ListDrawCandidates :many
-- Entries still in the running for the day's draw.
SELECT de.id AS entry_id, u.id AS user_id, u.telegram_id, u.username, u.first_name
FROM daily_entries de
JOIN users u ON u.id = de.user_id
WHERE de.campaign_day_id = $1 AND de.excluded_at IS NULL
ORDER BY de.id;

-- name: ExcludeDailyEntry :exec
UPDATE daily_entries SET excluded_at = now(), excluded_reason = @reason::text WHERE id = @id;

-- name: CountExcludedEntries :one
SELECT count(*) FROM daily_entries WHERE campaign_day_id = $1 AND excluded_at IS NOT NULL;
