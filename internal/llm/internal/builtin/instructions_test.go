package builtin

import (
	"strings"
	"testing"
)

func TestSystemInstructionsTeachAgenticRuntimeWorkflow(t *testing.T) {
	for _, want := range []string{
		"Conversation bootstrap order",
		"Executor lifecycle: polish → execute → report",
		"workspace channel contract",
		"`memo/` is private durable scratch",
		"`upstream/<node>/handoff.md` contains directed upstream input",
		"`downstream/artifacts/` stores durable files",
		"`exchange/` is runner-scoped shared public context",
		"Use tools to inspect referenced exchange or handoff files",
		"Memo means writing files under the provided `memo/` directory",
		"Domain `SKILL.md`",
		"do not need to restate these generic workflow rules",
	} {
		if !strings.Contains(SystemInstructions, want) {
			t.Fatalf("SystemInstructions missing %q", want)
		}
	}
}
