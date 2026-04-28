package main

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestStyleRespectsColorEnabled(t *testing.T) {
	old := colorEnabled
	t.Cleanup(func() { colorEnabled = old })

	colorEnabled = false
	if got := style(ansiRed, "hello"); got != "hello" {
		t.Fatalf("style without color = %q, want hello", got)
	}

	colorEnabled = true
	if got := style(ansiRed, "hello"); got != ansiRed+"hello"+ansiReset {
		t.Fatalf("style with color = %q", got)
	}
}

func TestFrontmatterBody(t *testing.T) {
	doc := "---\nname: demo\n---\nbody\n"
	if got := frontmatterBody(doc); got != "body\n" {
		t.Fatalf("frontmatterBody = %q, want body", got)
	}
	if got := frontmatterBody("plain body"); got != "plain body" {
		t.Fatalf("frontmatterBody without frontmatter = %q", got)
	}
}

func TestIndentBlock(t *testing.T) {
	got := indentBlock("one\ntwo\n", "  ")
	if got != "  one\n  two" {
		t.Fatalf("indentBlock = %q", got)
	}
	if got := indentBlock("", "  "); !strings.Contains(got, "(empty)") {
		t.Fatalf("indentBlock empty = %q", got)
	}
}

func TestParseEnvFiles(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []string
		wantErr string
	}{
		{
			name: "no files",
		},
		{
			name: "long flag can repeat",
			args: []string{"--env-file", "local.env", "--env-file=secret.env"},
			want: []string{"local.env", "secret.env"},
		},
		{
			name: "short flag",
			args: []string{"-e", "local.env"},
			want: []string{"local.env"},
		},
		{
			name:    "empty file",
			args:    []string{"--env-file", ""},
			wantErr: "env file path must not be empty",
		},
		{
			name:    "unexpected positional arg",
			args:    []string{"task"},
			wantErr: "unexpected arguments: task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEnvFiles(tt.args, io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseEnvFiles() err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEnvFiles() unexpected err: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseEnvFiles() = %v, want %v", got, tt.want)
			}
		})
	}
}
