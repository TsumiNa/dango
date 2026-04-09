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
