# 53 — MCP Config and Visibility (code)

Kind: code. Third of the three MCP implementation subtasks split from `50`.

**Prerequisite.** `52` merged.

## Goal

Apply the global vs per-skill visibility rule from `50` at the
orchestrator level. App/cmd entry points install global MCP servers once;
the orchestrator augments every registered skill with global MCP tools
and, when the user mounts a skill with per-skill MCP servers, augments
that specific skill with only those server's tools.

## Scope

1. `internal/engine/orchestrator.go`:
   - `Orchestrator.AddMCPServers(handles ...*llm.MCPServer) error` — store
     handles in the orchestrator and log one INFO line per server plus a
     single WARN risk notice. Reject duplicate server names.
   - On every `AddSkills` call, append the global MCP tools to each new
     skill (via `Skill.AddTools`).
   - On `AddSkills` with `SkillRegistration.MCPServers` non-empty, append
     those handles' tools to the one skill they were registered with.
   - `Orchestrator.MCPServers()` snapshot accessor.
   - `Orchestrator.Close()` shuts down every MCP server the orchestrator
     started so app/cmd has a single shutdown call.
2. New `MCPServers []*llm.MCPServer` field on `SkillRegistration`.

## Tests

- `TestOrchestratorRegistersGlobalMCPTools`.
- `TestOrchestratorPerSkillMCPVisibility` — per-skill MCP tools only
  appear on the registered skill, not on others.
- `TestOrchestratorDuplicateMCPServerNameRejected`.
- `TestOrchestratorCloseShutsDownMCPServers`.

## Out of scope

- HTTP transport (still deferred).
- Recommended-server curation (deferred to app/cmd cycle per `50`).

## Verifiable acceptance

- `go test ./internal/engine/...` green.
- Global MCP tools appear on every registered skill; per-skill tools
  appear only on the skill that declared them; orchestrator shutdown
  closes every server.
