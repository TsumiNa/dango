// Package llm provides clients, conversations, and skills built on top of the
// OpenAI Go SDK's Responses API.
//
// The package auto-detects credentials from the process environment (optionally
// sourced from a local .env file) and returns a ready-to-use client bound to
// the appropriate provider endpoint. Supported providers are selected by the
// first matching API key among OPENAI_API_KEY, OPENROUTER_API_KEY, and
// GEMINI_API_KEY. The model used for orchestration requests is read from the
// MODEL environment variable.
//
// A [Conversation] owns a multi-turn request loop, tool dispatch, token usage,
// and optional session persistence. A [Skill] loads SKILL.md metadata and
// instructions from a workspace, then binds that prompt and tool set to a
// conversation when the caller is ready to run it. Skills expose three
// workspace layers to built-in tools: the skill source directory, a private
// temp playground for relative paths and shell execution, and optional
// user-added accessible directories configured with [Skill.SetAccessibleDirs].
package llm
