package toolpolicy

import (
	"context"
	"fmt"
)

// ExecPolicy controls whether a capability runs immediately, is recorded for a
// future approval flow, or is disabled.
type ExecPolicy string

const (
	ExecPolicyPassby      ExecPolicy = "passby"
	ExecPolicyNeedApprove ExecPolicy = "need_approve"
	ExecPolicyOff         ExecPolicy = "off"
)

// CapabilityKind classifies the kind of capability a policy entry targets.
type CapabilityKind string

const (
	CapabilityBuiltin CapabilityKind = "builtin"
	CapabilityExtra   CapabilityKind = "extra"
	CapabilityTool    CapabilityKind = "tool"
	CapabilitySkill   CapabilityKind = "skill"
	CapabilityMCPTool CapabilityKind = "mcp_tool"
)

// CapabilityRef uniquely identifies one policy-controlled capability.
type CapabilityRef struct {
	Kind CapabilityKind
	Name string
}

// BashCommandPolicy applies an execution policy to bash calls whose command
// contains a matching executable head and argument prefix.
type BashCommandPolicy struct {
	Command    string
	ArgsPrefix []string
	Policy     ExecPolicy
}

// Decision records the effective execution-policy classification used for one
// tool call.
type Decision struct {
	Capability CapabilityRef
	Policy     ExecPolicy
	Reason     string
}

// DisabledError reports that a capability or bash command pattern is disabled.
type DisabledError struct {
	Capability CapabilityRef
	Reason     string
}

func (e *DisabledError) Error() string {
	if e == nil {
		return "llm: capability is disabled"
	}
	if e.Capability.Name == "" {
		if e.Reason == "" {
			return "llm: capability is disabled"
		}
		return "llm: capability is disabled: " + e.Reason
	}
	if e.Reason == "" {
		return fmt.Sprintf("llm: capability %q is disabled", e.Capability.Name)
	}
	return fmt.Sprintf("llm: capability %q is disabled: %s", e.Capability.Name, e.Reason)
}

// Default returns p when it is non-empty, otherwise passby.
func (p ExecPolicy) Default() ExecPolicy {
	if p == "" {
		return ExecPolicyPassby
	}
	return p
}

// BuiltinCapability returns the policy key for a core builtin tool.
func BuiltinCapability(name string) CapabilityRef {
	return CapabilityRef{Kind: CapabilityBuiltin, Name: name}
}

// ExtraCapability returns the policy key for an opt-in builtin extra tool.
func ExtraCapability(name string) CapabilityRef {
	return CapabilityRef{Kind: CapabilityExtra, Name: name}
}

// ToolCapability returns the generic policy key for an arbitrary tool name.
func ToolCapability(name string) CapabilityRef {
	return CapabilityRef{Kind: CapabilityTool, Name: name}
}

// SkillCapability returns the policy key for a registered skill.
func SkillCapability(name string) CapabilityRef {
	return CapabilityRef{Kind: CapabilitySkill, Name: name}
}

// ClonePolicyMap returns a deep copy of policies.
func ClonePolicyMap(policies map[CapabilityRef]ExecPolicy) map[CapabilityRef]ExecPolicy {
	if len(policies) == 0 {
		return nil
	}
	out := make(map[CapabilityRef]ExecPolicy, len(policies))
	for k, v := range policies {
		out[k] = v
	}
	return out
}

// CloneBashCommandPolicies returns a deep copy of policies.
func CloneBashCommandPolicies(policies []BashCommandPolicy) []BashCommandPolicy {
	if len(policies) == 0 {
		return nil
	}
	out := make([]BashCommandPolicy, len(policies))
	for i, policy := range policies {
		policy.ArgsPrefix = append([]string(nil), policy.ArgsPrefix...)
		out[i] = policy
	}
	return out
}

type recorderKey struct{}

// WithRecorder attaches decision as the mutable recorder for downstream tool
// execution.
func WithRecorder(ctx context.Context, decision *Decision) context.Context {
	if decision == nil {
		return ctx
	}
	return context.WithValue(ctx, recorderKey{}, decision)
}

// Record stores decision on ctx's recorder when present.
func Record(ctx context.Context, decision Decision) {
	if ctx == nil {
		return
	}
	target, _ := ctx.Value(recorderKey{}).(*Decision)
	if target == nil {
		return
	}
	*target = decision
}
