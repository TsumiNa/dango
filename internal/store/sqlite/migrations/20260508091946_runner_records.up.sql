CREATE TABLE IF NOT EXISTS runner_records (
	runner_id        TEXT NOT NULL,
	sequence_number  INTEGER NOT NULL,
	kind             TEXT NOT NULL,
	timestamp        TEXT NOT NULL,
	record_json      TEXT NOT NULL,
	PRIMARY KEY (runner_id, sequence_number)
);
