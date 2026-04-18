package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Tool is the contract the [Agent] uses to invoke a single function tool.
//
// A Tool exposes the metadata needed to advertise itself to the LLM and a
// handler that executes a single call. Implementations must be safe for
// concurrent use when the same instance is shared across multiple [Agent]
// runs. Handlers should return a compact string representation of the tool's
// output; that string is sent back to the model verbatim as the
// function_call_output.
type Tool interface {
	// Name returns the unique tool name advertised to the model.
	Name() string
	// Description explains to the model when the tool should be used.
	Description() string
	// Parameters returns the JSON Schema object describing the tool arguments.
	Parameters() map[string]any
	// Execute runs the tool with the raw JSON arguments string produced by
	// the model and returns the output reported back to the model.
	Execute(ctx context.Context, arguments string) (string, error)
}

// FuncTool is the default [Tool] implementation, built from a name,
// description, parameter schema, and a handler function.
//
// FuncTool is the preferred way to register new built-in or user-supplied
// tools without declaring a new Go type. The zero value is not usable; use
// [NewFuncTool] or set all fields explicitly.
type FuncTool struct {
	NameV        string
	DescriptionV string
	ParametersV  map[string]any
	Handler      func(ctx context.Context, arguments string) (string, error)
}

// NewFuncTool constructs a [FuncTool] from its components.
func NewFuncTool(name, description string, parameters map[string]any, handler func(ctx context.Context, arguments string) (string, error)) *FuncTool {
	return &FuncTool{
		NameV:        name,
		DescriptionV: description,
		ParametersV:  parameters,
		Handler:      handler,
	}
}

func (t *FuncTool) Name() string               { return t.NameV }
func (t *FuncTool) Description() string        { return t.DescriptionV }
func (t *FuncTool) Parameters() map[string]any { return t.ParametersV }
func (t *FuncTool) Execute(ctx context.Context, arguments string) (string, error) {
	if t.Handler == nil {
		return "", fmt.Errorf("skill: tool %q has no handler", t.NameV)
	}
	return t.Handler(ctx, arguments)
}

// BuiltinTools returns the default set of filesystem and shell tools scoped to
// root.
//
// All filesystem tools resolve relative paths against root and reject absolute
// paths or parent traversals that escape root. The bash tool runs commands
// with root as its working directory. root should be an existing directory;
// typically it is the skill directory.
func BuiltinTools(root string) []Tool {
	return []Tool{
		NewBashTool(root),
		NewReadFileTool(root),
		NewWriteFileTool(root),
		NewListDirTool(root),
		NewPwdTool(root),
	}
}

// NewBashTool returns a Tool that runs a shell command via /bin/bash -c with
// cwd fixed to root and the parent process environment inherited.
func NewBashTool(root string) Tool {
	return NewFuncTool(
		"bash",
		"Run a shell command via /bin/bash -c. Use for ad-hoc scripting, invoking skill scripts, or running helper programs. Returns combined stdout+stderr.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute.",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},

		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("bash: parse arguments: %w", err)
			}
			if strings.TrimSpace(args.Command) == "" {
				return "", fmt.Errorf("bash: command is required")
			}
			cmd := exec.CommandContext(ctx, "/bin/bash", "-c", args.Command)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if err != nil {
				return string(out), fmt.Errorf("bash: %w", err)
			}
			return string(out), nil
		},
	)
}

// NewReadFileTool returns a Tool that reads a file's contents as UTF-8 text.
// The path must resolve inside root.
func NewReadFileTool(root string) Tool {
	return NewFuncTool(
		"read_file",
		"Read the contents of a file within the skill workspace and return it as text.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file relative to the skill workspace root.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("read_file: parse arguments: %w", err)
			}
			p, err := resolveWorkspacePath(root, args.Path)
			if err != nil {
				return "", fmt.Errorf("read_file: %w", err)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return "", fmt.Errorf("read_file: %w", err)
			}
			return string(data), nil
		},
	)
}

// NewWriteFileTool returns a Tool that writes UTF-8 content to a file within
// root, creating parent directories as needed. Existing files are overwritten.
func NewWriteFileTool(root string) Tool {
	return NewFuncTool(
		"write_file",
		"Write UTF-8 text content to a file within the skill workspace, creating parent directories as needed. Overwrites existing files.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file relative to the skill workspace root.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The file content to write.",
				},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("write_file: parse arguments: %w", err)
			}
			p, err := resolveWorkspacePath(root, args.Path)
			if err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			if err := os.WriteFile(p, []byte(args.Content), 0o644); err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
		},
	)
}

// NewListDirTool returns a Tool that lists entries in a directory within root.
// Entries are returned one per line; directories are suffixed with "/".
func NewListDirTool(root string) Tool {
	return NewFuncTool(
		"list_dir",
		"List the entries of a directory within the skill workspace. Directories are suffixed with '/'.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path relative to the workspace root. Use '.' for the root.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("list_dir: parse arguments: %w", err)
			}
			if args.Path == "" {
				args.Path = "."
			}
			p, err := resolveWorkspacePath(root, args.Path)
			if err != nil {
				return "", fmt.Errorf("list_dir: %w", err)
			}
			entries, err := os.ReadDir(p)
			if err != nil {
				return "", fmt.Errorf("list_dir: %w", err)
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				names = append(names, name)
			}
			sort.Strings(names)
			return strings.Join(names, "\n"), nil
		},
	)
}

// NewPwdTool returns a Tool that reports the absolute path of the skill
// workspace root.
func NewPwdTool(root string) Tool {
	return NewFuncTool(
		"pwd",
		"Return the absolute path of the skill workspace root.",
		map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			abs, err := filepath.Abs(root)
			if err != nil {
				return "", fmt.Errorf("pwd: %w", err)
			}
			return abs, nil
		},
	)
}

// resolveWorkspacePath resolves rel against root and ensures the cleaned
// result stays inside root. It returns a cleaned absolute path on success.
func resolveWorkspacePath(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the workspace root", rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	cleaned := filepath.Clean(filepath.Join(absRoot, rel))
	relCheck, err := filepath.Rel(absRoot, cleaned)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root", rel)
	}
	return cleaned, nil
}
