-- name: UpsertTool :exec
INSERT INTO tools (name, image, config_json)
VALUES (sqlc.arg(name), sqlc.arg(image), sqlc.arg(config_json))
ON CONFLICT(name) DO UPDATE SET
  image = excluded.image,
  config_json = excluded.config_json,
  registered = CURRENT_TIMESTAMP;

-- name: DeleteTool :execrows
DELETE FROM tools
WHERE name = sqlc.arg(name);

-- name: GetTool :one
SELECT name, image, config_json, registered
FROM tools
WHERE name = sqlc.arg(name)
LIMIT 1;

-- name: ListTools :many
SELECT name, image, config_json, registered
FROM tools
ORDER BY name ASC;

-- name: CreateTask :exec
INSERT INTO tasks (id, status, request, dag_json)
VALUES (
  sqlc.arg(id),
  sqlc.arg(status),
  sqlc.narg(request),
  sqlc.narg(dag_json)
);

-- name: GetTask :one
SELECT id, status, request, dag_json, created, updated
FROM tasks
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: ListTasks :many
SELECT id, status, request, dag_json, created, updated
FROM tasks
ORDER BY updated DESC, created DESC;

-- name: UpdateTaskStatus :execrows
UPDATE tasks
SET status = sqlc.arg(status), updated = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: UpdateTaskPlan :execrows
UPDATE tasks
SET
  status = sqlc.arg(status),
  dag_json = sqlc.narg(dag_json),
  updated = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: UpsertEdge :exec
INSERT INTO edges (
  id,
  task_id,
  tool_name,
  upstream,
  status,
  shared_dir,
  handoff_yaml,
  started,
  finished
) VALUES (
  sqlc.arg(id),
  sqlc.arg(task_id),
  sqlc.arg(tool_name),
  sqlc.narg(upstream),
  sqlc.arg(status),
  sqlc.narg(shared_dir),
  sqlc.narg(handoff_yaml),
  sqlc.narg(started),
  sqlc.narg(finished)
)
ON CONFLICT(id) DO UPDATE SET
  task_id = excluded.task_id,
  tool_name = excluded.tool_name,
  upstream = excluded.upstream,
  status = excluded.status,
  shared_dir = excluded.shared_dir,
  handoff_yaml = excluded.handoff_yaml,
  started = excluded.started,
  finished = excluded.finished;

-- name: UpdateEdgeResult :execrows
UPDATE edges
SET
  status = sqlc.arg(status),
  handoff_yaml = sqlc.narg(handoff_yaml),
  finished = sqlc.narg(finished)
WHERE id = sqlc.arg(edge_id);

-- name: InsertLog :exec
INSERT INTO logs (edge_id, level, message)
VALUES (sqlc.arg(edge_id), sqlc.arg(level), sqlc.arg(message));

-- name: InsertRequestStreamEvent :exec
INSERT INTO request_stream_events (
  request_id,
  sequence_number,
  logical_time,
  event_type,
  source_layer,
  source_id,
  source_parent_id,
  runner_id,
  node_id,
  session_id,
  status,
  timestamp,
  raw_event_json
) VALUES (
  sqlc.arg(request_id),
  sqlc.arg(sequence_number),
  sqlc.arg(logical_time),
  sqlc.arg(event_type),
  sqlc.arg(source_layer),
  sqlc.narg(source_id),
  sqlc.narg(source_parent_id),
  sqlc.narg(runner_id),
  sqlc.narg(node_id),
  sqlc.narg(session_id),
  sqlc.arg(status),
  sqlc.arg(timestamp),
  sqlc.arg(raw_event_json)
);

-- name: ListRequestStreamEvents :many
SELECT sequence_number, raw_event_json
FROM request_stream_events
WHERE request_id = sqlc.arg(request_id)
  AND sequence_number >= sqlc.arg(from_sequence_number)
  AND (sqlc.narg(runner_id) IS NULL OR runner_id = sqlc.narg(runner_id))
  AND (sqlc.narg(node_id) IS NULL OR node_id = sqlc.narg(node_id))
  AND (sqlc.narg(session_id) IS NULL OR session_id = sqlc.narg(session_id))
  AND (sqlc.narg(status) IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(event_type) IS NULL OR event_type = sqlc.narg(event_type))
  AND (sqlc.narg(event_type_prefix) IS NULL OR event_type LIKE sqlc.narg(event_type_prefix) || '%')
  AND (sqlc.narg(source_layer) IS NULL OR source_layer = sqlc.narg(source_layer))
  AND (sqlc.narg(source_id) IS NULL OR source_id = sqlc.narg(source_id))
  AND (sqlc.narg(source_parent_id) IS NULL OR source_parent_id = sqlc.narg(source_parent_id))
ORDER BY sequence_number ASC;

-- name: GetRunnerRecordState :one
SELECT
  CAST(COALESCE(MAX(sequence_number), 0) AS INTEGER) AS last_sequence_number,
  CAST(COALESCE(MAX(CASE WHEN kind = 'init' THEN 1 ELSE 0 END), 0) AS INTEGER) AS has_init
FROM runner_records
WHERE runner_id = sqlc.arg(runner_id);

-- name: InsertRunnerRecord :exec
INSERT INTO runner_records (
  runner_id,
  sequence_number,
  kind,
  timestamp,
  record_json
) VALUES (
  sqlc.arg(runner_id),
  sqlc.arg(sequence_number),
  sqlc.arg(kind),
  sqlc.arg(timestamp),
  sqlc.arg(record_json)
);

-- name: ListRunnerRecords :many
SELECT sequence_number, kind, record_json
FROM runner_records
WHERE runner_id = sqlc.arg(runner_id)
ORDER BY sequence_number ASC;

-- name: DeleteRunnerRecords :execrows
DELETE FROM runner_records
WHERE runner_id = sqlc.arg(runner_id);
