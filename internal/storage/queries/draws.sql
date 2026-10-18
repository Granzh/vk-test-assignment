-- name: ListEligibleEntryUserIDs :many
SELECT user_id FROM daily_entries WHERE campaign_day_id = $1 ORDER BY user_id;

-- name: InsertDailyWinner :one
-- Returns no rows when the user is already a winner of this day.
INSERT INTO daily_winners (campaign_day_id, user_id, prize, is_reserve)
VALUES ($1, $2, $3, $4)
ON CONFLICT (campaign_day_id, user_id) DO NOTHING
RETURNING *;

-- name: ListDailyWinners :many
SELECT dw.*, u.telegram_id, u.username
FROM daily_winners dw
JOIN users u ON u.id = dw.user_id
WHERE dw.campaign_day_id = $1
ORDER BY dw.is_reserve, dw.id;

-- name: SetDailyWinnerStatus :exec
UPDATE daily_winners SET status = $2 WHERE id = $1;

-- name: CreateFinalDraw :one
INSERT INTO final_draw DEFAULT VALUES RETURNING *;

-- name: GetFinalDraw :one
SELECT * FROM final_draw WHERE id = $1;

-- name: SetFinalDrawStatus :one
UPDATE final_draw
SET status       = @status::text,
    confirmed_at = CASE WHEN @status::text = 'confirmed' THEN now() ELSE confirmed_at END
WHERE id = @id
RETURNING *;

-- name: ListChancesPerUser :many
-- Final draw tickets: one per active day.
SELECT user_id, count(*)::int AS tickets
FROM activity_days
GROUP BY user_id
ORDER BY user_id;

-- name: InsertFinalWinner :one
INSERT INTO final_winners (final_draw_id, user_id, tickets_count, is_reserve)
VALUES ($1, $2, $3, $4)
ON CONFLICT (final_draw_id, user_id) DO NOTHING
RETURNING *;

-- name: ListFinalWinners :many
SELECT fw.*, u.telegram_id, u.username
FROM final_winners fw
JOIN users u ON u.id = fw.user_id
WHERE fw.final_draw_id = $1
ORDER BY fw.is_reserve, fw.id;

-- name: MarkFinalWinnerNotified :exec
UPDATE final_winners SET status = 'notified', deadline = $2 WHERE id = $1;

-- name: ListExpiredFinalWinners :many
SELECT * FROM final_winners
WHERE status = 'notified' AND deadline < $1
ORDER BY deadline;

-- name: SetFinalWinnerStatus :exec
UPDATE final_winners SET status = $2 WHERE id = $1;

-- name: ListDayWinners :many
SELECT dw.id, dw.user_id, dw.prize, dw.is_reserve, dw.status, u.telegram_id, u.username, u.first_name
FROM daily_winners dw
JOIN users u ON u.id = dw.user_id
WHERE dw.campaign_day_id = $1
ORDER BY dw.is_reserve, dw.id;

-- name: ClaimDailyWinnerForNotify :one
-- Atomically moves a winner from selected to notified; no row means somebody else has it.
UPDATE daily_winners SET status = 'notified' WHERE id = $1 AND status = 'selected' RETURNING id;

-- name: LockFinalDrawStage :one
-- Serializes the (single) final draw run across connections until the transaction ends.
SELECT 1::int AS locked FROM (SELECT pg_advisory_xact_lock(730502)) AS l;

-- name: GetFinalDrawOnly :one
SELECT * FROM final_draw ORDER BY id LIMIT 1;

-- name: LockFinalDrawRow :one
SELECT * FROM final_draw WHERE id = $1 FOR UPDATE;

-- name: CreateFinalDrawHeld :one
INSERT INTO final_draw (held_by_telegram_id, drum_tickets, drum_users, winners_count, reserves_count, prize_tickets)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: InsertFinalWinnerRanked :one
-- reserve_position is NULL for winners and 1.. for the reserve queue.
INSERT INTO final_winners (final_draw_id, user_id, tickets_count, is_reserve, reserve_position)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (final_draw_id, user_id) DO NOTHING
RETURNING *;

-- name: ListFinalDrawWinners :many
-- Winners first, then the reserve queue in order.
SELECT fw.id, fw.user_id, fw.tickets_count, fw.is_reserve, fw.reserve_position, fw.status,
       fw.deadline, fw.contact_name, fw.consent_given_at, fw.dialog_step, fw.forfeit_reason, fw.promoted_at,
       fw.contact_purged_at, u.telegram_id, u.username, u.first_name
FROM final_winners fw
JOIN users u ON u.id = fw.user_id
WHERE fw.final_draw_id = $1
ORDER BY fw.is_reserve, fw.reserve_position, fw.id;

-- name: ClaimFinalWinnerForNotify :one
-- Atomically moves a winner from selected to notified and sets the response deadline;
-- no row means somebody else has it.
UPDATE final_winners SET status = 'notified', deadline = $2 WHERE id = $1 AND status = 'selected' RETURNING id;

-- name: ReleaseFinalWinner :exec
UPDATE final_winners SET status = 'selected', deadline = NULL WHERE id = $1 AND status = 'notified';

-- name: GetFinalWinnerOfUser :one
SELECT * FROM final_winners WHERE user_id = $1 ORDER BY id DESC LIMIT 1;

-- name: StartFinalContactDialog :one
-- The state machine entry: nothing -> asking for the name. No row: not an active winner.
UPDATE final_winners SET dialog_step = 'name'
WHERE id = $1 AND status = 'notified' AND dialog_step IS NULL AND deadline > now()
RETURNING *;

-- name: SaveFinalContactName :one
-- name -> consent; no row means the step or the deadline does not allow it.
UPDATE final_winners SET contact_name = @full_name::text, dialog_step = 'consent'
WHERE id = @id AND status = 'notified' AND dialog_step = 'name' AND deadline > now()
RETURNING *;

-- name: GiveFinalConsent :one
-- consent -> contact_received with the consent time.
UPDATE final_winners SET consent_given_at = now(), status = 'contact_received', dialog_step = NULL
WHERE id = $1 AND status = 'notified' AND dialog_step = 'consent' AND deadline > now()
RETURNING *;

-- name: ForfeitFinalWinner :one
-- Takes the prize away (declined, expired, undeliverable) and wipes the personal data.
-- With only_expired the row must be past its deadline.
UPDATE final_winners
SET status = 'forfeited', forfeit_reason = @reason::text,
    contact_name = NULL, consent_given_at = NULL, dialog_step = NULL
WHERE id = @id AND status = 'notified' AND (NOT @only_expired::bool OR deadline < now())
RETURNING *;

-- name: PromoteNextReserve :one
-- Moves the first reserve in the queue to an active winner with a fresh deadline.
UPDATE final_winners
SET status = 'notified', promoted_at = now(), deadline = @new_deadline
WHERE id = (
    SELECT fw.id FROM final_winners fw
    WHERE fw.final_draw_id = @draw_id AND fw.is_reserve AND fw.status = 'selected' AND fw.promoted_at IS NULL
    ORDER BY fw.reserve_position
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING *;

-- name: CountFinalContacts :one
SELECT count(*) FROM final_winners WHERE contact_name IS NOT NULL;

-- name: PurgeFinalContacts :execrows
-- Removes collected names. Consent records stay as evidence; a dialog waiting
-- for the consent of a purged name is reset.
UPDATE final_winners
SET contact_name = NULL, contact_purged_at = now(),
    dialog_step = CASE WHEN dialog_step = 'consent' THEN NULL ELSE dialog_step END
WHERE contact_name IS NOT NULL;
