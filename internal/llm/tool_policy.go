package llm

import (
	"context"

	"github.com/tsumina/dango/internal/llm/internal/toolpolicy"
)

type policyTool struct {
	base     Tool
	ref      CapabilityRef
	policies map[CapabilityRef]ExecPolicy
}

func wrapToolsWithPolicySet(tools []Tool, cfg ToolSetConfig) []Tool {
	if len(tools) == 0 {
		return nil
	}
	policies := toolpolicy.ClonePolicyMap(cfg.Policies)
	out := make([]Tool, len(tools))
	for i, tool := range tools {
		if tool == nil {
			out[i] = nil
			continue
		}
		out[i] = &policyTool{
			base:     tool,
			ref:      capabilityRefForUnwrapped(tool),
			policies: policies,
		}
	}
	return out
}

func capabilityRefForTool(name string) CapabilityRef {
	if isBuiltinToolName(name) {
		if _, err := ParseExtraTool(name); err == nil {
			return ExtraCapability(name)
		}
		return BuiltinCapability(name)
	}
	return ToolCapability(name)
}

// capabilityRefForUnwrapped picks the policy key for tool, recognizing
// MCP-backed tools so they land under CapabilityMCPTool instead of the
// generic CapabilityTool bucket. Built-in extras / core tools still use
// [capabilityRefForTool].
func capabilityRefForUnwrapped(tool Tool) CapabilityRef {
	if tool == nil {
		return CapabilityRef{}
	}
	if m, ok := tool.(mcpToolMarker); ok && m.mcpToolName() != "" {
		return MCPCapability(tool.Name())
	}
	return capabilityRefForTool(tool.Name())
}

func (t *policyTool) Name() string               { return t.base.Name() }
func (t *policyTool) Description() string        { return t.base.Description() }
func (t *policyTool) Parameters() map[string]any { return t.base.Parameters() }

// mcpServerName / mcpToolName forward through the policy wrapper so the
// conversation can still detect an MCP-backed tool after policy wrapping.
func (t *policyTool) mcpServerName() string {
	if m, ok := t.base.(mcpToolMarker); ok {
		return m.mcpServerName()
	}
	return ""
}

func (t *policyTool) mcpToolName() string {
	if m, ok := t.base.(mcpToolMarker); ok {
		return m.mcpToolName()
	}
	return ""
}

func (t *policyTool) Execute(ctx context.Context, arguments string) (string, error) {
	policy := t.resolvePolicy()
	decision := toolpolicy.Decision{
		Capability: t.ref,
		Policy:     policy,
		Reason:     "capability policy",
	}
	if policy == ExecPolicyOff {
		toolpolicy.Record(ctx, decision)
		return "", &toolpolicy.DisabledError{
			Capability: t.ref,
			Reason:     "capability policy is off",
		}
	}
	if policy == ExecPolicyNeedApprove {
		resp, err := toolpolicy.RequestApproval(ctx, toolpolicy.ApprovalRequest{
			Capability: t.ref,
			Policy:     policy,
			Reason:     decision.Reason,
		})
		decision.ApprovalOutcome = resp.Outcome
		decision.ApprovalReason = resp.Reason
		toolpolicy.Record(ctx, decision)
		if err != nil {
			return "", err
		}
		if resp.Outcome == toolpolicy.ApprovalOutcomeApproveForSession {
			t.policies[t.ref] = ExecPolicyPassby
		}
		return t.base.Execute(ctx, arguments)
	}
	if policy != ExecPolicyPassby {
		toolpolicy.Record(ctx, decision)
	}
	return t.base.Execute(ctx, arguments)
}

func (t *policyTool) resolvePolicy() ExecPolicy {
	if len(t.policies) == 0 {
		return ExecPolicyPassby
	}
	for _, ref := range []CapabilityRef{
		t.ref,
		ToolCapability(t.base.Name()),
		{Name: t.base.Name()},
	} {
		if policy, ok := t.policies[ref]; ok {
			return policy.Default()
		}
	}
	return ExecPolicyPassby
}
