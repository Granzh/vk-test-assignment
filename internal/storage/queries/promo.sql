-- name: AddPromoCode :exec
INSERT INTO promo_codes (partner_id, code)
VALUES ($1, $2)
ON CONFLICT (partner_id, code) DO NOTHING;

-- name: ClaimPromoCode :one
-- Atomically takes one free code; returns no rows when the pool is empty.
UPDATE promo_codes
SET assigned_to_user_id = @target_user_id, assigned_at = now()
WHERE id = (
    SELECT pc.id FROM promo_codes pc
    WHERE pc.partner_id = @target_partner_id AND pc.assigned_to_user_id IS NULL
    ORDER BY pc.id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING *;

-- name: CreatePromoIssue :one
INSERT INTO promo_issues (user_id, campaign_day_id, promo_code_id)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, campaign_day_id) DO NOTHING
RETURNING *;

-- name: GetPromoIssueCode :one
SELECT pc.code, pi.issued_at
FROM promo_issues pi
JOIN promo_codes pc ON pc.id = pi.promo_code_id
WHERE pi.user_id = $1 AND pi.campaign_day_id = $2;

-- name: CountFreePromoCodes :one
SELECT count(*) FROM promo_codes
WHERE partner_id = $1 AND assigned_to_user_id IS NULL;

-- name: CountIssuedPromoCodes :one
SELECT count(*) FROM promo_codes
WHERE partner_id = $1 AND assigned_to_user_id IS NOT NULL;

-- name: AddPromoCodes :execrows
-- Bulk insert; returns how many codes were new (existing ones are skipped).
INSERT INTO promo_codes (partner_id, code)
SELECT @pid::bigint, c FROM unnest(@codes::text[]) AS c
ON CONFLICT (partner_id, code) DO NOTHING;

-- name: ListPromoStats :many
-- Stock per partner from two index-only counts (free and issued codes).
SELECT p.id, p.name, (f.n + i.n)::bigint AS total, f.n::bigint AS free
FROM partners p
CROSS JOIN LATERAL (
    SELECT count(*) AS n FROM promo_codes pc
    WHERE pc.partner_id = p.id AND pc.assigned_to_user_id IS NULL
) f
CROSS JOIN LATERAL (
    SELECT count(*) AS n FROM promo_codes pc2
    WHERE pc2.partner_id = p.id AND pc2.assigned_to_user_id IS NOT NULL
) i
ORDER BY p.id;

-- name: CountPromoIssuesByDay :one
SELECT count(*) FROM promo_issues WHERE campaign_day_id = $1;
