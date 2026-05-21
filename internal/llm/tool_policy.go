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
			ref:      capabilityRefForTool(tool.Name()),
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

func (t *policyTool) Name() string               { return t.base.Name() }
func (t *policyTool) Description() string        { return t.base.Description() }
func (t *policyTool) Parameters() map[string]any { return t.base.Parameters() }

func (t *policyTool) Execute(ctx context.Context, arguments string) (string, error) {
	policy := t.resolvePolicy()
	if policy != ExecPolicyPassby {
		toolpolicy.Record(ctx, toolpolicy.Decision{
			Capability: t.ref,
			Policy:     policy,
			Reason:     "capability policy",
		})
	}
	if policy == ExecPolicyOff {
		return "", &toolpolicy.DisabledError{
			Capability: t.ref,
			Reason:     "capability policy is off",
		}
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
