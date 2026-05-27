package engine

import (
	"fmt"
	"log/slog"

	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/mcpclient"
)

// AddMCPServers registers servers as orchestrator-wide ("global") MCP
// servers visible to every skill the orchestrator hosts. Each server's tools
// are appended to every skill registered later (and to skills already
// registered through subsequent AddSkills calls, since AddSkills snapshots
// the current global set at registration time).
//
// Caller-supplied [mcpclient.Server] handles must already be started; the
// orchestrator does not spawn them. The orchestrator keeps references for
// the duration of its lifetime so [Orchestrator.Close] can shut them down
// in one call. AddMCPServers logs one INFO line per registered server and
// one WARN risk notice describing that user-supplied MCP servers run as
// external processes with host privileges (see
// docs/mcp-support-plan.md §5).
//
// Like the other startup-only orchestrator settings, AddMCPServers must be
// called before the first planning call. Duplicate server names (across
// calls or within one call) are rejected.
func (o *Orchestrator) AddMCPServers(servers ...*mcpclient.Server) error {
	if len(servers) == 0 {
		return nil
	}
	for i, srv := range servers {
		if srv == nil {
			return fmt.Errorf("orchestrate: AddMCPServers entry %d is nil", i)
		}
		if srv.Name() == "" {
			return fmt.Errorf("orchestrate: AddMCPServers entry %d has empty server name", i)
		}
	}

	o.mu.Lock()
	if o.configLocked {
		o.mu.Unlock()
		return fmt.Errorf("orchestrate: AddMCPServers can only be called during startup")
	}
	seenInBatch := make(map[string]struct{}, len(servers))
	for _, srv := range servers {
		name := srv.Name()
		if _, dup := seenInBatch[name]; dup {
			o.mu.Unlock()
			return fmt.Errorf("orchestrate: MCP server %q provided twice in one AddMCPServers call", name)
		}
		if _, dup := o.mcpServerByName[name]; dup {
			o.mu.Unlock()
			return fmt.Errorf("orchestrate: MCP server %q is already registered", name)
		}
		seenInBatch[name] = struct{}{}
	}
	for _, srv := range servers {
		o.globalMCPServers = append(o.globalMCPServers, srv)
		o.mcpServerByName[srv.Name()] = srv
	}
	logger := o.logger
	o.mu.Unlock()

	for _, srv := range servers {
		logger.Info("orchestrate: registered MCP server",
			slog.String("server", srv.Name()),
			slog.Int("tools", len(srv.Tools())),
		)
	}
	logger.Warn("orchestrate: user-supplied MCP servers run as external processes with host privileges; risk is the user's to own")
	return nil
}

// MCPServers returns a snapshot of the orchestrator-wide MCP server handles
// registered through [Orchestrator.AddMCPServers]. The returned slice is safe
// for the caller to mutate.
func (o *Orchestrator) MCPServers() []*mcpclient.Server {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if len(o.globalMCPServers) == 0 {
		return nil
	}
	return append([]*mcpclient.Server(nil), o.globalMCPServers...)
}

// Close shuts down every MCP server the orchestrator is holding so the
// embedding app/cmd has a single call to make at process exit.
//
// Close is intended for process shutdown after every in-flight request has
// settled. It does not stop runners and does not flip the configuration
// lock, so a later [Orchestrator.AddMCPServers] call would succeed and
// register fresh servers; the contract "safe to call multiple times" is
// then only true if the caller has not registered new servers since the
// previous Close. Calling Close twice without any intervening registration
// is a no-op as documented.
func (o *Orchestrator) Close() error {
	o.mu.Lock()
	servers := o.globalMCPServers
	o.globalMCPServers = nil
	o.mcpServerByName = make(map[string]*mcpclient.Server)
	o.mu.Unlock()

	var firstErr error
	for _, srv := range servers {
		if err := srv.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// collectMCPTools flattens the global and per-skill MCP server lists into
// the [llm.Tool] adapters consumed by [llm.Skill.AddTools]. Servers are
// expanded in order; per-skill servers come after globals so users can rely
// on a deterministic registration order if they ever need to disambiguate
// otherwise-identical tool names by registration source.
func collectMCPTools(global, perSkill []*mcpclient.Server) []llm.Tool {
	if len(global) == 0 && len(perSkill) == 0 {
		return nil
	}
	out := make([]llm.Tool, 0, (len(global)+len(perSkill))*2)
	for _, srv := range global {
		if srv == nil {
			continue
		}
		out = append(out, llm.MCPTools(srv)...)
	}
	for _, srv := range perSkill {
		if srv == nil {
			continue
		}
		out = append(out, llm.MCPTools(srv)...)
	}
	return out
}
