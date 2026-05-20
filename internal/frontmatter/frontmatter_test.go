package frontmatter

import (
	"strings"
	"testing"
)

type testMeta struct {
	Title string `yaml:"title"`
	Count int    `yaml:"count"`
}

func TestParseNoFrontMatter(t *testing.T) {
	raw := "# Title\n\n---\nnot front matter\n"
	var meta testMeta
	body, err := Parse(strings.NewReader(raw), &meta)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if string(body) != raw {
		t.Fatalf("body = %q, want %q", body, raw)
	}
	if meta != (testMeta{}) {
		t.Fatalf("meta = %+v, want zero value", meta)
	}
}

func TestParseYAMLFrontMatter(t *testing.T) {
	raw := "---\ntitle: hello\ncount: 2\n---\n\n# Body\n"
	var meta testMeta
	body, err := Parse(strings.NewReader(raw), &meta)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if meta.Title != "hello" || meta.Count != 2 {
		t.Fatalf("meta = %+v, want title hello and count 2", meta)
	}
	if string(body) != "\n# Body\n" {
		t.Fatalf("body = %q, want %q", body, "\n# Body\n")
	}
}

func TestParseExplicitYAMLFrontMatter(t *testing.T) {
	raw := "---yaml\ntitle: hello\n---\nbody"
	var meta testMeta
	body, err := Parse(strings.NewReader(raw), &meta)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if meta.Title != "hello" {
		t.Fatalf("meta.Title = %q, want hello", meta.Title)
	}
	if string(body) != "body" {
		t.Fatalf("body = %q, want body", body)
	}
}

func TestParseTOMLFrontMatter(t *testing.T) {
	raw := "+++\ntitle = 'hello'\ncount = 2\n+++\n# Body\n"
	var meta testMeta
	body, err := Parse(strings.NewReader(raw), &meta)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if meta.Title != "hello" || meta.Count != 2 {
		t.Fatalf("meta = %+v, want title hello and count 2", meta)
	}
	if string(body) != "# Body\n" {
		t.Fatalf("body = %q, want %q", body, "# Body\n")
	}
}

func TestParseExplicitTOMLFrontMatter(t *testing.T) {
	raw := "---toml\ntitle = 'hello'\n---\nbody"
	var meta testMeta
	body, err := Parse(strings.NewReader(raw), &meta)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if meta.Title != "hello" {
		t.Fatalf("meta.Title = %q, want hello", meta.Title)
	}
	if string(body) != "body" {
		t.Fatalf("body = %q, want body", body)
	}
}

func TestParseFrontMatterEndingAtEOF(t *testing.T) {
	var meta testMeta
	body, err := Parse(strings.NewReader("---\ntitle: hello\n---"), &meta)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if meta.Title != "hello" {
		t.Fatalf("meta.Title = %q, want hello", meta.Title)
	}
	if string(body) != "" {
		t.Fatalf("body = %q, want empty", body)
	}
}

func TestParseEmptyBodyAfterFrontMatter(t *testing.T) {
	var meta testMeta
	body, err := Parse(strings.NewReader("---\ntitle: hello\n---\n"), &meta)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if meta.Title != "hello" {
		t.Fatalf("meta.Title = %q, want hello", meta.Title)
	}
	if string(body) != "" {
		t.Fatalf("body = %q, want empty", body)
	}
}

func TestParseUnclosedFrontMatter(t *testing.T) {
	var meta testMeta
	if _, err := Parse(strings.NewReader("---\ntitle: hello\n"), &meta); err == nil {
		t.Fatal("Parse() error = nil, want unclosed front matter error")
	}
}

func TestParseInvalidYAML(t *testing.T) {
	var meta testMeta
	if _, err := Parse(strings.NewReader("---\ntitle: [\n---\nbody"), &meta); err == nil {
		t.Fatal("Parse() error = nil, want YAML error")
	}
}

func TestParseInvalidTOML(t *testing.T) {
	var meta testMeta
	if _, err := Parse(strings.NewReader("+++\ntitle = \n+++\nbody"), &meta); err == nil {
		t.Fatal("Parse() error = nil, want TOML error")
	}
}

func TestParseBodyDelimiterIsNotReparsed(t *testing.T) {
	raw := "---\ntitle: hello\n---\nbody\n---\nstill body\n"
	var meta testMeta
	body, err := Parse(strings.NewReader(raw), &meta)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got, want := string(body), "body\n---\nstill body\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
