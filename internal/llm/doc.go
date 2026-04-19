// Package llm provides a thin wrapper around the OpenAI Go SDK's Responses API.
//
// The package auto-detects credentials from the process environment (optionally
// sourced from a local .env file) and returns a ready-to-use client bound to
// the appropriate provider endpoint. Supported providers are selected by the
// first matching API key among OPENAI_API_KEY, OPENROUTER_API_KEY, and
// GEMINI_API_KEY. The model used for orchestration requests is read from the
// ORCHESTRATION_MODEL environment variable.
package llm
