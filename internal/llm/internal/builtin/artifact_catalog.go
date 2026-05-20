package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultArtifactCatalogPath       = "downstream/artifacts"
	defaultArtifactCatalogHandoff    = "downstream/handoff.md"
	defaultArtifactCatalogMaxEntries = 50
)

type artifactCatalogRow struct {
	Path        string
	Kind        string
	Size        string
	Type        string
	Description string
	Status      string
}

type handoffArtifact struct {
	Path        string `yaml:"path"`
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
}

type handoffFrontMatter struct {
	Artifacts []handoffArtifact `yaml:"artifacts"`
}

// newArtifactCatalog returns a Tool that summarizes downstream artifacts by
// merging the on-disk artifact directory with handoff front matter metadata.
func newArtifactCatalog(ws workspace) tool {
	return newFuncTool(
		"artifact_catalog",
		"Summarize downstream artifacts by merging the on-disk artifact directory with downstream handoff front matter metadata. Useful for a one-call view of artifact paths, kinds, sizes, descriptions, and manifest mismatches.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Artifact directory to summarize. Defaults to downstream/artifacts.",
					"default":     defaultArtifactCatalogPath,
				},
				"handoff_path": map[string]any{
					"type":        "string",
					"description": "Handoff markdown file whose YAML front matter contains an artifacts list. Defaults to downstream/handoff.md. Missing files are ignored.",
					"default":     defaultArtifactCatalogHandoff,
				},
				"max_entries": map[string]any{
					"type":        "integer",
					"description": "Maximum number of table rows to return before truncating. Defaults to 50.",
					"default":     defaultArtifactCatalogMaxEntries,
					"minimum":     0,
				},
			},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path        string `json:"path"`
				HandoffPath string `json:"handoff_path"`
				MaxEntries  *int   `json:"max_entries"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("artifact_catalog: parse arguments: %w", err)
			}
			if args.Path == "" {
				args.Path = defaultArtifactCatalogPath
			}
			if args.HandoffPath == "" {
				args.HandoffPath = defaultArtifactCatalogHandoff
			}
			maxEntries := defaultArtifactCatalogMaxEntries
			if args.MaxEntries != nil {
				maxEntries = *args.MaxEntries
			}
			if maxEntries < 0 {
				return "", fmt.Errorf("artifact_catalog: max_entries must be >= 0")
			}

			artifactDir, err := ws.ResolvePath(args.Path)
			if err != nil {
				return "", fmt.Errorf("artifact_catalog: %w", err)
			}
			handoffPath, err := ws.ResolvePath(args.HandoffPath)
			if err != nil {
				return "", fmt.Errorf("artifact_catalog: %w", err)
			}

			rows, err := buildArtifactCatalogRows(artifactDir, args.Path, handoffPath)
			if err != nil {
				return "", fmt.Errorf("artifact_catalog: %w", err)
			}
			return renderArtifactCatalog(rows, maxEntries), nil
		},
	)
}

func buildArtifactCatalogRows(artifactDir string, basePath string, handoffPath string) ([]artifactCatalogRow, error) {
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		return nil, err
	}

	manifest, err := loadHandoffArtifacts(handoffPath)
	if err != nil {
		return nil, err
	}

	rows := make([]artifactCatalogRow, 0, len(entries)+len(manifest))
	diskPaths := make([]string, 0, len(entries))
	diskRows := make(map[string]artifactCatalogRow, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect %q: %w", entry.Name(), err)
		}
		relPath := joinSlashPath(basePath, entry.Name())
		row := artifactCatalogRow{
			Path:   relPath,
			Kind:   entryKind(entry),
			Size:   "-",
			Status: "unlisted",
		}
		if !entry.IsDir() {
			row.Size = fmt.Sprintf("%d", info.Size())
		}
		if declared, ok := manifest[relPath]; ok {
			row.Type = declared.Type
			row.Description = declared.Description
			row.Status = "listed"
			delete(manifest, relPath)
		}
		diskPaths = append(diskPaths, relPath)
		diskRows[relPath] = row
	}

	sort.Strings(diskPaths)
	for _, relPath := range diskPaths {
		rows = append(rows, diskRows[relPath])
	}

	missingPaths := make([]string, 0, len(manifest))
	for relPath := range manifest {
		missingPaths = append(missingPaths, relPath)
	}
	sort.Strings(missingPaths)
	for _, relPath := range missingPaths {
		declared := manifest[relPath]
		rows = append(rows, artifactCatalogRow{
			Path:        relPath,
			Kind:        "-",
			Size:        "-",
			Type:        declared.Type,
			Description: declared.Description,
			Status:      "missing",
		})
	}
	return rows, nil
}

func loadHandoffArtifacts(handoffPath string) (map[string]handoffArtifact, error) {
	data, err := os.ReadFile(handoffPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]handoffArtifact{}, nil
		}
		return nil, err
	}

	frontMatter, ok := extractFrontMatter(string(data))
	if !ok {
		return map[string]handoffArtifact{}, nil
	}

	var meta handoffFrontMatter
	if err := yaml.Unmarshal([]byte(frontMatter), &meta); err != nil {
		return nil, fmt.Errorf("parse %q front matter: %w", handoffPath, err)
	}

	manifest := make(map[string]handoffArtifact, len(meta.Artifacts))
	for _, artifact := range meta.Artifacts {
		normalized := normalizeCatalogPath(artifact.Path)
		if normalized == "" {
			continue
		}
		artifact.Path = normalized
		manifest[normalized] = artifact
	}
	return manifest, nil
}

func extractFrontMatter(raw string) (string, bool) {
	if strings.HasPrefix(raw, "---\r\n") {
		raw = strings.Replace(raw, "\r\n", "\n", -1)
	}
	if !strings.HasPrefix(raw, "---\n") {
		return "", false
	}
	rest := raw[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return "", false
	}
	return rest[:idx], true
}

func renderArtifactCatalog(rows []artifactCatalogRow, maxEntries int) string {
	var b strings.Builder
	b.WriteString("| path | kind | size | type | description | status |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")

	limit := len(rows)
	if maxEntries < limit {
		limit = maxEntries
	}
	for _, row := range rows[:limit] {
		b.WriteString("| ")
		b.WriteString(escapeTableCell(row.Path))
		b.WriteString(" | ")
		b.WriteString(escapeTableCell(orDash(row.Kind)))
		b.WriteString(" | ")
		b.WriteString(escapeTableCell(orDash(row.Size)))
		b.WriteString(" | ")
		b.WriteString(escapeTableCell(orDash(row.Type)))
		b.WriteString(" | ")
		b.WriteString(escapeTableCell(orDash(row.Description)))
		b.WriteString(" | ")
		b.WriteString(escapeTableCell(orDash(row.Status)))
		b.WriteString(" |\n")
	}
	if limit < len(rows) {
		b.WriteString(fmt.Sprintf("\n(%d more, truncated)", len(rows)-limit))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func entryKind(entry os.DirEntry) string {
	if entry.IsDir() {
		return "dir"
	}
	return "file"
}

func joinSlashPath(basePath string, name string) string {
	base := normalizeCatalogPath(basePath)
	if base == "" || base == "." {
		return normalizeCatalogPath(name)
	}
	return normalizeCatalogPath(base + "/" + filepath.ToSlash(name))
}

func normalizeCatalogPath(raw string) string {
	trimmed := strings.TrimSpace(filepath.ToSlash(raw))
	if trimmed == "" {
		return ""
	}
	return pathpkg.Clean(trimmed)
}

func escapeTableCell(value string) string {
	replacer := strings.NewReplacer("|", "\\|", "\n", "<br>", "\r", "")
	return replacer.Replace(value)
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
