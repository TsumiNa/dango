package llm

import (
	"github.com/tsumina/dango/llm/internal/builtin"
	"github.com/tsumina/dango/llm/internal/toolpolicy"
)

// ExtraTool names an opt-in built-in tool that is appended after the default
// built-in tool set.
type ExtraTool = builtin.ExtraTool

const (
	// ExtraListDir enables the list_dir tool.
	ExtraListDir = builtin.ExtraListDir
	// ExtraPwd enables the pwd tool.
	ExtraPwd = builtin.ExtraPwd
)

// ParseExtraTool converts a config string into a typed extra tool value.
func ParseExtraTool(name string) (ExtraTool, error) { return builtin.ParseExtraTool(name) }

// ToolSetConfig is the single typed input that controls which built-in tools
// are enabled for a skill.
type ToolSetConfig = builtin.ToolSetConfig

// ExecPolicy controls whether a capability runs automatically, waits for
// approval, or stays disabled.
type ExecPolicy = builtin.ExecPolicy

const (
	ExecPolicyPassby      = builtin.ExecPolicyPassby
	ExecPolicyNeedApprove = builtin.ExecPolicyNeedApprove
	ExecPolicyOff         = builtin.ExecPolicyOff
)

// CapabilityTool and CapabilityMCPTool are the only capability-kind constants
// with callers via the llm package. Other kinds and CapabilityKind itself are
// accessible through toolpolicy or builtin directly.
const (
	CapabilityTool    = builtin.CapabilityTool
	CapabilityMCPTool = builtin.CapabilityMCPTool
)

// CapabilityRef uniquely identifies one policy-controlled capability.
type CapabilityRef = builtin.CapabilityRef

// BashCommandPolicy applies an execution policy to matching bash commands.
type BashCommandPolicy = builtin.BashCommandPolicy

// Decision records the effective execution-policy classification used for one
// tool call.
type Decision = builtin.Decision

// DisabledError reports that a capability or bash command pattern is disabled.
type DisabledError = builtin.DisabledError

// ApprovalOutcome reports the result of one approval round-trip.
type ApprovalOutcome = toolpolicy.ApprovalOutcome

const (
	ApprovalOutcomeApprove           = toolpolicy.ApprovalOutcomeApprove
	ApprovalOutcomeDeny              = toolpolicy.ApprovalOutcomeDeny
	ApprovalOutcomeApproveForSession = toolpolicy.ApprovalOutcomeApproveForSession
)

// ApprovalRequest is the summarized payload an approver sees for one gated tool
// call.
type ApprovalRequest = toolpolicy.ApprovalRequest

// ApprovalResponse reports the approver's decision for one request.
type ApprovalResponse = toolpolicy.ApprovalResponse

// ApprovalDeniedError reports that a need_approve tool call was denied.
type ApprovalDeniedError = toolpolicy.ApprovalDeniedError

// BuiltinCapability returns the policy key for a core builtin tool.
func BuiltinCapability(name string) CapabilityRef { return toolpolicy.BuiltinCapability(name) }

// ExtraCapability returns the policy key for an opt-in builtin extra tool.
func ExtraCapability(name string) CapabilityRef { return toolpolicy.ExtraCapability(name) }

// ToolCapability returns the generic policy key for an arbitrary tool name.
func ToolCapability(name string) CapabilityRef { return toolpolicy.ToolCapability(name) }

// DefaultToolSetConfig returns the default built-in tool availability for a
// newly loaded skill.
func DefaultToolSetConfig() ToolSetConfig { return ToolSetConfig{} }
