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

### 4. Template-based prompt construction is too late and too entangled

Prompt rendering currently happens in executor stage methods. The prompt data includes workspace/accessibility details that should be part of a runner-provided runtime context at bind time or stage invocation time.

The bigger issue is that the executor currently assembles a mostly complete LLM request from stage-specific templates and injected handoff/exchange content. That keeps the request shape explicit and may reduce turns, but it also hard-codes one interpretation path for upstream inputs. When parent handoffs vary in structure or when a task needs a longer tool-driven investigation, the fixed template becomes a constraint instead of a runtime aid.

Prompt construction should be separated from message routing. Short term, prompt code should live in a focused executor prompt file. Longer term, the runtime should stop treating built-in prompts as filled request templates and instead provide agentic built-in instructions that teach the skill how Dango works, which tools and workspace channels exist, and how to inspect upstream exchange/handoff context for itself.

### 4A. Header fields and kind definitions are scattered

Stream events, exchange documents, handoff documents, and memo snapshots each define their own header-like fields today: `kind`, `version`, `runner_id`, `node_id`, `skill_name`, `created_at`, `from_node`, `to_nodes`, `scope`, `source`, `metadata`, and similar values appear in multiple packages and shapes.

This makes the information-flow model harder to reason about. The system needs one central vocabulary for message kinds and one base header shape that all stream messages and markdown channel documents embed or mirror.

### 5. Memo output is not being produced in the observed run

The Honshu workspace shows empty `skills/*/memo/` directories and no memo snapshots under `archive/memo/**`. The code only snapshots memo files if the skill creates files under its `memo/` directory. The runner does not synthesize memo files from model reasoning or progress events.

The observed debug stream contains model reasoning mentioning "memo", but the tool calls write artifacts under `outbox/artifacts`, not memo files. Therefore the current run appears to have no memo because the skills did not write memo files. The prompt also only says "Include concise memo-like progress in prose", which encourages handoff body prose rather than actual writes to `memo/`.

The absence of memo files is primarily a generic prompt/instruction problem, not a per-skill `SKILL.md` problem. Memo is a universal workspace capability, so individual domain skills should not need to document it in their own skill instructions. The common runtime instructions should teach every bound skill that `memo/` exists, when to use it, and how it differs from handoff and exchange.

### 6. Terminal renderer creates a second, misleading exchange directory

The Honshu example currently configures `streamrender` with `ExchangeDir = artifacts/exchanges`. That directory is outside the runner persistence workspace and is separate from the canonical runner exchange directory under `artifacts/persistence/workspace/task_<runner>/exchange`.

The observed file `artifacts/exchanges/exchange-000000001912.md` is not a normal runner exchange entry. It is the orchestrator planning handoff captured from an `llm.output.delta` event because the renderer treats any channel-looking markdown as an exchange reference. The result is misleading in three ways:

- the file is named `exchange-*` even though its front matter is `handoff`;
- it lives outside the runner's canonical exchange directory;
- it mixes terminal UI/debug capture concerns with durable runtime message storage.

This is a real bug, but `streamrender` is slated for a larger independent refactor so it can leave `internal` and become the foundation for future command terminal UI work. The immediate fix should therefore be minimal: stop the Honshu example from creating a separate `artifacts/exchanges` store and prevent the renderer from labeling arbitrary channel markdown as canonical exchange storage. Deeper renderer architecture changes belong in the deferred `streamrender` refactor.

### 7. Handoff bodies duplicate large artifact data

The shared executor instructions currently encourage execute-stage handoff bodies to contain structured downstream output and explicitly suggest a fenced JSON block. In the observed Honshu run, the skill had already written `enriched_observations.json` as a handoff artifact and referenced it in front matter, but it also copied the full JSON payload into the handoff body. Because executor stage packaging writes the same stage body into the exchange entry, the canonical exchange file also received a huge duplicated data block.

This violates the channel contract: durable data belongs in `downstream/artifacts`, and handoff bodies should carry concise recipient-facing guidance, schema notes, counts, quality notes, and artifact references. They should not inline large data blocks, long examples, or generated code when those bytes are already stored as artifacts.

