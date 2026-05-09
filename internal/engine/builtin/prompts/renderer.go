package prompts

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed *.tmpl
var templateFS embed.FS

// Renderer renders built-in executor prompts from embedded templates.
type Renderer struct {
	templates *template.Template
}

type rendererConfig struct {
	overrides map[string]string
}

// RendererOption adjusts a built-in prompt renderer.
type RendererOption func(*rendererConfig)

// WithTemplateOverrides replaces embedded prompt templates by template name.
//
// This is an advanced extension hook. The renderer keeps its own copy of the
// map; callers may mutate their input map after construction.
func WithTemplateOverrides(overrides map[string]string) RendererOption {
	return func(cfg *rendererConfig) {
		if len(overrides) == 0 {
			return
		}
		cfg.overrides = make(map[string]string, len(overrides))
		for name, body := range overrides {
			cfg.overrides[name] = body
		}
	}
}

// NewRenderer returns a renderer backed by embedded prompt templates.
func NewRenderer(opts ...RendererOption) (*Renderer, error) {
	var cfg rendererConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	tpl, err := template.New("executor_prompts").Funcs(template.FuncMap{
		"trim": strings.TrimSpace,
	}).ParseFS(templateFS, "*.tmpl")
	if err != nil {
		return nil, fmt.Errorf("engine/builtin/prompts: parse templates: %w", err)
	}
	for name, body := range cfg.overrides {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("engine/builtin/prompts: override template name must not be empty")
		}
		if _, err := tpl.New(name).Parse(body); err != nil {
			return nil, fmt.Errorf("engine/builtin/prompts: parse override %s: %w", name, err)
		}
	}
	return &Renderer{templates: tpl}, nil
}

func (r *Renderer) render(name string, data any) (string, error) {
	if r == nil || r.templates == nil {
		return "", fmt.Errorf("engine/builtin/prompts: renderer is not initialized")
	}
	var b strings.Builder
	if err := r.templates.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("engine/builtin/prompts: render %s: %w", name, err)
	}
	return strings.TrimSpace(b.String()), nil
}

type PolishData struct {
	TaskDescription string
	Reason          string
	Solution        string
	Version         uint32
}

type ExecuteData struct {
	TaskDescription string
	SourceInput     string
	ParentHandoffs  string
	ArtifactsDir    string
	AccessibleDirs  []string
}

type ReportData struct {
	Output string
}

func (r *Renderer) RenderPolish(data PolishData) (string, error) {
	return r.render("polish.tmpl", data)
}

func (r *Renderer) RenderExecute(data ExecuteData) (string, error) {
	return r.render("execute.tmpl", data)
}

func (r *Renderer) RenderReport(data ReportData) (string, error) {
	return r.render("report.tmpl", data)
}
