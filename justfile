set shell := ["zsh", "-cu"]

default:
    @just --list

# Regenerate sqlc wrappers for the SQLite store.
db-generate:
    go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate

# Create the next SQLite migration pair under internal/store/sqlite/migrations/.
db-new-migration name:
    @test -n "{{name}}" || { echo "usage: just db-new-migration <name>" >&2; exit 1; }
    @version=$(date -u +%Y%m%d%H%M%S); base="internal/store/sqlite/migrations/${version}_{{name}}"; touch "${base}.up.sql" "${base}.down.sql"; echo "created ${base}.up.sql"; echo "created ${base}.down.sql"

# Open the orchestrator SQLite database in the sqlite3 shell.
db-open data_dir=".dango-demo":
    @command -v sqlite3 >/dev/null || { echo "sqlite3 is required for db-open" >&2; exit 1; }
    sqlite3 {{quote(data_dir)}}/dango.db

# Execute a one-off SQL statement against the orchestrator SQLite database.
db-query sql data_dir=".dango-demo":
    @test -n "{{sql}}" || { echo "usage: just db-query <sql> [data_dir]" >&2; exit 1; }
    @command -v sqlite3 >/dev/null || { echo "sqlite3 is required for db-query" >&2; exit 1; }
    sqlite3 -header -column {{quote(data_dir)}}/dango.db {{quote(sql)}}

# Run the Go test suite with isolated caches.
test:
    GOCACHE=/tmp/dango-gocache GOMODCACHE=/tmp/dango-gomodcache go test ./...

# Run the local demo pipeline with the built-in toy tools.
demo request="Write a short project status update" data_dir=".dango-demo":
    GOCACHE=/tmp/dango-gocache GOMODCACHE=/tmp/dango-gomodcache go run ./cmd/dango orchestrator demo-run --data-dir {{quote(data_dir)}} --request {{quote(request)}}

# Run the local demo pipeline with verbose logging persisted to a file.
demo-debug request="Write a short project status update" data_dir=".dango-demo" log_file=".dango-demo/demo.log":
    GOCACHE=/tmp/dango-gocache GOMODCACHE=/tmp/dango-gomodcache go run ./cmd/dango orchestrator demo-run --data-dir {{quote(data_dir)}} --request {{quote(request)}} --log-level debug --log-format text --log-file {{quote(log_file)}}

# Start the local orchestrator HTTP server.
serve data_dir=".dango-demo" port="8080":
    GOCACHE=/tmp/dango-gocache GOMODCACHE=/tmp/dango-gomodcache go run ./cmd/dango orchestrator serve --data-dir {{quote(data_dir)}} --port {{port}}

# Summarize a runner's stream-event log: bash command-head distribution,
# inner-bodies of Turing-complete heads, per-skill tallies, curl/wget URLs.
# Writes the markdown report to stdout (or `out_md`) plus an optional JSON
# sidecar at `out_json`.
analyze-traces jsonl out_md="" out_json="":
    @test -n "{{jsonl}}" || { echo "usage: just analyze-traces <stream_events.jsonl> [out_md] [out_json]" >&2; exit 1; }
    @md_flag=""; if [ -n "{{out_md}}" ]; then md_flag="-out {{quote(out_md)}}"; fi; \
     json_flag=""; if [ -n "{{out_json}}" ]; then json_flag="-json {{quote(out_json)}}"; fi; \
     GOCACHE=/tmp/dango-gocache GOMODCACHE=/tmp/dango-gomodcache go run ./tools/analyze-tool-traces $md_flag $json_flag {{quote(jsonl)}}
