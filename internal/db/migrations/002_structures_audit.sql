-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS structures_audit (
    id            uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    structure_id  uuid NOT NULL REFERENCES structures(id) ON DELETE CASCADE,
    action        text NOT NULL CHECK (action IN ('created', 'updated', 'deleted')),
    changes       jsonb NOT NULL DEFAULT '{}'::jsonb,
    actor         text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_structures_audit_structure_id ON structures_audit(structure_id);
CREATE INDEX IF NOT EXISTS idx_structures_audit_created_at ON structures_audit(created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS structures_audit;
-- +goose StatementEnd
