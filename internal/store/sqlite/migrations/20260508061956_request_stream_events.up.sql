CREATE TABLE IF NOT EXISTS request_stream_events (
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

CREATE INDEX IF NOT EXISTS idx_request_stream_events_event_type
	ON request_stream_events (request_id, event_type, sequence_number);

CREATE INDEX IF NOT EXISTS idx_request_stream_events_runner
	ON request_stream_events (request_id, runner_id, sequence_number);
