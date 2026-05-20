// Package prompts stores repository-owned prompt assets for Dango's built-in
// AI hooks.
//
// The package sits between orchestration code and the llm transport layer. It
// does not call models directly. Instead, it is the home for repository-owned
// prompt content and any deterministic helpers that render that content into
// request-ready text.
//
// Dependency direction should remain one-way. Packages such as orchestrator,
// runner, and agent may depend on prompts, but prompts should not depend on
// those higher-level packages for control flow. Its job is to encode prompt
// assets and lightweight rendering support, not runtime policy.
package prompts
