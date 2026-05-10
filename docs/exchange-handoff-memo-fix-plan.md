# Exchange / Handoff / Memo Fix Plan

## Purpose

Exchange, handoff, and memo are the message boundary between a skill's raw model/tool output and the runner stream bus. They should make runtime communication parseable and explicit by using one markdown front matter envelope per message.

The intended roles are:

- **Exchange**: public, runner-scoped messages in the shared exchange area. These are for orchestrator review, human inspection, and shared progress/reporting.
- **Handoff**: directed messages from one node to specific downstream nodes. These are delivered through downstream/upstream directories and drive successor execution.
- **Memo**: skill-private working notes in the skill workspace. These are owned by the skill and snapshotted by the runner for audit/replay, not sent to other skills as a directed handoff.

A document's `kind` front matter is the routing contract. Code that receives an upstream document should branch on `kind`; exchange code must not parse or unwrap handoff documents, and handoff code must not treat exchange documents as handoffs.

Use short kind names unless there is a concrete collision risk. Prefer `exchange`, `handoff`, and `memo` over `dango.exchange_doc`, `dango.handoff_doc`, and `dango.memo`. The markdown front matter is already scoped by the Dango runtime and schema version; the `dango.` prefix and `_doc` suffix add noise without adding routing information. If future interop requires namespacing, reintroduce it in one central kind definition rather than per document type.

## Current Problems

### 1. Executor output packaging has unclear ownership

`internal/engine/executor_channels.go` currently mixes unrelated responsibilities:

- executor stage entrypoints (`polishExchange`, `executeExchange`, `reportExchange`);
- built-in prompt rendering and prompt construction;
- conversion of stage output into `HandoffDoc` and `ExchangeDoc` markdown;
- filesystem writes to `outbox/handoff.md` and `exchange/*.md`;
- memo snapshot archiving;
- reverse-engineering runner workspace paths from `accessibleDirs`;
- formatting parent handoffs for prompts.

This makes the file name and function names misleading. In particular, `polishExchange`, `executeExchange`, and `reportExchange` return handoff markdown while also writing exchange files as side effects.

### 2. Exchange/handoff boundaries are blurred

The current PR workaround strips nested `HandoffDoc` front matter inside `exchangeDocMarkdown`. That removes duplicate front matter from generated exchange files, but it puts handoff parsing inside exchange generation.

That is the wrong boundary. If an exchange document body is a full handoff document, the upstream caller passed the wrong value. The fix should be at the stage output routing boundary, not by making exchange know about handoff.

### 3. Workspace paths are derived in the executor

The runner already provisions `Workspace` and `SkillWorkspace` values and can compute each skill's `memo`, `inbox`, `outbox`, `workspace`, and shared `exchange` directories. The executor should not infer these paths by inspecting `accessibleDirs` and walking parent directories.

This is brittle because `accessibleDirs` is an access-control/resource list, not a typed runtime context. It also forces executor code to know runner layout details.

The per-skill `workspace/` directory is currently the skill's general working directory: a scratch/project area for temporary files, generated glue code, downloads, and intermediate files that are neither private memo notes nor downstream artifacts. That purpose is valid, but the name is confusing because it nests `workspace/` under an already named runner workspace. Rename it to `scratch/`.

The `inbox/` and `outbox/` names are also generic mailbox terms. They do not describe graph direction. Rename them to `upstream/` and `downstream/` so the directory layout matches dataflow:

- `upstream/<node>/handoff.md` for directed parent inputs;
- `downstream/handoff.md` for this node's directed output;
- `downstream/artifacts/` for artifacts referenced by the directed handoff.

### 4. Prompt construction is too late and too entangled

Prompt rendering currently happens in executor stage methods. The prompt data includes workspace/accessibility details that should be part of a runner-provided runtime context at bind time or stage invocation time.

Prompt construction should be separated from message routing. At minimum, prompt rendering should live in a focused executor prompt file. Longer term, the skill binding/runtime context should provide stable instructions and paths before execution starts, not require stage output code to rediscover them.

### 4A. Header fields and kind definitions are scattered

Stream events, exchange documents, handoff documents, and memo snapshots each define their own header-like fields today: `kind`, `version`, `runner_id`, `node_id`, `skill_name`, `created_at`, `from_node`, `to_nodes`, `scope`, `source`, `metadata`, and similar values appear in multiple packages and shapes.

