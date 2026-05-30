CREATE TABLE IF NOT EXISTS snapshot_cursors (
  request_id           TEXT PRIMARY KEY,
  runner_id            TEXT,
  checkpoint_sequence  BIGINT NOT NULL,
  event_sequence       BIGINT NOT NULL,
  updated_at           TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_snapshot_cursors_runner
  ON snapshot_cursors (runner_id);
