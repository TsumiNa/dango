CREATE TABLE tools (
  name         TEXT PRIMARY KEY,
  image        TEXT NOT NULL,
  config_json  TEXT NOT NULL,
  registered   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE tasks (
  id           TEXT PRIMARY KEY,
  status       TEXT NOT NULL,
  request      TEXT,
  dag_json     TEXT,
  created      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE edges (
  id           TEXT PRIMARY KEY,
  task_id      TEXT NOT NULL REFERENCES tasks(id),
  tool_name    TEXT NOT NULL REFERENCES tools(name),
  upstream     TEXT,
  status       TEXT NOT NULL,
  shared_dir   TEXT,
  handoff_yaml TEXT,
  started      DATETIME,
  finished     DATETIME
);

CREATE TABLE logs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  edge_id      TEXT NOT NULL REFERENCES edges(id),
  level        TEXT NOT NULL,
  message      TEXT NOT NULL,
  timestamp    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE request_stream_events (
  request_id       TEXT NOT NULL,
  sequence_number  INTEGER NOT NULL,
  logical_time     INTEGER NOT NULL,
  event_type       TEXT NOT NULL,
  source_layer     TEXT NOT NULL,
  source_id        TEXT,
  source_parent_id TEXT,
  runner_id        TEXT,
  node_id          TEXT,
  session_id       TEXT,
  status           TEXT NOT NULL,
  timestamp        TEXT NOT NULL,
  raw_event_json   TEXT NOT NULL,
  PRIMARY KEY (request_id, sequence_number)
);

CREATE INDEX idx_request_stream_events_event_type
  ON request_stream_events (request_id, event_type, sequence_number);

CREATE INDEX idx_request_stream_events_runner
  ON request_stream_events (request_id, runner_id, sequence_number);

CREATE TABLE runner_records (
  runner_id        TEXT NOT NULL,
  sequence_number  INTEGER NOT NULL,
  kind             TEXT NOT NULL,
  timestamp        TEXT NOT NULL,
  record_json      TEXT NOT NULL,
  PRIMARY KEY (runner_id, sequence_number)
);

CREATE TABLE snapshot_cursors (
  request_id           TEXT PRIMARY KEY,
  runner_id            TEXT,
  checkpoint_sequence  INTEGER NOT NULL,
  event_sequence       INTEGER NOT NULL,
  updated_at           TEXT NOT NULL
);

CREATE INDEX idx_snapshot_cursors_runner
  ON snapshot_cursors (runner_id);