The immediate fix should update the shared runtime prompt/instructions to prohibit large fenced data/code blocks in handoff bodies and to require artifact references for large outputs. A follow-up implementation guard should reject or compact handoff bodies that appear to inline large JSON/code payloads despite having declared artifacts, so malformed model output does not silently pollute handoff and exchange storage.

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

The shared runtime prompt/instruction layer should teach skills how to operate in Dango, not pre-solve each stage by assembling one filled prompt from upstream content. Skills should be able to inspect exchange/handoff artifacts agentically through tools, use memo when the task spans multiple steps, and decide how much upstream context is relevant instead of inheriting one template-defined reading path.

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

### Phase 3A: Replace template-built prompts with agentic built-in instructions

After the executor code is structurally split, refactor the built-in prompt system away from stage-specific template filling and toward markdown instructions that teach runtime behavior.

- Stop treating `polish.tmpl`, `execute.tmpl`, and `report.tmpl` as the long-term execution contract. They may remain temporarily during migration, but the target is to remove template-filled stage prompts as the primary way skills receive work.
- Replace the renderer/template package with versioned markdown built-in instructions and small stage notes that explain:
  - the role of the current stage (`polish`, `execute`, `report`);
  - the Dango runtime workflow and the skill's responsibility within it;
  - available tools and when to use them;
  - the workspace channel contract for `memo/`, `upstream/`, `downstream/`, `downstream/artifacts/`, `scratch/`, and `exchange/`;
  - when to inspect upstream handoff/exchange content directly with tools rather than relying on pre-injected summaries;
  - when to create memo files for long call chains, planning, failed attempts, data-quality concerns, and decisions that should survive context loss.
- Change executor stage invocation so it passes only the minimal stage objective plus stable runtime context. Do not pre-compose a fully interpreted request by copying upstream handoff/exchange content into a fixed template whenever the skill can read the source material itself.
- Keep the built-in instructions markdown-first and readable as runtime policy docs. Avoid introducing a new structured prompt DSL or another layer of stage-specific Go template data structs as the replacement.
- Ensure the migration preserves the current tool-access boundary: the skill should inspect only the files and directories that the runner intentionally exposes through runtime context and accessible dirs.
- Define an explicit conversation bootstrap order for every new skill conversation:
  1. load shared built-in instructions first, so the skill learns the Dango workflow, stage model, tool usage, memo guidance, and channel semantics;
  2. load the skill's own `SKILL.md`, so the skill learns its domain capability, scripts, schemas, and task-specific operating guidance;
  3. provide references to currently relevant exchange files, including their locations and front matter summaries, so the skill knows what shared public context exists and can decide which files to inspect;
  4. provide the concrete task description for the current stage;
  5. when a directed upstream handoff exists, provide its location and basic metadata so the skill can inspect it directly and decide how to use it.
- Keep exchange and handoff references lightweight at bootstrap time. The runtime should tell the skill where these documents are and what they are, but should avoid pre-interpreting all of their contents into one filled request.
- Add tests that assert the shared built-in instructions:
  - describe the runtime workflow and channel contract;
  - tell skills to use tools to inspect upstream handoff/exchange inputs when needed;
  - explain memo usage as file writes under `memo/`, not memo-like prose in handoff bodies;
  - do not require each domain `SKILL.md` to restate the generic Dango workflow.

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
- `upstream/<node>/handoff.md` contains directed upstream input from a parent node. Read parent handoffs from runner-exposed upstream context, not from the shared exchange unless the task explicitly asks for shared public context.
- `scratch/` is private working space for temporary glue code and intermediate files that are not memo notes and not downstream artifacts.
- `exchange/` is runner-scoped shared public context. Publish exchange documents for public progress/reporting, not for directed downstream delivery.

The instruction layer should also include decision guidance:

