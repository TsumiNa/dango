package runner

func (r *Runner) annotateExchangeOutput(node *Node, output any) any {
	text, ok := output.(string)
	if !ok || !IsExchangeMarkdown(text) {
		return output
	}
	defaults := ExchangeDocument{RunnerID: r.id}
	if node != nil {
		defaults.NodeID = node.Id
		defaults.SkillName = node.SkillName
		defaults.TaskDescription = node.TaskDescription
	}
	normalized, err := NormalizeExchangeMarkdown(text, defaults)
	if err != nil {
		return output
	}
	return normalized
}