This makes the information-flow model harder to reason about. The system needs one central vocabulary for message kinds and one base header shape that all stream messages and markdown channel documents embed or mirror.

### 5. Memo output is not being produced in the observed run

The Honshu workspace shows empty `skills/*/memo/` directories and no memo snapshots under `archive/memo/**`. The code only snapshots memo files if the skill creates files under its `memo/` directory. The runner does not synthesize memo files from model reasoning or progress events.

The observed debug stream contains model reasoning mentioning "memo", but the tool calls write artifacts under `outbox/artifacts`, not memo files. Therefore the current run appears to have no memo because the skills did not write memo files. The prompt also only says "Include concise memo-like progress in prose", which encourages handoff body prose rather than actual writes to `memo/`.

The absence of memo files is primarily a generic prompt/instruction problem, not a per-skill `SKILL.md` problem. Memo is a universal workspace capability, so individual domain skills should not need to document it in their own skill instructions. The common runtime instructions should teach every bound skill that `memo/` exists, when to use it, and how it differs from handoff and exchange.

## Target Architecture

### Runner responsibilities

The runner should own runtime layout and message routing:

1. Provision one typed workspace context per node.
2. Bind each executor/skill with that context before execution.
3. Parse upstream documents by front matter `kind`.
4. Route documents by type:
  - `handoff` -> emit handoff events, resolve artifacts, deliver to successor upstream directories;
  - `exchange` -> publish exchange events and persist/read shared exchange entries;
  - `memo` -> parse archived memo snapshots only, not as downstream handoff input.
5. Snapshot skill-owned `memo/` files after stage/node boundaries and emit memo snapshot events when snapshots exist.
6. Own the workspace directory vocabulary and expose typed runtime paths to executors/skills instead of asking them to infer layout.

### Executor responsibilities

The executor should be a proxy/sandbox for one skill:

1. Hold a typed runtime context assigned by the runner.
2. Invoke the bound skill for polish/execute/report stages.
3. Require or normalize skill stage output into one explicit channel document when needed.
4. Return the primary directed handoff document to the runner.
5. Avoid inferring workspace paths or parsing exchange/handoff documents except at a clearly named stage-output normalization boundary.

### Skill responsibilities

A skill should produce explicit runtime messages:

1. Return a full `handoff` document when it is handing work/results to downstream nodes.
2. Return a full `exchange` document when it is publishing to the shared exchange.
3. Write private notes to `memo/` only when it needs durable private scratch state.
4. Put durable downstream artifacts in `downstream/artifacts` and reference them in handoff front matter.

Domain `SKILL.md` files should stay focused on domain behavior: capability, canonical scripts, input/output schemas, and task-specific constraints. They should not repeat generic channel mechanics such as how to use `memo/`, how to format handoff front matter, or which workspace directories exist. Those generic mechanics belong in the shared runtime prompt/instruction layer so all skills receive the same channel contract.

### Message header model

Define stream/document kinds in one package-level location and use them everywhere. The core kinds should start simple:

- `exchange`
- `handoff`
- `memo`
- status/progress event kinds such as runner, executor, tool, and LLM events

Introduce a base message header used by both stream events and markdown channel documents. Field names should stay stable across JSON and YAML where possible. The proposed shape is:

Required on every message:

- `kind`: central message kind, for example `exchange`, `handoff`, `memo`, `runner.phase`, `llm.reasoning.delta`, or `tool.call.started`.
- `version`: integer schema version for this message kind.
- `message_id`: globally unique message ID generated by the runtime or producer.
- `created_at`: producer timestamp in UTC RFC3339/RFC3339Nano form.
- `source`: producer identity.
- `scope`: correlation IDs.

`source` fields:

- `layer`: one of `orchestrator`, `runner`, `executor`, `skill`, `llm`, or `tool`.
- `id`: stable producer ID within the layer, such as runner ID, node ID, skill name, session ID, model name, or tool name.
- `parent_id`: optional immediate owner ID, such as a skill's node ID or an executor's runner ID.

`scope` fields:

- `request_id`: user request / orchestration request ID.
- `runner_id`: runner instance ID.
- `node_id`: plan node ID.
- `skill_name`: bound skill name when applicable.
- `session_id`: LLM conversation/session ID when applicable.

Optional on stream-carried messages:

- `sequence`: per-stream monotonic sequence number assigned by the stream.
- `logical_time`: stream-wide monotonic logical time assigned by the stream.
- `status`: lifecycle status for progress events (`pending`, `running`, `completed`, `failed`, `canceled`). Omit for pure document messages that are already complete.

Optional routing and indexing fields:

- `intent`: machine-readable routing intent such as `continue`, `review`, `summarize`, or `publish`.
- `title`: short human-readable summary for exchange/reporting views.
- `trace_id`: optional cross-process trace ID if external tracing is added later.
- `metadata`: small extensible key/value metadata, not long content and not fields that already belong in `source`, `scope`, or the message-specific body.

Specific message types then embed or contain this base header and add only their own fields:

- exchange: title plus markdown body/public document reference.
- handoff: `to_nodes`, artifact/resource references, body.
- memo: memo path, snapshot path, body.
- runner/executor status: phase, node ID, error summary.
- LLM/tool stream messages: delta payload, tool call IDs, result chunks.

Long content should not be duplicated across metadata and body. Metadata stays compact and indexable; exchange, handoff, memo, reasoning, and tool outputs carry long content in their own message bodies or delta payloads.

## Proposed Refactor Plan

### Phase 1: Correct the immediate exchange/handoff bug

- Remove `ParseHandoffMarkdown` from `exchangeDocMarkdown`.
- Ensure exchange file generation receives the raw stage body, not an already wrapped handoff document.
- Add tests that assert:
  - `downstream/handoff.md` contains exactly one `handoff` envelope;
  - `exchange/*.md` contains exactly one `exchange` envelope;
  - exchange generation does not know about or unwrap handoff documents.

### Phase 2: Introduce typed executor runtime paths

Add a small typed context owned by the runner, for example:

- `RunnerID`
- `NodeID`
- `SkillName`
- `MemoDir`
- `UpstreamDir`
- `DownstreamDir`
- `ScratchDir`
- `ExchangeDir`
- `ArchiveMemoDir`
- `AccessibleDirs`

Populate it from `runner.Workspace` when binding or before invoking an executor. Stop deriving `runnerID` and archive paths from `accessibleDirs`.

Rename directory fields as part of this context migration:

- `InboxDir` -> `UpstreamDir`
- `OutboxDir` -> `DownstreamDir`
- `WorkingDir` -> `ScratchDir`

For in-progress branch code, update call sites directly instead of adding compatibility wrappers. If preserving old on-disk artifacts matters for existing persisted runs, add an explicit migration/read fallback at the runner persistence boundary rather than leaking old names through the executor API.

### Phase 2A: Centralize message kinds and headers

- Create one central kind definition location, preferably in the stream/message package that owns runtime information flow.
- Rename markdown channel kinds to shorter values:
  - `dango.exchange_doc` -> `exchange`
  - `dango.handoff_doc` -> `handoff`
  - `dango.memo` -> `memo`
- Add one base header struct for shared fields and embed it in exchange, handoff, memo, and stream-event payload/message definitions where appropriate.
- Keep a deliberate compatibility decision: either reject old kind names after this in-branch refactor, or accept old names only in parsers for reading already persisted artifacts. Do not emit old names.
- Update tests to assert all emitted markdown documents use the centralized kind constants.

### Phase 3: Split `executor_channels.go`

Replace the current catch-all file with focused files:

- `executor_stage.go`: `Polish`, `Execute`, `Report` stage orchestration and calls into runtime skill.
- `executor_stage_output.go`: stage output normalization and channel document construction.
- `executor_prompt.go`: prompt renderer setup and `polishPrompt`, `executionPrompt`, `reportPrompt`.
- `executor_workspace.go`: typed runtime workspace context and memo snapshot helpers, until memo snapshot ownership moves fully to runner.

Do not add adapter layers for old private function names; update call sites directly.

### Phase 4: Move routing decisions to runner kind parsing

Create one focused parser/routing function for channel documents, for example a small internal result type with kind-specific payloads. The runner should parse front matter once and branch on `kind` rather than trying `ParseHandoffMarkdown` then `ParseExchangeDocMarkdown` in multiple places.

Expected behavior:

- Handoff outputs emit `handoff.emitted`, artifact events, and delivery events.
- Exchange outputs emit `exchange.published` and stay in the shared exchange area.
- Invalid or unknown `kind` values fail or are ignored according to stage contract, but are not silently rewrapped as another channel type.

