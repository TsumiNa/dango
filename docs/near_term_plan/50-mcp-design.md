# 50 — MCP Support (design)

Kind: design doc. Produces `docs/mcp-support-plan.md` and adds the MCP
implementation files to this folder once the design lands. No
implementation ships in this subtask.

**Scale note.** MCP is a cycle-magnitude effort — a full external
integration surface (transport, lifecycle, adapter, config,
visibility, stream events), likely larger than every other near-term
subtask combined. It is design-first here; whether its implementation
joins the current batch or becomes its own cycle is decided *after*
this design lands, not assumed now.

**Prerequisite.** `10` accepted (MCP availability and execution policy
reuse that model). Implementation also assumes `11`/`12a` are merged so
MCP tools register through the same config surface.

## Goal

Wire dango as an MCP client so the four-class tool architecture
(coverage memo § 0.2) becomes real. MCP is how dango consumes
narrow, well-defined external tools (`web_search`, paper fetch, etc.)
without reimplementing them.

## Confirmed product decisions (input to the design)

These are settled; the design doc must honor them.

- **Global vs per-skill visibility.** MCP servers configured at app/cmd
  startup are **global** — visible to every skill. A user mounting
  their own skills may additionally declare **per-skill** MCP servers
  visible only to that skill.
- **Stream semantics.** MCP tool *results* already surface in the
  exchange / memo / handoff documents, so results are **not** written
  into the event stream. MCP tool *call* events **are** published to
  the stream so the top-level caller is aware a call happened. Design
  the call-event payload (server, tool, argument summary, outcome
  status) without the full result body.
- **Security.** MCP servers run as external processes with the host's
  privileges; risk for user-supplied servers is the user's to own.
  Dango does not sandbox them. At startup the runtime lists the mounted
  MCP servers and prints a one-line risk notice. Default execution
  policy is `passby`; users opt into `need_approve` or `off` per the
  `10` model.
- **Startup listing.** For now, simply list all mounted MCP servers at
  startup. Curating a recommended server set is deferred to the app/cmd
  cycle; do not block on it.

## Questions the design doc must answer

1. **Library / transport.** Survey Go MCP client libraries; pick one
   (or justify a thin in-house client). Pin the version. Decide stdio
   vs HTTP (or both) and state why.
2. **Lifecycle.** Who spawns/stops a server; how the orchestrator and
   runner observe a server crash; per-call timeout; reconnection
   policy; behavior when a server adds/removes tools mid-session.
3. **Tool naming.** Convention for namespacing MCP tools and resolving
   collisions with Go builtins and with other MCP servers (relate to
   the `40` alias model where sensible).
4. **Schema and result handling.** Argument-schema forwarding to the
   LLM; result-to-string conversion; truncation policy mirroring the
   bash 16 KiB cap.
5. **Config shape.** How global and per-skill servers are declared;
   how the `10` availability/policy lists reference MCP servers and
   tools; how `SkillConfig` carries per-skill servers.
6. **Call-event contract.** Exact stream event for an MCP call per the
   "results out, calls in" decision above.
7. **Implementation split.** Propose the MCP implementation files to
   add to this folder (e.g. `51-mcp-client.md`, `52-mcp-adapter.md`,
   `53-mcp-config-visibility.md`), each independently verifiable.

## Honshu observation (at implementation, not at design)

The "results in exchange/memo/handoff, calls on the stream" split is
exactly a what-to-surface / what-to-hide question that honshu exists to
answer. When the implementation lands, observe via honshu whether the
top-level caller sees MCP activity at the right granularity — enough to
know a call happened and to whom, without the result body flooding the
stream. Record adjustments to the call-event payload. UX signal, not a
gate.

## Verifiable acceptance

`docs/mcp-support-plan.md` exists and answers all seven questions, the
implementation files are appended to this folder, and the call-event
contract and visibility rules match the confirmed decisions above.
