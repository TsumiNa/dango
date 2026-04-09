package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/runtime"
	"github.com/tsumina/dango/internal/store/sqlite"
)

type fakeRuntime struct {
	describeYAML []byte
}

func (f fakeRuntime) Pull(_ context.Context, _ string) error {
	return nil
}

func (f fakeRuntime) DescribeTool(_ context.Context, _ string) ([]byte, error) {
	return f.describeYAML, nil
}

func (f fakeRuntime) PlanExecutor(_ context.Context, _ runtime.ExecutorPlanRequest) ([]byte, error) {
	return []byte(`{}`), nil
}

func (f fakeRuntime) RunExecutor(_ context.Context, _ runtime.ExecutorRunRequest) error {
	return nil
}

func TestRegistryRegister(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	locator, err := datadir.New(root)
	if err != nil {
		t.Fatalf("datadir.New() error = %v", err)
	}
	if err := locator.Ensure(); err != nil {
		t.Fatalf("locator.Ensure() error = %v", err)
	}

	store, err := sqlite.Open(locator.DBPath())
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	overridePath := filepath.Join(root, "override.yaml")
	overrideYAML := []byte("model: openrouter/google/gemini-3.5-flash\ndefaults:\n  page_size: letter\n")
	if err := os.WriteFile(overridePath, overrideYAML, 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	service := NewRegistryService(locator, store, fakeRuntime{
		describeYAML: []byte("" +
			"name: pdf-generator\n" +
			"version: 1.0.0\n" +
			"description: Generates PDF reports from structured data\n" +
			"input_types: [json, csv, md]\n" +
			"output_types: [pdf, png]\n" +
			"model: local/pdf-specialist-v2\n" +
			"defaults:\n" +
			"  page_size: A4\n" +
			"  language: ja\n"),
	}, nil)

	registered, err := service.Register(context.Background(), "example/pdf-tool:v1", overridePath)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if got, want := registered.Tool.Name, "pdf-generator"; got != want {
		t.Fatalf("registered.Tool.Name = %q, want %q", got, want)
	}
	if got, want := registered.Tool.Model, "openrouter/google/gemini-3.5-flash"; got != want {
		t.Fatalf("registered.Tool.Model = %q, want %q", got, want)
	}
	if got, want := registered.Tool.Defaults["page_size"], "letter"; got != want {
		t.Fatalf("registered.Tool.Defaults[page_size] = %#v, want %q", got, want)
	}
	if got, want := registered.Tool.Defaults["language"], "ja"; got != want {
		t.Fatalf("registered.Tool.Defaults[language] = %#v, want %q", got, want)
	}

	for _, path := range []string{
		registered.Files.ToolPath,
		registered.Files.OverridePath,
		registered.Files.MergedPath,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file %q to exist: %v", path, err)
		}
	}
}
