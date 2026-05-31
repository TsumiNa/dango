package runner

import (
	"fmt"

	"github.com/tsumina/dango/llm"
)

type toolSetPolicyAgent interface {
	RuntimeToolSetConfig() llm.ToolSetConfig
	SetRuntimeToolSetConfig(llm.ToolSetConfig)
}

func (r *Runner) snapshotSkillToolSets(nodes map[string]*Node) {
	if len(nodes) == 0 {
		return
	}
	r.toolSetMu.Lock()
	defer r.toolSetMu.Unlock()
	for _, node := range nodes {
		if node == nil || node.Agent == nil || node.SkillName == "" {
			continue
		}
		if _, exists := r.skillToolSets[node.SkillName]; exists {
			continue
		}
		agent, ok := node.Agent.(toolSetPolicyAgent)
		if !ok {
			continue
		}
		r.skillToolSets[node.SkillName] = cloneToolSetConfig(agent.RuntimeToolSetConfig())
	}
}

func (r *Runner) applySkillToolSet(skillName string, agent Agent) {
	if skillName == "" || agent == nil {
		return
	}
	policyAgent, ok := agent.(toolSetPolicyAgent)
	if !ok {
		return
	}
	r.toolSetMu.RLock()
	cfg, exists := r.skillToolSets[skillName]
	r.toolSetMu.RUnlock()
	if !exists {
		cfg = cloneToolSetConfig(policyAgent.RuntimeToolSetConfig())
		r.toolSetMu.Lock()
		r.skillToolSets[skillName] = cfg
		r.toolSetMu.Unlock()
	}
	policyAgent.SetRuntimeToolSetConfig(cloneToolSetConfig(cfg))
}

// SkillToolSetConfig returns the runner-local policy snapshot for skillName.
func (r *Runner) SkillToolSetConfig(skillName string) (llm.ToolSetConfig, bool) {
	if r == nil || skillName == "" {
		return llm.ToolSetConfig{}, false
	}
	r.toolSetMu.RLock()
	defer r.toolSetMu.RUnlock()
	cfg, ok := r.skillToolSets[skillName]
	return cloneToolSetConfig(cfg), ok
}

// SetSkillCapabilityPolicy changes one capability policy for this runner only.
func (r *Runner) SetSkillCapabilityPolicy(skillName string, ref llm.CapabilityRef, policy llm.ExecPolicy) error {
	if r == nil || skillName == "" {
		return fmt.Errorf("runner: skill name is required")
	}
	r.toolSetMu.Lock()
	defer r.toolSetMu.Unlock()
	cfg, ok := r.skillToolSets[skillName]
	if !ok {
		return fmt.Errorf("runner: skill %q has no policy snapshot", skillName)
	}
	cfg = cloneToolSetConfig(cfg)
	if cfg.Policies == nil {
		cfg.Policies = make(map[llm.CapabilityRef]llm.ExecPolicy)
	}
	cfg.Policies[ref] = policy
	r.skillToolSets[skillName] = cfg
	return nil
}

// SetSkillBashCommandPolicies replaces the runner-local bash command policies
// for skillName.
func (r *Runner) SetSkillBashCommandPolicies(skillName string, policies []llm.BashCommandPolicy) error {
	if r == nil || skillName == "" {
		return fmt.Errorf("runner: skill name is required")
	}
	r.toolSetMu.Lock()
	defer r.toolSetMu.Unlock()
	cfg, ok := r.skillToolSets[skillName]
	if !ok {
		return fmt.Errorf("runner: skill %q has no policy snapshot", skillName)
	}
	cfg = cloneToolSetConfig(cfg)
	cfg.BashCommandPolicies = append([]llm.BashCommandPolicy(nil), policies...)
	for i := range cfg.BashCommandPolicies {
		cfg.BashCommandPolicies[i].ArgsPrefix = append([]string(nil), cfg.BashCommandPolicies[i].ArgsPrefix...)
	}
	r.skillToolSets[skillName] = cfg
	return nil
}

func cloneToolSetConfig(cfg llm.ToolSetConfig) llm.ToolSetConfig {
	cfg.BashAllow = append([]string(nil), cfg.BashAllow...)
	cfg.BashBlock = append([]string(nil), cfg.BashBlock...)
	cfg.Extras = append([]llm.ExtraTool(nil), cfg.Extras...)
	if cfg.Policies != nil {
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
