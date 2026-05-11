package instructions

import (
	"strings"
	"testing"
)

func TestStageNoteLoadsMarkdownStageNotes(t *testing.T) {
	for _, stage := range []string{"polish", "execute", "report"} {
		note, err := StageNote(stage)
		if err != nil {
			t.Fatalf("StageNote(%q): %v", stage, err)
		}
		if !strings.HasPrefix(note, "# ") {
			t.Fatalf("StageNote(%q) is not markdown:\n%s", stage, note)
		}
	}
}

func TestExecuteStageNoteTeachesAgenticInputInspection(t *testing.T) {
	note, err := StageNote("execute")
	if err != nil {
		t.Fatalf("StageNote: %v", err)
	}
	for _, want := range []string{
		"Use tools to inspect exchange and upstream handoff references",
		"`downstream/artifacts/`",
		"`memo/`",
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("execute note missing %q:\n%s", want, note)
		}
	}
}

func TestStageNoteRejectsInvalidStageNames(t *testing.T) {
	if _, err := StageNote("../execute"); err == nil {
		t.Fatal("expected invalid stage name error")
	}
}