### Phase 5: Make memo behavior explicit

- Update the shared runtime instructions to say that memo means writing files under the provided `memo/` directory, not just writing memo-like prose in handoff bodies.
- Add a test skill or fixture that writes `memo/plan.md` during execution and verifies:
  - the file remains in the skill's `memo/` directory;
  - the runner snapshots it under `archive/memo/<node>/<stage>/...`;
  - a memo snapshot stream event is emitted when snapshots exist.
- Keep memo optional: no memo file means no memo snapshot event.

### Phase 5A: Refine the shared prompt/instruction system

Memo, handoff, exchange, and workspace directory usage should be taught by shared runtime instructions that are injected for every skill binding. They should not be duplicated in each domain `SKILL.md`.

The shared instruction layer should include a concise "Workspace channel contract" section:

- `memo/` is private durable scratch for the current skill/node. Use it for plans, assumptions, data-quality notes, model notes, failed attempts, or other information useful across polish/execute/report turns. It is not delivered to downstream skills.
- `downstream/handoff.md` is the directed downstream message. Return or write exactly one handoff document when a stage needs to pass results to downstream nodes or the orchestrator.
- `downstream/artifacts/` stores durable files referenced by handoff front matter.
- `upstream/<node>/handoff.md` contains directed upstream input from a parent node. Read parent handoffs from the prompt or upstream directory, not from the shared exchange unless the task explicitly asks for shared public context.
- `scratch/` is private working space for temporary glue code and intermediate files that are not memo notes and not downstream artifacts.
- `exchange/` is runner-scoped shared public context. Publish exchange documents for public progress/reporting, not for directed downstream delivery.

The instruction layer should also include decision guidance:

- Do not write memo files for every trivial task.
- Write memo files when the task has non-obvious assumptions, complex field mapping, data-quality concerns, long tool workflows, retry/failure information, model assumptions, or decisions that should survive context loss.
- Prefer stable names such as `memo/plan.md`, `memo/data_quality.md`, `memo/model_notes.md`, and `memo/tool_runs.md`.
- Keep handoff bodies focused on downstream-readable results. If a memo exists, the handoff may briefly mention it for auditability, but downstream correctness must not depend on reading memo files.

Prompt plumbing changes:

- Move the workspace channel contract out of domain `SKILL.md` files and into the common skill runtime/system instructions.
- Include typed workspace paths in the prompt data from runner-owned runtime context, not by asking executor prompt code to rediscover paths from `accessibleDirs`.
- Rename prompt fields so they reflect channel semantics: use `ParentHandoffs` for directed input, `ExchangeContext` only for shared public context, and `MemoDir`/`DownstreamDir`/`ArtifactsDir`/`ScratchDir` for writable locations.
- Remove wording such as "memo-like progress in prose" from `execute.tmpl`; replace it with explicit memo-file guidance.
- Ensure polish prompts remain no-tool/no-file when the phase is a pure feasibility review; memo writing should be available in execute/report only when tools and workspace writes are allowed.

Tests for this phase should assert that rendered generic prompts mention `memo/` as a writable private workspace channel and do not require individual domain `SKILL.md` files to document memo usage.

### Phase 6: Align the Honshu example vocabulary

The example docs and skill prompts still use older wording such as "exchange markdown" when they often mean upstream handoff markdown. Update them to say:

- parent handoff for directed upstream input;
- shared exchange for public progress/reporting;
- memo for skill-private notes.

## Acceptance Criteria

- No generated exchange file contains nested handoff front matter.
- No generated handoff file contains nested exchange front matter.
- New generated markdown kinds are `exchange`, `handoff`, and `memo` from centralized constants.
- Runner routing decisions are based on document `kind`.
- Executor code no longer derives workspace structure from `accessibleDirs`.
- Per-skill runtime directories use directional names (`upstream`, `downstream`) and `scratch` instead of nested `workspace`.
- Stream events and markdown channel documents share a central base header model rather than duplicating ad-hoc header fields.
- Prompt code, workspace code, stage entrypoints, and document packaging are in separate focused files.
- Memo snapshots are produced only when skills actually write memo files, and tests cover both memo-present and memo-absent cases.
- Honshu example artifacts/tests use handoff/exchange/memo terminology consistently.
