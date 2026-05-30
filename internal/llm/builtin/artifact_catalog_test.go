package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactCatalogReturnsDiskAndManifestMerge(t *testing.T) {
	root := t.TempDir()
	writeArtifactCatalogFile(t, filepath.Join(root, "downstream", "artifacts", "report.txt"), "hello")
	if err := os.MkdirAll(filepath.Join(root, "downstream", "artifacts", "images"), 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	writeArtifactCatalogHandoff(t, root, `---
artifacts:
  - path: downstream/artifacts/images
    type: dir
    description: Plot assets
  - path: downstream/artifacts/report.txt
    type: markdown
    description: Summary report
---

Body
`)

	tool := newArtifactCatalog(testWorkspace{root})
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("artifact_catalog: %v", err)
	}

	want := strings.TrimSpace(`
| path | kind | size | type | description | status |
| --- | --- | --- | --- | --- | --- |
| downstream/artifacts/images | dir | - | dir | Plot assets | listed |
| downstream/artifacts/report.txt | file | 5 | markdown | Summary report | listed |
`)
	if out != want {
		t.Fatalf("artifact_catalog output = %q, want %q", out, want)
	}
}

func TestArtifactCatalogFlagsUnlistedDiskEntry(t *testing.T) {
	root := t.TempDir()
	writeArtifactCatalogFile(t, filepath.Join(root, "downstream", "artifacts", "extra.bin"), "data")
	writeArtifactCatalogHandoff(t, root, `---
artifacts: []
---
`)

	tool := newArtifactCatalog(testWorkspace{root})
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("artifact_catalog: %v", err)
	}
	if !strings.Contains(out, "| downstream/artifacts/extra.bin | file | 4 | - | - | unlisted |") {
		t.Fatalf("artifact_catalog output = %q, want unlisted row", out)
	}
}

func TestArtifactCatalogFlagsMissingManifestEntry(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "downstream", "artifacts"), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	writeArtifactCatalogHandoff(t, root, `---
artifacts:
  - path: downstream/artifacts/missing.txt
    type: text
    description: Missing file
---
`)

	tool := newArtifactCatalog(testWorkspace{root})
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("artifact_catalog: %v", err)
	}
	if !strings.Contains(out, "| downstream/artifacts/missing.txt | - | - | text | Missing file | missing |") {
		t.Fatalf("artifact_catalog output = %q, want missing row", out)
	}
}

func TestArtifactCatalogMissingHandoffIsSilent(t *testing.T) {
	root := t.TempDir()
	writeArtifactCatalogFile(t, filepath.Join(root, "downstream", "artifacts", "report.txt"), "hello")

	tool := newArtifactCatalog(testWorkspace{root})
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("artifact_catalog: %v", err)
	}
	if !strings.Contains(out, "| downstream/artifacts/report.txt | file | 5 | - | - | unlisted |") {
		t.Fatalf("artifact_catalog output = %q, want unlisted row without handoff", out)
	}
}

func TestArtifactCatalogParsesHandoffFrontMatterAtEOF(t *testing.T) {
	root := t.TempDir()
	writeArtifactCatalogFile(t, filepath.Join(root, "downstream", "artifacts", "report.txt"), "hello")
	writeArtifactCatalogHandoff(t, root, `---
artifacts:
  - path: downstream/artifacts/report.txt
    type: markdown
    description: Summary report
---`)

	tool := newArtifactCatalog(testWorkspace{root})
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("artifact_catalog: %v", err)
	}
	if !strings.Contains(out, "| downstream/artifacts/report.txt | file | 5 | markdown | Summary report | listed |") {
		t.Fatalf("artifact_catalog output = %q, want listed row parsed from EOF front matter", out)
	}
}

func TestArtifactCatalogMissingDirectoryReturnsError(t *testing.T) {
	root := t.TempDir()
	tool := newArtifactCatalog(testWorkspace{root})

	_, err := tool.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected missing directory error")
	}
	if !strings.Contains(err.Error(), "artifact_catalog:") {
		t.Fatalf("error = %v, want artifact_catalog context", err)
	}
}

func TestArtifactCatalogPathEscapeRejected(t *testing.T) {
	root := t.TempDir()
	tool := newArtifactCatalog(testWorkspace{root})

	_, err := tool.Execute(context.Background(), `{"path":"../escape"}`)
	if err == nil {
		t.Fatal("expected path escape error")
	}
	if !strings.Contains(err.Error(), "escapes workspace root") {
		t.Fatalf("error = %v, want path escape rejection", err)
	}
}

func TestArtifactCatalogRespectsMaxEntries(t *testing.T) {
	root := t.TempDir()
	writeArtifactCatalogFile(t, filepath.Join(root, "downstream", "artifacts", "a.txt"), "a")
	writeArtifactCatalogFile(t, filepath.Join(root, "downstream", "artifacts", "b.txt"), "bb")
	writeArtifactCatalogFile(t, filepath.Join(root, "downstream", "artifacts", "c.txt"), "ccc")

	tool := newArtifactCatalog(testWorkspace{root})
	out, err := tool.Execute(context.Background(), `{"max_entries":2}`)
	if err != nil {
		t.Fatalf("artifact_catalog: %v", err)
	}
	if strings.Count(out, "\n| downstream/artifacts/") != 2 {
		t.Fatalf("artifact_catalog output = %q, want exactly 2 rows", out)
	}
	if !strings.Contains(out, "(1 more, truncated)") {
		t.Fatalf("artifact_catalog output = %q, want truncation footer", out)
	}
	if strings.Contains(out, "downstream/artifacts/c.txt") {
		t.Fatalf("artifact_catalog output = %q, want truncated rows omitted", out)
	}
}

func writeArtifactCatalogFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func writeArtifactCatalogHandoff(t *testing.T, root string, content string) {
	t.Helper()
	path := filepath.Join(root, "downstream", "handoff.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir handoff dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write handoff: %v", err)
	}
}
