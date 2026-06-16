-- name: UpsertRule :one
INSERT INTO rules (tenant_id, rule_id, algorithm, limit_count, period_secs, burst)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (tenant_id, rule_id) DO UPDATE SET
    algorithm   = EXCLUDED.algorithm,
    limit_count = EXCLUDED.limit_count,
    period_secs = EXCLUDED.period_secs,
    burst       = EXCLUDED.burst,
    updated_at  = now()
RETURNING *;

-- name: GetRule :one
SELECT * FROM rules
WHERE tenant_id = $1 AND rule_id = $2;

-- name: ListRulesByTenant :many
SELECT * FROM rules
WHERE tenant_id = $1
ORDER BY rule_id;

-- name: ListAllRules :many
SELECT * FROM rules
ORDER BY tenant_id, rule_id;

-- name: DeleteRule :execrows
DELETE FROM rules
WHERE tenant_id = $1 AND rule_id = $2;
