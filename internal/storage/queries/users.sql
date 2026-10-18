-- name: UpsertUser :one
-- Registers the sender or refreshes them. A user who writes again is no longer
-- blocked. To keep a busy user from rewriting their row on every update (dead
-- tuples, WAL), the row is updated only when something changed or last_seen_at
-- is more than a minute old; otherwise the current row is returned as it is.
WITH upserted AS (
    INSERT INTO users (telegram_id, username, first_name)
    VALUES ($1, $2, $3)
    ON CONFLICT (telegram_id) DO UPDATE
    SET username     = EXCLUDED.username,
        first_name   = EXCLUDED.first_name,
        last_seen_at = now(),
        is_blocked   = false
    WHERE users.username   IS DISTINCT FROM EXCLUDED.username
       OR users.first_name IS DISTINCT FROM EXCLUDED.first_name
       OR users.is_blocked
       OR users.last_seen_at < now() - interval '1 minute'
    RETURNING *
)
SELECT * FROM upserted
UNION ALL
SELECT * FROM users u
WHERE u.telegram_id = $1 AND NOT EXISTS (SELECT 1 FROM upserted);

-- name: GetUserByTelegramID :one
SELECT * FROM users WHERE telegram_id = $1;

-- name: SetUserBlocked :exec
UPDATE users SET is_blocked = $2 WHERE telegram_id = $1;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CountNewUsersByDay :many
SELECT (created_at AT TIME ZONE @tz::text)::date AS day, count(*) AS users
FROM users
GROUP BY 1
ORDER BY 1;

-- name: ListActiveUsers :many
SELECT id, telegram_id FROM users WHERE NOT is_blocked ORDER BY id;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;
