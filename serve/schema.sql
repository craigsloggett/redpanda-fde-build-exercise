CREATE TABLE IF NOT EXISTS verdicts (
  rev_id         BIGINT PRIMARY KEY,
  rev_parent_id  BIGINT NOT NULL,
  title          TEXT NOT NULL,
  editor         TEXT NOT NULL,
  editor_is_temp BOOLEAN NOT NULL,
  comment        TEXT NOT NULL DEFAULT '',
  bytes_delta    INTEGER NOT NULL,
  event_ts       TIMESTAMPTZ NOT NULL,
  diff_url       TEXT NOT NULL,
  tier           TEXT NOT NULL,
  diff           TEXT NOT NULL DEFAULT '',
  diff_truncated BOOLEAN NOT NULL DEFAULT FALSE,
  label          TEXT NOT NULL,
  confidence     REAL NOT NULL,
  reason         TEXT NOT NULL DEFAULT '',
  evidence       TEXT NOT NULL DEFAULT '',
  grounded       BOOLEAN NOT NULL DEFAULT FALSE,
  route          TEXT NOT NULL,
  steps          JSONB NOT NULL DEFAULT '[]',
  model          TEXT NOT NULL,
  attempts       INTEGER NOT NULL,
  tokens         INTEGER NOT NULL DEFAULT 0,
  latency_ms     INTEGER NOT NULL,
  reasoned_at    TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS verdicts_reasoned_at_idx ON verdicts (reasoned_at DESC);
CREATE INDEX IF NOT EXISTS verdicts_route_idx ON verdicts (route, confidence DESC);

CREATE OR REPLACE FUNCTION notify_verdict() RETURNS trigger AS $$
BEGIN
  PERFORM pg_notify('verdicts', NEW.rev_id::text);
  RETURN NULL;
END
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER verdicts_notify
  AFTER INSERT OR UPDATE ON verdicts
  FOR EACH ROW EXECUTE FUNCTION notify_verdict();
