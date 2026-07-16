package skill

import (
	"fmt"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type SkillRegistry struct {
	mu     sync.RWMutex
	skills map[string]types.Skill
}

func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{skills: make(map[string]types.Skill)}
}

func (r *SkillRegistry) Register(skill types.Skill) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.skills[skill.Name]; exists {
		return fmt.Errorf("skill %q already registered", skill.Name)
	}
	r.skills[skill.Name] = skill
	return nil
}

func (r *SkillRegistry) Lookup(name string) (types.Skill, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.skills[name]
	if !ok {
		return types.Skill{}, fmt.Errorf("skill %q not found", name)
	}
	return s, nil
}

func (r *SkillRegistry) ListNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.skills))
	for name := range r.skills {
		names = append(names, name)
	}
	return names
}

func (r *SkillRegistry) ListSummaries() []types.SkillSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	summaries := make([]types.SkillSummary, 0, len(r.skills))
	for _, s := range r.skills {
		summaries = append(summaries, types.SkillSummary{
			Name:        s.Name,
			Description: s.Description,
		})
	}
	return summaries
}
