package runner

// LooksLikeChannelMarkdown reports whether raw is one of the current runner
// channel markdown envelopes.
func LooksLikeChannelMarkdown(raw string) bool {
	return isRunnerChannelMarkdown(raw)
}
