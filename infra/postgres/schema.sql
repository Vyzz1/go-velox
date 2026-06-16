-- Rule storage for config-service.
-- This file is BOTH the Postgres init schema (mounted into the container's
-- /docker-entrypoint-initdb.d) AND the schema sqlc reads for type inference.
--
-- "limit" is a reserved word in SQL, so the column is named limit_count.

CREATE TABLE IF NOT EXISTS rules (
    tenant_id   TEXT        NOT NULL,
    rule_id     TEXT        NOT NULL,
    algorithm   TEXT        NOT NULL DEFAULT 'gcra',
    limit_count BIGINT      NOT NULL,
    period_secs BIGINT      NOT NULL,
    burst       BIGINT      NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, rule_id)
);
