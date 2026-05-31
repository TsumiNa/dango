CREATE TABLE IF NOT EXISTS runner_records (
  runner_id        TEXT NOT NULL,
  sequence_number  BIGINT NOT NULL,
  kind             TEXT NOT NULL,
  timestamp        TIMESTAMPTZ NOT NULL,
  record_json      JSONB NOT NULL,
  PRIMARY KEY (runner_id, sequence_number)
);
