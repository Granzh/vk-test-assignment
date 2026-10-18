-- name: InsertAuditLog :exec
INSERT INTO audit_log (admin_telegram_id, action, entity_type, entity_id, details)
VALUES ($1, $2, $3, $4, $5);

-- name: ListAuditLog :many
SELECT * FROM audit_log ORDER BY id DESC LIMIT $1;
