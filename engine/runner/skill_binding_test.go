package runner

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/tsumina/dango/llm"
	streampkg "github.com/tsumina/dango/stream"
)

type bindRecorderAgent struct {
	calls       int
	seenSession []string
	seenPaths   []AgentRuntimePaths
}

func (e *bindRecorderAgent) BindForRunner(sessID *string, runtimePaths AgentRuntimePaths, sessStores ...llm.SessionStore) (string, error) {
	e.calls++
	runtimePaths.AccessibleDirs = append([]string(nil), runtimePaths.AccessibleDirs...)
	e.seenPaths = append(e.seenPaths, runtimePaths)
	if len(sessStores) != 1 || sessStores[0] == nil {
		return "", context.Canceled
	}
	if sessID != nil {
		e.seenSession = append(e.seenSession, *sessID)
		return *sessID, nil
	}
	e.seenSession = append(e.seenSession, "")
	return "session-1", nil
}

func (e *bindRecorderAgent) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	return nil, nil, nil
}

func (e *bindRecorderAgent) Polish(ctx context.Context) (any, error) { return nil, nil }

func (e *bindRecorderAgent) Report(ctx context.Context, output any) (any, error) {
	return output, nil
}

func TestRunner_PrepareNodeAgents_ReusesStoredSessionID(t *testing.T) {
	exec := &bindRecorderAgent{}
	r := New(
		WithLogger(testLogger),
		WithInitialPlan(&CoarsePlan{Request: "demo"}, map[string]*Node{
			"only": {Id: "only", Agent: exec},
		}),
	)

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}
	if exec.calls != 1 {
		t.Fatalf("bind calls after StartPolish = %d, want 1", exec.calls)
	}
	if got := exec.seenSession[0]; got != "" {
		t.Fatalf("first bind session = %q, want empty", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached awaiting review, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.AcceptPolishedPlan(context.Background(), &CoarsePlan{Request: "demo"}); err != nil {
		t.Fatalf("AcceptPolishedPlan: %v", err)
	}
	if exec.calls != 2 {
		t.Fatalf("bind calls after AcceptPolishedPlan = %d, want 2", exec.calls)
	}
	if got := exec.seenSession[1]; got != "session-1" {
		t.Fatalf("second bind session = %q, want session-1", got)
	}
}

func TestRunner_PrepareNodeAgent_ForwardsTypedRuntimePaths(t *testing.T) {
	exec := &bindRecorderAgent{}
	r := newTestRunner()
	workspace, err := ProvisionWorkspace(t.TempDir(), r.ID(), []string{"only"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	r.workspace = workspace
	runtimePaths, err := r.nodeRuntimePaths("only", "skill-one", nil)
	if err != nil {
		t.Fatalf("nodeRuntimePaths: %v", err)
	}

	if err := r.prepareNodeAgent("only", exec, runtimePaths); err != nil {
		t.Fatalf("prepareNodeAgent: %v", err)
	}
	if len(exec.seenPaths) != 1 {
		t.Fatalf("seen runtime path count = %d, want 1", len(exec.seenPaths))
	}
	skillWS, ok := workspace.Skill("only")
	if !ok {
		t.Fatal("workspace.Skill(only) = false")
	}
	got := exec.seenPaths[0]
	if got.RunnerID != r.ID() || got.NodeID != "only" || got.SkillName != "skill-one" {
		t.Fatalf("runtime paths = %+v", got)
	}
	if got.MemoDir != skillWS.MemoDir || got.UpstreamDir != skillWS.UpstreamDir || got.DownstreamDir != skillWS.DownstreamDir || got.ScratchDir != skillWS.ScratchDir {
		t.Fatalf("runtime dirs = %+v, want workspace dirs %+v", got, skillWS)
	}
	if got.ExchangeDir != workspace.ExchangeDir() {
		t.Fatalf("ExchangeDir = %q, want %q", got.ExchangeDir, workspace.ExchangeDir())
	}
	wantArchiveMemoDir := filepath.Join(workspace.ArchiveDir(), "memo", "only")
	if got.ArchiveMemoDir != wantArchiveMemoDir {
		t.Fatalf("ArchiveMemoDir = %q, want %q", got.ArchiveMemoDir, wantArchiveMemoDir)
	}
	for _, wantDir := range []string{skillWS.MemoDir, skillWS.UpstreamDir, skillWS.DownstreamDir, skillWS.ScratchDir, workspace.ExchangeDir()} {
		if !containsDir(got.AccessibleDirs, wantDir) {
			t.Fatalf("runtime AccessibleDirs = %v, missing %q", got.AccessibleDirs, wantDir)
		}
	}
}

type streamingBindAgent struct {
	eventStream *streampkg.Stream
}

func (e *streamingBindAgent) BindForRunner(sessID *string, runtimePaths AgentRuntimePaths, sessStores ...llm.SessionStore) (string, error) {
	e.eventStream = streampkg.New(streampkg.Scope{NodeID: "owned-node"}, streampkg.DefaultConfig())
	return "session-owned-stream", nil
}

func (e *streamingBindAgent) EventStream() *streampkg.Stream { return e.eventStream }

func (e *streamingBindAgent) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	return nil, nil, nil
}

func (e *streamingBindAgent) Polish(ctx context.Context) (any, error) { return nil, nil }

func (e *streamingBindAgent) Report(ctx context.Context, output any) (any, error) {
	return output, nil
}

func TestRunner_PrepareNodeAgent_MergesAgentOwnedStream(t *testing.T) {
	exec := &streamingBindAgent{}
	r := newTestRunner()
	sub, err := r.SubscribeStream(streampkg.Filter{EventTypes: []string{streampkg.EventMergeBundle}}, streampkg.WithSubscriberBuffer(4), streampkg.WithRawStream())
	if err != nil {
		t.Fatalf("SubscribeStream: %v", err)
	}
	defer sub.Cancel()

	if err := r.prepareNodeAgent("owned-node", exec, AgentRuntimePaths{}); err != nil {
		t.Fatalf("prepareNodeAgent: %v", err)
	}
	if exec.eventStream == nil {
		t.Fatal("agent did not create an event stream during bind")
	}
	if err := exec.eventStream.Emit(context.Background(), streampkg.Event{
		EventType: streampkg.EventLLMOutputDelta,
		From:      streampkg.Source{Layer: "skill", ID: "owned-skill"},
		Status:    streampkg.StatusRunning,
		Scope:     streampkg.Scope{NodeID: "owned-node"},
		Delta:     json.RawMessage(`"hello"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	topEvent, ok, err := sub.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("Next = (_, %v, %v), want merged event", ok, err)
	}
	if topEvent.EventType != streampkg.EventMergeBundle {
		t.Fatalf("merged top event = %q, want bundle", topEvent.EventType)
	}
	events, err := streampkg.FilterBundleEvent(topEvent, streampkg.Filter{EventTypes: []string{streampkg.EventLLMOutputDelta}})
	if err != nil {
		t.Fatalf("FilterBundleEvent: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expanded events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Scope.RunnerID != r.ID() || event.Scope.NodeID != "owned-node" || event.From.ID != "owned-skill" {
		t.Fatalf("merged event = %+v, want runner and node scope preserved", event)
	}
}

type policyBindAgent struct {
	cfg  llm.ToolSetConfig
	seen []llm.ToolSetConfig
}

func (e *policyBindAgent) RuntimeToolSetConfig() llm.ToolSetConfig { return cloneRunnerToolSet(e.cfg) }

func (e *policyBindAgent) SetRuntimeToolSetConfig(cfg llm.ToolSetConfig) {
	e.cfg = cloneRunnerToolSet(cfg)
}

func (e *policyBindAgent) BindForRunner(sessID *string, runtimePaths AgentRuntimePaths, sessStores ...llm.SessionStore) (string, error) {
	e.seen = append(e.seen, cloneRunnerToolSet(e.cfg))
	return "session-policy", nil
}

func (e *policyBindAgent) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	return nil, nil, nil
}

func (e *policyBindAgent) Polish(ctx context.Context) (any, error) { return nil, nil }

func (e *policyBindAgent) Report(ctx context.Context, output any) (any, error) {
	return output, nil
}

func TestRunnerSnapshotIsolatesFromPreset(t *testing.T) {
	preset := llm.ToolSetConfig{
		Policies: map[llm.CapabilityRef]llm.ExecPolicy{
			llm.ToolCapability("echo"): llm.ExecPolicyNeedApprove,
		},
	}
	agent := &policyBindAgent{cfg: preset}
	r := New(WithLogger(testLogger), WithInitialPlan(&CoarsePlan{Request: "demo"}, map[string]*Node{
		"node": {Id: "node", SkillName: "skill", Agent: agent},
	}))
	preset.Policies[llm.ToolCapability("echo")] = llm.ExecPolicyOff
	got, ok := r.SkillToolSetConfig("skill")
	if !ok {
		t.Fatal("SkillToolSetConfig(skill) = false")
	}
	if got.Policies[llm.ToolCapability("echo")] != llm.ExecPolicyNeedApprove {
		t.Fatalf("runner snapshot policy = %q, want need_approve", got.Policies[llm.ToolCapability("echo")])
	}
}

func TestRunnerDynamicAdjustAffectsOnlyThisRun(t *testing.T) {
	preset := llm.ToolSetConfig{
		Policies: map[llm.CapabilityRef]llm.ExecPolicy{
			llm.ToolCapability("echo"): llm.ExecPolicyNeedApprove,
		},
	}
	agentA := &policyBindAgent{cfg: preset}
	agentB := &policyBindAgent{cfg: preset}
	runnerA := New(WithLogger(testLogger), WithInitialPlan(&CoarsePlan{Request: "demo"}, map[string]*Node{
		"node-a": {Id: "node-a", SkillName: "skill", Agent: agentA},
	}))
	runnerB := New(WithLogger(testLogger), WithInitialPlan(&CoarsePlan{Request: "demo"}, map[string]*Node{
		"node-b": {Id: "node-b", SkillName: "skill", Agent: agentB},
	}))
	if err := runnerA.SetSkillCapabilityPolicy("skill", llm.ToolCapability("echo"), llm.ExecPolicyOff); err != nil {
		t.Fatalf("SetSkillCapabilityPolicy: %v", err)
	}
	if err := runnerA.prepareNodeAgent("node-a", agentA, AgentRuntimePaths{SkillName: "skill"}); err != nil {
		t.Fatalf("prepareNodeAgent(runnerA): %v", err)
	}
	if err := runnerB.prepareNodeAgent("node-b", agentB, AgentRuntimePaths{SkillName: "skill"}); err != nil {
		t.Fatalf("prepareNodeAgent(runnerB): %v", err)
	}
	if len(agentA.seen) != 1 || agentA.seen[0].Policies[llm.ToolCapability("echo")] != llm.ExecPolicyOff {
		t.Fatalf("runnerA saw policies %+v, want off", agentA.seen)
	}
	if len(agentB.seen) != 1 || agentB.seen[0].Policies[llm.ToolCapability("echo")] != llm.ExecPolicyNeedApprove {
		t.Fatalf("runnerB saw policies %+v, want need_approve", agentB.seen)
	}
	if preset.Policies[llm.ToolCapability("echo")] != llm.ExecPolicyNeedApprove {
		t.Fatalf("preset mutated to %q, want need_approve", preset.Policies[llm.ToolCapability("echo")])
	}
}

func TestRunnerSetSkillBashCommandPoliciesClonesArgsPrefix(t *testing.T) {
	agent := &policyBindAgent{}
	r := New(WithLogger(testLogger), WithInitialPlan(&CoarsePlan{Request: "demo"}, map[string]*Node{
		"node": {Id: "node", SkillName: "skill", Agent: agent},
	}))
	policies := []llm.BashCommandPolicy{{Command: "git", ArgsPrefix: []string{"push"}, Policy: llm.ExecPolicyNeedApprove}}
	if err := r.SetSkillBashCommandPolicies("skill", policies); err != nil {
		t.Fatalf("SetSkillBashCommandPolicies: %v", err)
	}
	policies[0].ArgsPrefix[0] = "status"
	got, ok := r.SkillToolSetConfig("skill")
	if !ok {
		t.Fatal("SkillToolSetConfig(skill) = false")
	}
	if got.BashCommandPolicies[0].ArgsPrefix[0] != "push" {
		t.Fatalf("runner snapshot args prefix = %q, want push", got.BashCommandPolicies[0].ArgsPrefix[0])
	}
}

func cloneRunnerToolSet(cfg llm.ToolSetConfig) llm.ToolSetConfig {
	cfg.BashAllow = append([]string(nil), cfg.BashAllow...)
	cfg.BashBlock = append([]string(nil), cfg.BashBlock...)
	cfg.Extras = append([]llm.ExtraTool(nil), cfg.Extras...)
	if len(cfg.Policies) > 0 {
		cloned := make(map[llm.CapabilityRef]llm.ExecPolicy, len(cfg.Policies))
		for k, v := range cfg.Policies {
			cloned[k] = v
		}
		cfg.Policies = cloned
	}
	cfg.BashCommandPolicies = append([]llm.BashCommandPolicy(nil), cfg.BashCommandPolicies...)
	for i := range cfg.BashCommandPolicies {
		cfg.BashCommandPolicies[i].ArgsPrefix = append([]string(nil), cfg.BashCommandPolicies[i].ArgsPrefix...)
	}
	return cfg
}
