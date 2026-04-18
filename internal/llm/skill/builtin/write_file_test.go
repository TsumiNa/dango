package builtin

import (
	"context"
	"encoding/json"
	"testing"
)

func TestWriteAndReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	write := NewWriteFile(root)
	args, _ := json.Marshal(map[string]string{"path": "sub/hello.txt", "content": "hi"})
	if _, err := write.Execute(ctx, string(args)); err != nil {
		t.Fatalf("write_file: %v", err)
	}

	read := NewReadFile(root)
	args, _ = json.Marshal(map[string]string{"path": "sub/hello.txt"})
	out, err := read.Execute(ctx, string(args))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if out != "hi" {
		t.Errorf("read_file output = %q, want %q", out, "hi")
	}
}
