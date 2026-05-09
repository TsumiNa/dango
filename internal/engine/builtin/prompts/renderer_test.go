package prompts

import (
	"strings"
	"testing"
)

func TestRendererRendersPolishTemplate(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	out, err := r.RenderPolish(PolishData{
		TaskDescription: "Validate groundwater model inputs.",
		Reason:          "Need to check data completeness before training.",
		Solution:        "Run quality checks and normalize units.",
		Version:         3,
	})
	if err != nil {
		t.Fatalf("RenderPolish: %v", err)
	}
	if !strings.Contains(out, "Validate groundwater model inputs.") || !strings.Contains(out, "Planner version: 3") {
		t.Fatalf("RenderPolish output missing interpolated fields:\n%s", out)
	}
}

func TestRendererRendersExecuteTemplateWithMemoDiscipline(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	out, err := r.RenderExecute(ExecuteData{
		TaskDescription: "Train the model.",
		SourceInput:     `{"records":[1]}`,
		ParentHandoffs:  "No parent handoffs.",
		ArtifactsDir:    "/tmp/artifacts",
		AccessibleDirs:  []string{"/tmp/workspace"},
	})
	if err != nil {
		t.Fatalf("RenderExecute: %v", err)
	}
	if !strings.Contains(out, "Return one handoff markdown document body") || !strings.Contains(out, "Original request input for this root task:") {
		t.Fatalf("RenderExecute output missing required guidance:\n%s", out)
	}
}

func TestRendererRendersReportTemplate(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	out, err := r.RenderReport(ReportData{Output: `{"status":"ok"}`})
	if err != nil {
		t.Fatalf("RenderReport: %v", err)
	}
	if !strings.Contains(out, `{"status":"ok"}`) {
		t.Fatalf("RenderReport output missing execution output:\n%s", out)
	}
}

func TestRendererUsesTemplateOverride(t *testing.T) {
	r, err := NewRenderer(WithTemplateOverrides(map[string]string{
		"execute.tmpl": "advanced override for {{.TaskDescription}}",
	}))
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	out, err := r.RenderExecute(ExecuteData{TaskDescription: "custom task"})
	if err != nil {
		t.Fatalf("RenderExecute: %v", err)
	}
	if out != "advanced override for custom task" {
		t.Fatalf("RenderExecute = %q", out)
	}
}
