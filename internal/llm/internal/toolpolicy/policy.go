package toolpolicy

import (
	"context"
	"fmt"
)

// ExecPolicy controls whether a capability runs immediately, waits for
// approval, or is disabled.
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
	Capability      CapabilityRef
	Policy          ExecPolicy
	Reason          string
	ApprovalOutcome ApprovalOutcome
	ApprovalReason  string
}

// DisabledError reports that a capability or bash command pattern is disabled.
type DisabledError struct {
	Capability CapabilityRef
	Reason     string
}

// ApprovalOutcome reports the result of one approval round-trip.
type ApprovalOutcome string

const (
	ApprovalOutcomeApprove           ApprovalOutcome = "approve"
	ApprovalOutcomeDeny              ApprovalOutcome = "deny"
	ApprovalOutcomeApproveForSession ApprovalOutcome = "approve_for_session"
)

// ApprovalRequest is the summarized payload an approver sees for one gated tool
// call.
type ApprovalRequest struct {
	CallID           string
	ToolName         string
	ArgumentsSummary string
	Capability       CapabilityRef
	Policy           ExecPolicy
	Reason           string
}

// ApprovalResponse reports the approver's decision for one request.
type ApprovalResponse struct {
	Outcome ApprovalOutcome
	Reason  string
}

// ApprovalDeniedError reports that a need_approve tool call was denied.
type ApprovalDeniedError struct {
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

func (e *ApprovalDeniedError) Error() string {
	if e == nil {
		return "llm: approval denied"
	}
	if e.Capability.Name == "" {
		if e.Reason == "" {
			return "llm: approval denied"
		}
		return "llm: approval denied: " + e.Reason
	}
	if e.Reason == "" {
		return fmt.Sprintf("llm: capability %q was denied approval", e.Capability.Name)
	}
	return fmt.Sprintf("llm: capability %q was denied approval: %s", e.Capability.Name, e.Reason)
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
type approvalHandlerKey struct{}
type callMetadataKey struct{}

type approvalHandler func(context.Context, ApprovalRequest) (ApprovalResponse, error)

type callMetadata struct {
	CallID           string
	ToolName         string
	ArgumentsSummary string
}

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

// WithApprover attaches handler as the approval callback for downstream gated
// tool execution.
func WithApprover(ctx context.Context, handler func(context.Context, ApprovalRequest) (ApprovalResponse, error)) context.Context {
	if handler == nil {
		return ctx
	}
	return context.WithValue(ctx, approvalHandlerKey{}, approvalHandler(handler))
}

// WithCallMetadata attaches call metadata used to enrich approval requests.
func WithCallMetadata(ctx context.Context, callID string, toolName string, argumentsSummary string) context.Context {
	if callID == "" && toolName == "" && argumentsSummary == "" {
		return ctx
	}
	return context.WithValue(ctx, callMetadataKey{}, callMetadata{
		CallID:           callID,
		ToolName:         toolName,
		ArgumentsSummary: argumentsSummary,
	})
}

// RequestApproval routes req through ctx's configured approval callback.
func RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if meta, _ := ctx.Value(callMetadataKey{}).(callMetadata); req.CallID == "" || req.ToolName == "" || req.ArgumentsSummary == "" {
		if req.CallID == "" {
			req.CallID = meta.CallID
		}
		if req.ToolName == "" {
			req.ToolName = meta.ToolName
		}
		if req.ArgumentsSummary == "" {
			req.ArgumentsSummary = meta.ArgumentsSummary
		}
	}
	handler, _ := ctx.Value(approvalHandlerKey{}).(approvalHandler)
	if handler == nil {
		resp := ApprovalResponse{
			Outcome: ApprovalOutcomeDeny,
			Reason:  "no approver configured",
		}
		return resp, &ApprovalDeniedError{Capability: req.Capability, Reason: resp.Reason}
	}
	resp, err := handler(ctx, req)
	if err != nil {
		return resp, err
	}
	switch resp.Outcome {
	case ApprovalOutcomeApprove, ApprovalOutcomeApproveForSession:
		return resp, nil
	case "", ApprovalOutcomeDeny:
		if resp.Reason == "" {
			resp.Reason = "approval denied"
		}
		if resp.Outcome == "" {
			resp.Outcome = ApprovalOutcomeDeny
		}
		return resp, &ApprovalDeniedError{Capability: req.Capability, Reason: resp.Reason}
	default:
		return resp, fmt.Errorf("llm: invalid approval outcome %q", resp.Outcome)
	}
}
