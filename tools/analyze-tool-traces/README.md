# analyze-tool-traces

A developer utility that summarizes the stream-event JSON-lines log a dango
runner writes to `artifacts/debug/stream_events.jsonl`. It produces the
data shapes the post-alpha hardening phase relies on to make
evidence-driven decisions about builtin-tool design — bash command-head
distribution, captured inner bodies for Turing-complete heads (`python -c`
/ `bash -c` / `sh -c` / `awk` / `make` / `xargs`), per-skill tool-call
tallies, and curl/wget URL frequencies.

This tool is **not** part of the dango runtime. It is a separate Go
binary under `tools/` that consumes runner artifacts post-hoc. It is the
canonical replacement for the hand-rolled `jq`+`awk` pipeline recorded
in `docs/builtin-tools-restructure-plan.md` (PR C-3).

## Why it exists

`docs/builtin-tools-restructure-plan.md` and
`docs/near_term_plan/60-instrumentation.md` require new wrapper-tool
proposals to clear a `≥5 occurrences in one honshu run` threshold before
a wrapper PR is scheduled. This binary is the analyzer that produces
the numbers behind that threshold — it makes the methodology
reproducible and machine-checkable, so a "we should add a `sed_in_place`
wrapper" claim can be substantiated against a real trace rather than
gut feel.

## Usage

The repository entry point goes through `just`:

```sh
# Print the markdown report to stdout.
just analyze-traces artifacts/debug/stream_events.jsonl

# Write the markdown report to a file.
just analyze-traces artifacts/debug/stream_events.jsonl report.md

# Also write a JSON sidecar with the full report structure.
just analyze-traces artifacts/debug/stream_events.jsonl report.md report.json
```

Direct invocation works too:

```sh
go run ./tools/analyze-tool-traces \
    -out report.md \
    -json report.json \
    artifacts/debug/stream_events.jsonl
```

### Flags

| Flag | Purpose |
| --- | --- |
| `-out <path>` | Write the markdown report to `<path>` (default: stdout). |
| `-json <path>` | Also write the full `Report` struct as indented JSON. |

The single positional argument is the path to a JSON-lines stream-event
log (typically `artifacts/debug/stream_events.jsonl` under a runner's
artifacts directory).

## What gets reported

The report is built from events with `event_type ==
"llm.tool_call.started"`. Filtering by event type (rather than the
`metadata.category == "audit"` tag introduced in PR #97 / subtask 60a)
keeps the analyzer compatible with traces captured before the tag
landed; the `AuditEvents` field separately reports how many events did
carry the audit tag, so callers can tell how audit-grade a given trace
is.

| Report field | Meaning |
| --- | --- |
| `TotalEvents` | Lines parsed from the input, including non-tool-call events. |
| `AuditEvents` | Subset with `metadata.category == "audit"`. |
| `ToolCallStarted` | Subset matching `event_type == "llm.tool_call.started"`. The denominator for the breakdowns below. |
| `BashCalls` | Tool calls whose tool name is `bash`. |
| `BashHeads` | Per-head occurrence count for the first non-flag word of each `bash` call's command (`grep` / `git` / `python` / `make` / …). |
| `InnerBodies` | For Turing-complete heads (`python`, `python3`, `bash`, `sh`, `awk`, `make`, `xargs`), the captured `-c`-style inline program bodies so a reviewer can see what's actually being run. |
| `PerSkillTallies` | Tool-call counts grouped by `metadata.skill_name`. |
| `CurlCalls` / `WgetCalls` | Counts of `curl` / `wget` invocations across all bash calls. |
| `URLsByHost` | Frequency of each hostname appearing in those `curl` / `wget` calls. |

Lines that fail to parse are counted toward `TotalEvents` and otherwise
ignored. A partial / interrupted trace still produces a useful summary.

## When to run it

- Before proposing a new wrapper tool. Run a honshu (or other
  representative) flow with `DANGO_DEBUG_STREAM_DUMP=1` or whatever
  capture is in scope, then point this tool at the resulting `.jsonl`.
  Decide on wrapper additions based on what crosses the `≥5
  occurrences` bar.
- After landing a bash-allowlist or url-allowlist change, to sanity-check
  the new policy against an existing trace corpus.
- Periodically against `examples/honshu_groundwater` runs, to track
  drift in agent tool-use shape over time.

## Source layout

```
tools/analyze-tool-traces/
   main.go            CLI entry point; flag parsing + I/O.
   analyzer.go        Report struct + Analyze(io.Reader) + FormatMarkdown.
   analyzer_test.go   Coverage for parsing, accumulation, markdown shape.
```

The `Analyze` and `FormatMarkdown` functions are package-private
(`package main`) on purpose — the only intended caller is this binary.
The `Report` struct is JSON-marshallable so the `-json` sidecar is
stable for downstream programmatic consumers.
