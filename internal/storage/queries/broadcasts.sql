-- name: CreateBroadcast :one
INSERT INTO broadcasts (text, created_by_telegram_id, segment, campaign_day_id)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetBroadcast :one
SELECT * FROM broadcasts WHERE id = $1;

-- name: LockBroadcast :one
SELECT * FROM broadcasts WHERE id = $1 FOR UPDATE;

-- name: StartBroadcast :one
UPDATE broadcasts SET status = 'sending', started_at = now()
WHERE id = $1 AND status = 'draft'
RETURNING *;

-- name: CancelBroadcast :one
-- Drafts and running broadcasts can be cancelled; pending recipients are then never sent.
UPDATE broadcasts SET status = 'cancelled', finished_at = now()
WHERE id = $1 AND status IN ('draft', 'sending')
RETURNING *;

-- name: FinishBroadcast :one
-- No row means somebody else finished it already.
UPDATE broadcasts SET status = 'done', finished_at = now()
WHERE id = $1 AND status = 'sending'
RETURNING *;

-- name: ListSendingBroadcasts :many
SELECT * FROM broadcasts WHERE status = 'sending' ORDER BY id;

-- name: GetLatestBroadcast :one
SELECT * FROM broadcasts ORDER BY id DESC LIMIT 1;

-- name: CountSegment :one
-- How many users a segment reaches now. Users who blocked the bot are never included.
SELECT count(*) FROM users u
WHERE NOT u.is_blocked AND (
    @segment::text = 'all'
    OR (@segment::text = 'no_promo' AND NOT EXISTS (
        SELECT 1 FROM promo_issues pi WHERE pi.user_id = u.id AND pi.campaign_day_id = @day_id::bigint))
    OR (@segment::text = 'no_entry' AND NOT EXISTS (
        SELECT 1 FROM daily_entries de WHERE de.user_id = u.id AND de.campaign_day_id = @day_id::bigint))
);

-- name: AddSegmentRecipients :execrows
-- Same predicate as CountSegment; run once, when the broadcast is confirmed.
INSERT INTO broadcast_recipients (broadcast_id, user_id, telegram_id)
SELECT @bid::bigint, u.id, u.telegram_id FROM users u
WHERE NOT u.is_blocked AND (
    @segment::text = 'all'
    OR (@segment::text = 'no_promo' AND NOT EXISTS (
        SELECT 1 FROM promo_issues pi WHERE pi.user_id = u.id AND pi.campaign_day_id = @day_id::bigint))
    OR (@segment::text = 'no_entry' AND NOT EXISTS (
        SELECT 1 FROM daily_entries de WHERE de.user_id = u.id AND de.campaign_day_id = @day_id::bigint))
)
ORDER BY u.id
ON CONFLICT (broadcast_id, user_id) DO NOTHING;

-- name: ClaimRecipients :many
-- Takes the next batch of pending recipients; SKIP LOCKED lets several workers coexist.
UPDATE broadcast_recipients
SET status = 'sending', claimed_at = now(), attempts = attempts + 1
WHERE id IN (
    SELECT br.id FROM broadcast_recipients br
    WHERE br.broadcast_id = @bid AND br.status = 'pending'
    ORDER BY br.id
    FOR UPDATE SKIP LOCKED
    LIMIT @batch
)
RETURNING *;

-- name: ReleaseStaleRecipients :execrows
-- Recovers recipients claimed by a worker that died before recording the outcome.
UPDATE broadcast_recipients SET status = 'pending'
WHERE broadcast_id = $1 AND status = 'sending' AND claimed_at < now() - interval '5 minutes';

-- name: MarkRecipientSent :exec
UPDATE broadcast_recipients SET status = 'sent', sent_at = now(), last_error = NULL WHERE id = $1;

-- name: MarkRecipientBlocked :exec
UPDATE broadcast_recipients SET status = 'blocked', last_error = @err::text WHERE id = @id;

-- name: MarkRecipientFailed :exec
UPDATE broadcast_recipients SET status = 'failed', last_error = @err::text WHERE id = @id;

-- name: RetryRecipient :exec
-- Puts a recipient back in the queue; give_back also returns the attempt taken
-- at claim time (used when Telegram asked to slow down, which is not the recipient's fault).
UPDATE broadcast_recipients
SET status = 'pending', last_error = @err::text, attempts = GREATEST(attempts - @give_back::int, 0)
WHERE id = @id;

-- name: BroadcastStats :one
SELECT count(*)                                      AS total,
       count(*) FILTER (WHERE status = 'pending')    AS pending,
       count(*) FILTER (WHERE status = 'sending')    AS in_flight,
       count(*) FILTER (WHERE status = 'sent')       AS sent,
       count(*) FILTER (WHERE status = 'failed')     AS failed,
       count(*) FILTER (WHERE status = 'blocked')    AS blocked
FROM broadcast_recipients WHERE broadcast_id = $1;
