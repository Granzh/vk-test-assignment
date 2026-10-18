-- name: CreateCampaignDay :one
INSERT INTO campaign_days (day, partner_id, winners_count, prizes_description)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetCampaignDayByDate :one
SELECT * FROM campaign_days WHERE day = $1;

-- name: GetCampaignDay :one
SELECT * FROM campaign_days WHERE id = $1;

-- name: ListCampaignDays :many
SELECT * FROM campaign_days ORDER BY day;

-- name: ListCampaignDaysByStatus :many
SELECT * FROM campaign_days WHERE status = $1 ORDER BY day;

-- name: SetCampaignDayStatus :one
UPDATE campaign_days
SET status       = @status::text,
    drawn_at     = CASE WHEN @status::text = 'drawn' THEN now() ELSE drawn_at END,
    confirmed_at = CASE WHEN @status::text = 'confirmed' THEN now() ELSE confirmed_at END
WHERE id = @id
RETURNING *;

-- name: UpdateCampaignDay :one
UPDATE campaign_days
SET partner_id         = @new_partner_id,
    winners_count      = @new_winners_count,
    prizes_description = @new_prizes
WHERE id = @id
RETURNING *;

-- name: LockCampaignDay :one
-- Serializes draws of one day (and entries being created for it, see GetCampaignDayShared).
SELECT * FROM campaign_days WHERE id = $1 FOR UPDATE;

-- name: GetCampaignDayShared :one
-- Entry creation takes a shared lock so it cannot slip in while the day is being drawn.
SELECT * FROM campaign_days WHERE id = $1 FOR SHARE;
