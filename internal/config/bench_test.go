package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// BenchmarkLoadDefinitions measures loading + validating a full .agents
// configuration tree. It is on the startup path for every HTTP server boot
// and every flow definition reload, so slow YAML parsing or validation adds
// directly to cold-start latency. The fixture mirrors the production layout
// (flows/teams/agents dirs) built once outside the timer.
func BenchmarkLoadDefinitions(b *testing.B) {
	root := b.TempDir()
	agentsDir := filepath.Join(root, ".agents", "agents")
	teamsDir := filepath.Join(root, ".agents", "teams")
	flowsDir := filepath.Join(root, ".agents", "flows")
	require.NoError(b, os.MkdirAll(agentsDir, 0755))
	require.NoError(b, os.MkdirAll(teamsDir, 0755))
	require.NoError(b, os.MkdirAll(flowsDir, 0755))

	require.NoError(b, os.WriteFile(filepath.Join(flowsDir, "default.yml"), []byte(`
id: code-fix
entry: default
teams:
  default:
    team: default-team
    coordinator: true
    inputs:
      user_message: true
`), 0644))

	require.NoError(b, os.WriteFile(filepath.Join(teamsDir, "default.yml"), []byte(`
id: default-team
calls:
  assistant:
    type: agent
    agent: default-assistant
    output:
      record: AssistantReply
`), 0644))

	require.NoError(b, os.WriteFile(filepath.Join(agentsDir, "default-assistant.md"), []byte(`---
name: default-assistant
persona:
  role: assistant
  goal: coordinate
model:
  provider: openai
  model: gpt-4o-mini
---
You coordinate the request.
`), 0644))

	loader := NewConfigLoader(root)
	flowPath := filepath.Join(flowsDir, "default.yml")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		definitions, err := loader.LoadDefinitions(context.Background(), DefinitionsLoadRequest{
			FlowPath: flowPath,
		})
		if err != nil {
			b.Fatal(err)
		}
		if definitions == nil || definitions.Flow.ID != "code-fix" {
			b.Fatalf("unexpected definitions: %+v", definitions)
		}
	}
}
