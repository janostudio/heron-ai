package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestSkillRegistry_RegisterAndLookup(t *testing.T) {
	reg := NewSkillRegistry()
	skill := types.Skill{
		Name:        "test-skill",
		Description: "A test skill",
		Body:        "skill body content",
		Tools:       []string{"tool1", "tool2"},
	}

	err := reg.Register(skill)
	require.NoError(t, err)

	found, err := reg.Lookup("test-skill")
	require.NoError(t, err)
	assert.Equal(t, "test-skill", found.Name)
	assert.Equal(t, "A test skill", found.Description)
	assert.Equal(t, "skill body content", found.Body)
	assert.Equal(t, []string{"tool1", "tool2"}, found.Tools)

	// Duplicate registration
	err = reg.Register(skill)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")

	// Lookup non-existent
	_, err = reg.Lookup("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSkillRegistry_ListNames(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Register(types.Skill{Name: "s1", Description: "d1"})
	reg.Register(types.Skill{Name: "s2", Description: "d2"})

	names := reg.ListNames()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "s1")
	assert.Contains(t, names, "s2")
}

func TestSkillRegistry_ListSummaries(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Register(types.Skill{Name: "s1", Description: "d1"})
	reg.Register(types.Skill{Name: "s2", Description: "d2"})

	summaries := reg.ListSummaries()
	assert.Len(t, summaries, 2)
	
	names := make([]string, len(summaries))
	descs := make([]string, len(summaries))
	for i, s := range summaries {
		names[i] = s.Name
		descs[i] = s.Description
	}
	assert.Contains(t, names, "s1")
	assert.Contains(t, names, "s2")
	assert.Contains(t, descs, "d1")
	assert.Contains(t, descs, "d2")
}

func TestSkillLoader_Load(t *testing.T) {
	dir := t.TempDir()
	skillContent := `---
name: my-skill
description: A loaded skill
tools:
  - tool1
  - tool2
---
This is the skill body content.`

	err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0644)
	require.NoError(t, err)

	loader := NewSkillLoader(".")
	skill, err := loader.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "my-skill", skill.Name)
	assert.Equal(t, "A loaded skill", skill.Description)
	assert.Equal(t, []string{"tool1", "tool2"}, skill.Tools)
	assert.Equal(t, "This is the skill body content.", skill.Body)
}

func TestSkillLoader_Load_MissingFile(t *testing.T) {
	dir := t.TempDir()
	loader := NewSkillLoader(".")
	_, err := loader.Load(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read skill file")
}

func TestSkillLoader_LoadAll(t *testing.T) {
	skillsDir := t.TempDir()

	// Create skill1
	dir1 := filepath.Join(skillsDir, "skill1")
	err := os.MkdirAll(dir1, 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir1, "SKILL.md"), []byte(`---
name: skill1
description: First skill
---
Body of skill1.`), 0644)
	require.NoError(t, err)

	// Create skill2
	dir2 := filepath.Join(skillsDir, "skill2")
	err = os.MkdirAll(dir2, 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir2, "SKILL.md"), []byte(`---
name: skill2
description: Second skill
---
Body of skill2.`), 0644)
	require.NoError(t, err)

	// Create invalid dir (no SKILL.md) - should be skipped
	dir3 := filepath.Join(skillsDir, "not-a-skill")
	err = os.MkdirAll(dir3, 0755)
	require.NoError(t, err)

	loader := NewSkillLoader(".")
	skills, err := loader.LoadAll(skillsDir)
	require.NoError(t, err)
	assert.Len(t, skills, 2)

	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	assert.Contains(t, names, "skill1")
	assert.Contains(t, names, "skill2")
}

func TestSkillInjector_Inject(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Register(types.Skill{
		Name:  "s1",
		Body:  "prompt for s1",
		Tools: []string{"t1", "t2"},
	})
	reg.Register(types.Skill{
		Name:  "s2",
		Body:  "prompt for s2",
		Tools: []string{"t3"},
	})

	injector := NewSkillInjector(reg)
	prompts, tools := injector.Inject([]string{"s1", "s2"})
	assert.Equal(t, []string{"prompt for s1", "prompt for s2"}, prompts)
	assert.Equal(t, []string{"t1", "t2", "t3"}, tools)
}

func TestSkillInjector_Inject_UnknownSkill(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Register(types.Skill{
		Name:  "s1",
		Body:  "prompt",
		Tools: []string{"t1"},
	})

	injector := NewSkillInjector(reg)
	prompts, tools := injector.Inject([]string{"s1", "unknown"})
	assert.Equal(t, []string{"prompt"}, prompts)
	assert.Equal(t, []string{"t1"}, tools)
}

func TestSkillInjector_Inject_EmptyBody(t *testing.T) {
	reg := NewSkillRegistry()
	reg.Register(types.Skill{
		Name:  "no-body",
		Body:  "",
		Tools: []string{"t1"},
	})

	injector := NewSkillInjector(reg)
	prompts, tools := injector.Inject([]string{"no-body"})
	assert.Empty(t, prompts)
	assert.Equal(t, []string{"t1"}, tools)
}
