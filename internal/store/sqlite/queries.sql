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
