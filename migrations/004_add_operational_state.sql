CREATE TABLE IF NOT EXISTS event_status_history (
    id BIGSERIAL PRIMARY KEY,
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (btrim(status) <> ''),
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_event_status_history_event_created
    ON event_status_history (event_id, created_at, id);

CREATE TABLE IF NOT EXISTS delivery_attempts (
    id BIGSERIAL PRIMARY KEY,
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    outcome TEXT NOT NULL CHECK (btrim(outcome) <> ''),
    response_code INTEGER,
    error TEXT,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_delivery_attempts_event_started
    ON delivery_attempts (event_id, started_at, id);

CREATE TABLE IF NOT EXISTS dlq_records (
    event_id UUID PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    original_stream_id TEXT NOT NULL,
    attempt_count INTEGER NOT NULL CHECK (attempt_count > 0),
    last_error TEXT,
    status TEXT NOT NULL DEFAULT 'OPEN' CHECK (btrim(status) <> ''),
    dead_lettered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    redriven_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_dlq_records_open_dead_lettered
    ON dlq_records (dead_lettered_at)
    WHERE status = 'OPEN';
