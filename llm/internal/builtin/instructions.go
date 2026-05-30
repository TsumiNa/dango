package builtin

import _ "embed"

// SystemInstructions is the platform-level prompt every skill receives as
// the leading section of its system instruction. It documents Dango's two
// skill roles, the agent lifecycle (polish/execute/report), the
// workspace access model, the built-in tool conventions, and the handoff
// markdown contract — so external skills can be reused inside Dango with
// no Dango-specific edits to their own SKILL.md.
//
//go:embed system_instructions.md
var SystemInstructions string
