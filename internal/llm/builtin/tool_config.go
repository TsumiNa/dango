package builtin

import (
	"fmt"

	"github.com/tsumina/dango/internal/llm/toolpolicy"
)

// ExtraTool names an opt-in built-in tool that is not part of the core floor.
type ExtraTool string

const (
	// ExtraListDir enables the list_dir tool.
	ExtraListDir ExtraTool = "list_dir"
	// ExtraPwd enables the pwd tool.
	ExtraPwd ExtraTool = "pwd"
)

// String returns the tool name exposed to the model.
func (t ExtraTool) String() string { return string(t) }

// ParseExtraTool converts a config string into a typed extra tool value.
func ParseExtraTool(name string) (ExtraTool, error) {
	switch ExtraTool(name) {
	case ExtraListDir:
		return ExtraListDir, nil
	case ExtraPwd:
		return ExtraPwd, nil
	default:
		return "", fmt.Errorf("builtin: unknown extra tool %q", name)
	}
}

// ExecPolicy controls whether a capability runs automatically, waits for
// approval, or stays disabled.
type ExecPolicy = toolpolicy.ExecPolicy

const (
	ExecPolicyPassby      = toolpolicy.ExecPolicyPassby
	ExecPolicyNeedApprove = toolpolicy.ExecPolicyNeedApprove
	ExecPolicyOff         = toolpolicy.ExecPolicyOff
)

// CapabilityKind classifies one policy-controlled capability entry.
type CapabilityKind = toolpolicy.CapabilityKind

const (
	CapabilityBuiltin = toolpolicy.CapabilityBuiltin
	CapabilityExtra   = toolpolicy.CapabilityExtra
	CapabilityTool    = toolpolicy.CapabilityTool
	CapabilitySkill   = toolpolicy.CapabilitySkill
	CapabilityMCPTool = toolpolicy.CapabilityMCPTool
)

// CapabilityRef uniquely identifies one policy-controlled capability.
type CapabilityRef = toolpolicy.CapabilityRef

// BashCommandPolicy applies an execution policy to matching bash commands.
type BashCommandPolicy = toolpolicy.BashCommandPolicy

// Decision records the effective execution-policy classification used for one
// tool call.
type Decision = toolpolicy.Decision

// DisabledError reports that a capability or bash command pattern is disabled.
type DisabledError = toolpolicy.DisabledError

// ToolSetConfig describes which opt-in builtin capabilities are added on top
// of the always-available core tool floor.
type ToolSetConfig struct {
	BashAllow           []string
	BashBlock           []string
	BashURLAllowlist    []string
	Extras              []ExtraTool
	Policies            map[CapabilityRef]ExecPolicy
	BashCommandPolicies []BashCommandPolicy
}