- Do not write memo files for every trivial task.
- Write memo files when the task has non-obvious assumptions, complex field mapping, data-quality concerns, long tool workflows, retry/failure information, model assumptions, or decisions that should survive context loss.
- Prefer stable names such as `memo/plan.md`, `memo/data_quality.md`, `memo/model_notes.md`, and `memo/tool_runs.md`.
- Keep handoff bodies focused on downstream-readable results. If a memo exists, the handoff may briefly mention it for auditability, but downstream correctness must not depend on reading memo files.
- Keep large data and generated files out of handoff bodies. Store them under `downstream/artifacts/`, list them in handoff front matter, and describe the schema, row counts, caveats, and intended downstream use in short prose.
- Do not inline large fenced `json`, `csv`, source-code, or model-output blocks in handoff bodies when the content is available as a declared artifact. Small snippets are acceptable only when they clarify schema or usage and are not the data payload itself.

The instruction layer should also make the context loading order explicit:

1. shared built-in instructions;
2. bound skill `SKILL.md`;
3. exchange file references and front matter summaries for shared public context;
4. the concrete stage task;
5. optional handoff location and metadata for directed upstream input.

This ordering ensures the skill first understands how to operate inside Dango, then understands its own domain capability, then learns which runtime artifacts are available for inspection, and only then receives the specific work it must complete.

Instruction-set follow-up changes:

- Move the workspace channel contract out of domain `SKILL.md` files and into the common skill runtime/system instructions.
- Pass typed workspace paths and stage identity as runtime context, but avoid making prompt-data structs the semantic contract for how a skill consumes upstream work.
- Replace template-specific wording such as "memo-like progress in prose" and "prefer fenced JSON payloads" with markdown instruction text that teaches artifact-first and memo-file behavior.
- Keep stage notes minimal: they should state the objective and constraints of the stage, not pre-read all upstream context on the skill's behalf.
- Expose exchange and handoff documents as inspectable references with stable paths and concise metadata, not as eagerly flattened prose payloads.
- Ensure polish guidance remains lightweight when the phase is a pure feasibility review; memo writing should be available in execute/report only when tools and workspace writes are allowed.

### Phase 5B: Add handoff body size and artifact-reference safeguards

- Add stage-output validation or normalization that detects large fenced JSON/code/data blocks in handoff bodies, especially when the handoff already declares artifacts.
- Prefer failing the malformed stage output with a clear error or compacting it into a short artifact-reference summary. Do not silently duplicate large artifact payloads into exchange entries.
- Keep the check narrow and explainable: it should target obvious large blocks, not normal short markdown summaries.
- Add tests covering a handoff with a declared artifact plus a large fenced JSON body and assert the system does not write that large payload into the exchange document.

### Phase 5C: Minimal streamrender exchange capture fix

- Remove the Honshu example's separate `artifacts/exchanges` renderer directory, or point any UI-only exchange references at canonical runner-persistence exchange paths when those paths are available.
- In `streamrender`, avoid naming captured channel markdown `exchange-*` unless the payload is actually an exchange document. Handoff-looking markdown should not be represented as a canonical exchange file.
- Keep this fix intentionally small. Do not reorganize renderer APIs, package boundaries, or terminal UI architecture in this PR; those belong to the deferred streamrender extraction/refactor.
- Update Honshu tests so they assert canonical exchange files under `artifacts/persistence/workspace/task_<runner>/exchange` rather than the old outer `artifacts/exchanges` directory.

Tests for this phase should assert that shared built-in instructions mention `memo/` as a writable private workspace channel, tell the skill to inspect upstream handoffs/exchange context with tools when needed, and do not require individual domain `SKILL.md` files to document generic Dango workflow usage.

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
- Built-in executor instructions are markdown workflow guidance rather than filled prompt templates that pre-interpret upstream handoff/exchange content.
- Memo snapshots are produced only when skills actually write memo files, and tests cover both memo-present and memo-absent cases.
- Honshu example artifacts/tests use handoff/exchange/memo terminology consistently.
- The Honshu example does not create a second outer `artifacts/exchanges` directory for renderer-captured channel markdown.
- Handoff bodies do not duplicate large artifact payloads; large JSON/data outputs are stored in `downstream/artifacts` and referenced from front matter plus short prose.
