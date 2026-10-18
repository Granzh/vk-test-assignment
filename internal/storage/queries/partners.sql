-- name: CreatePartner :one
INSERT INTO partners (name, url, chat_id, promo_text, promo_terms, promo_valid_until)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetPartner :one
SELECT * FROM partners WHERE id = $1;

-- name: ListPartners :many
SELECT * FROM partners ORDER BY id;

-- name: UpdatePartner :one
UPDATE partners
SET name              = @new_name,
    url               = @new_url,
    chat_id           = @new_chat_id,
    promo_text        = @new_promo_text,
    promo_terms       = @new_promo_terms,
    promo_valid_until = @new_valid_until
WHERE id = @id
RETURNING *;
