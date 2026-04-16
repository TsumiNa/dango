package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/terrai/skillframe/skill"
	"go.uber.org/zap"
)

// Matcher decides whether a skill is relevant to a user query.
// Return a score in [0, 1]. 0 = irrelevant, 1 = perfect match.
type Matcher interface {
	Score(query string, s *skill.Skill) float64
}

// Registry holds all loaded skills and provides matching.
type Registry struct {
	mu      sync.RWMutex
	skills  map[string]*skill.Skill
	matcher Matcher
	log     *zap.Logger
}

// New creates a Registry with the given Matcher.
// If matcher is nil, a default keyword matcher is used.
func New(matcher Matcher, log *zap.Logger) *Registry {
	if matcher == nil {
		matcher = &KeywordMatcher{}
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Registry{
		skills:  make(map[string]*skill.Skill),
		matcher: matcher,
		log:     log,
	}
}

// LoadDir scans a directory for skill subdirectories (each containing SKILL.md).
func (r *Registry) LoadDir(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read skills dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name(), "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			continue // not a skill directory
		}
		s, err := skill.LoadFromDir(filepath.Join(root, e.Name()))
		if err != nil {
			r.log.Warn("skip skill", zap.String("dir", e.Name()), zap.Error(err))
			continue
		}
		r.Register(s)
		r.log.Info("loaded skill", zap.String("name", s.Name))
	}
	return nil
}

// Register adds a skill to the registry.
func (r *Registry) Register(s *skill.Skill) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills[s.Name] = s
}

// Get returns a skill by name.
func (r *Registry) Get(name string) (*skill.Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.skills[name]
	return s, ok
}

// All returns all registered skills.
func (r *Registry) All() []*skill.Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*skill.Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	return out
}

// Match returns skills relevant to the query, sorted by score descending.
// threshold controls the minimum score (e.g. 0.1).
func (r *Registry) Match(query string, threshold float64) []ScoredSkill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var results []ScoredSkill
	for _, s := range r.skills {
		score := r.matcher.Score(query, s)
		if score >= threshold {
			results = append(results, ScoredSkill{Skill: s, Score: score})
		}
	}

	// simple insertion sort (skill count is small)
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].Score > results[j-1].Score; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
	return results
}

// ScoredSkill pairs a skill with its relevance score.
type ScoredSkill struct {
	Skill *skill.Skill
	Score float64
}

// --- Default Matcher: keyword overlap ---

// KeywordMatcher scores by word overlap between query and skill description.
type KeywordMatcher struct{}

func (m *KeywordMatcher) Score(query string, s *skill.Skill) float64 {
	qWords := tokenize(query)
	dWords := tokenize(s.Description + " " + s.Name)
	if len(qWords) == 0 || len(dWords) == 0 {
		return 0
	}

	dSet := make(map[string]struct{}, len(dWords))
	for _, w := range dWords {
		dSet[w] = struct{}{}
	}

	hits := 0
	for _, w := range qWords {
		if _, ok := dSet[w]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(qWords))
}

func tokenize(text string) []string {
	text = strings.ToLower(text)
	// crude tokenizer: split on non-alphanumeric, drop short tokens
	var tokens []string
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_')
	}) {
		if len(w) > 2 {
			tokens = append(tokens, w)
		}
	}
	return tokens
}
