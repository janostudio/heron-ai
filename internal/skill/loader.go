package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/frontmatter"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type SkillLoader struct {
	baseDir string
}

func NewSkillLoader(baseDir string) *SkillLoader {
	return &SkillLoader{baseDir: baseDir}
}

func (l *SkillLoader) Load(dir string) (types.Skill, error) {
	skillFile := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return types.Skill{}, fmt.Errorf("read skill file: %w", err)
	}

	var skill types.Skill
	body, err := frontmatter.Parse(strings.NewReader(string(data)), &skill)
	if err != nil {
		return types.Skill{}, fmt.Errorf("parse skill frontmatter: %w", err)
	}
	skill.Body = string(body)

	return skill, nil
}

func (l *SkillLoader) LoadAll(skillsDir string) ([]types.Skill, error) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}

	var skills []types.Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skill, err := l.Load(filepath.Join(skillsDir, entry.Name()))
		if err != nil {
			continue // skip invalid skills
		}
		skills = append(skills, skill)
	}

	return skills, nil
}
