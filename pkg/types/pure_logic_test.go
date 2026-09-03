package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestContextConfigWithDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   ContextConfig
		want ContextConfig
	}{
		{
			name: "all zero falls back to defaults",
			in:   ContextConfig{},
			want: ContextConfig{
				TargetRatio:                0.70,
				CompactionThreshold:        0.80,
				HardLimitRatio:             0.90,
				OutputReserveRatio:         0,
				ToolOutputRatio:            0.10,
				MaxToolOutputChars:         0,
				MicrocompactThresholdChars: 8192,
				MicrocompactMaxChars:       4096,
				RecentMessageGroups:        2,
			},
		},
		{
			name: "ratio out of range clamps to default",
			in: ContextConfig{
				TargetRatio:          -1,
				CompactionThreshold:  1.5,
				HardLimitRatio:       2,
				OutputReserveRatio:   -0.5,
				ToolOutputRatio:      1,
				MicrocompactMaxChars: 100000,
			},
			want: ContextConfig{
				TargetRatio:                0.70,
				CompactionThreshold:        0.80,
				HardLimitRatio:             0.90,
				OutputReserveRatio:         0.15,
				ToolOutputRatio:            0.10,
				MicrocompactThresholdChars: 8192,
				MicrocompactMaxChars:       8192, // clamped to threshold
				RecentMessageGroups:        2,
			},
		},
		{
			name: "ratio exactly zero clamps except output reserve allows zero",
			in: ContextConfig{
				TargetRatio:         0,
				CompactionThreshold: 0,
				HardLimitRatio:      0,
				OutputReserveRatio:  0,
				ToolOutputRatio:     0,
			},
			want: ContextConfig{
				TargetRatio:                0.70,
				CompactionThreshold:        0.80,
				HardLimitRatio:             0.90,
				OutputReserveRatio:         0,
				ToolOutputRatio:            0.10,
				MicrocompactThresholdChars: 8192,
				MicrocompactMaxChars:       4096,
				RecentMessageGroups:        2,
			},
		},
		{
			name: "ratio exactly one clamps except hard limit allows one",
			in: ContextConfig{
				TargetRatio:         1,
				CompactionThreshold: 1,
				HardLimitRatio:      1,
				OutputReserveRatio:  1,
				ToolOutputRatio:     1,
			},
			want: ContextConfig{
				TargetRatio:                0.70,
				CompactionThreshold:        0.80,
				HardLimitRatio:             1,
				OutputReserveRatio:         0.15,
				ToolOutputRatio:            0.10,
				MicrocompactThresholdChars: 8192,
				MicrocompactMaxChars:       4096,
				RecentMessageGroups:        2,
			},
		},
		{
			name: "valid ratios preserved",
			in: ContextConfig{
				TargetRatio:         0.55,
				CompactionThreshold: 0.75,
				HardLimitRatio:      0.85,
				OutputReserveRatio:  0.20,
				ToolOutputRatio:     0.05,
			},
			want: ContextConfig{
				TargetRatio:                0.55,
				CompactionThreshold:        0.75,
				HardLimitRatio:             0.85,
				OutputReserveRatio:         0.20,
				ToolOutputRatio:            0.05,
				MicrocompactThresholdChars: 8192,
				MicrocompactMaxChars:       4096,
				RecentMessageGroups:        2,
			},
		},
		{
			name: "compaction threshold clamped to hard limit",
			in: ContextConfig{
				TargetRatio:         0.70,
				CompactionThreshold: 0.95,
				HardLimitRatio:      0.80,
			},
			want: ContextConfig{
				TargetRatio:                0.70,
				CompactionThreshold:        0.80,
				HardLimitRatio:             0.80,
				OutputReserveRatio:         0,
				ToolOutputRatio:            0.10,
				MicrocompactThresholdChars: 8192,
				MicrocompactMaxChars:       4096,
				RecentMessageGroups:        2,
			},
		},
		{
			name: "negative tool output chars clamps to zero",
			in: ContextConfig{
				MaxToolOutputChars: -5,
			},
			want: ContextConfig{
				TargetRatio:                0.70,
				CompactionThreshold:        0.80,
				HardLimitRatio:             0.90,
				OutputReserveRatio:         0,
				ToolOutputRatio:            0.10,
				MaxToolOutputChars:         0,
				MicrocompactThresholdChars: 8192,
				MicrocompactMaxChars:       4096,
				RecentMessageGroups:        2,
			},
		},
		{
			name: "microcompact max clamped to threshold",
			in: ContextConfig{
				MicrocompactThresholdChars: 1000,
				MicrocompactMaxChars:       2000,
			},
			want: ContextConfig{
				TargetRatio:                0.70,
				CompactionThreshold:        0.80,
				HardLimitRatio:             0.90,
				OutputReserveRatio:         0,
				ToolOutputRatio:            0.10,
				MicrocompactThresholdChars: 1000,
				MicrocompactMaxChars:       1000,
				RecentMessageGroups:        2,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.WithDefaults()
			require.Equal(t, tt.want, got)
		})
	}
}

func TestModelConfigOutputTokenLimit(t *testing.T) {
	intPtr := func(v int) *int { return &v }

	t.Run("max_output_tokens takes priority", func(t *testing.T) {
		c := ModelConfig{MaxOutputTokens: intPtr(512), MaxTokens: 1024}
		got := c.OutputTokenLimit()
		require.NotNil(t, got)
		require.Equal(t, 512, *got)
	})

	t.Run("falls back to deprecated max_tokens", func(t *testing.T) {
		c := ModelConfig{MaxTokens: 1024}
		got := c.OutputTokenLimit()
		require.NotNil(t, got)
		require.Equal(t, 1024, *got)
	})

	t.Run("max_tokens zero ignored", func(t *testing.T) {
		c := ModelConfig{MaxTokens: 0}
		require.Nil(t, c.OutputTokenLimit())
	})

	t.Run("max_tokens negative ignored", func(t *testing.T) {
		c := ModelConfig{MaxTokens: -5}
		require.Nil(t, c.OutputTokenLimit())
	})

	t.Run("neither set returns nil", func(t *testing.T) {
		c := ModelConfig{}
		require.Nil(t, c.OutputTokenLimit())
	})
}

func TestRuntimeLimitsWithDefaults(t *testing.T) {
	t.Run("all zero falls back to defaults", func(t *testing.T) {
		got := (RuntimeLimits{}).WithDefaults()
		require.Equal(t, RuntimeLimits{
			MaxTeamTurns:         20,
			MaxCallsPerTeamTurn:  20,
			MaxAgentRounds:       200,
			MaxParallelTeams:     20,
			MaxParallelCalls:     20,
			MaxCoordinateRetries: 1,
			MaxActivationRetries: 1,
			MaxParallelTools:     20,
		}, got)
	})

	t.Run("each negative field falls back independently", func(t *testing.T) {
		in := RuntimeLimits{
			MaxTeamTurns:         -1,
			MaxCallsPerTeamTurn:  -1,
			MaxAgentRounds:       -1,
			MaxParallelTeams:     -1,
			MaxParallelCalls:     -1,
			MaxCoordinateRetries: -1,
			MaxActivationRetries: -1,
			MaxParallelTools:     -1,
		}
		got := in.WithDefaults()
		require.Equal(t, RuntimeLimits{
			MaxTeamTurns:         20,
			MaxCallsPerTeamTurn:  20,
			MaxAgentRounds:       200,
			MaxParallelTeams:     20,
			MaxParallelCalls:     20,
			MaxCoordinateRetries: 1,
			MaxActivationRetries: 1,
			MaxParallelTools:     20,
		}, got)
	})

	t.Run("positive values preserved", func(t *testing.T) {
		in := RuntimeLimits{
			MaxTeamTurns:         5,
			MaxCallsPerTeamTurn:  6,
			MaxAgentRounds:       7,
			MaxParallelTeams:     8,
			MaxParallelCalls:     9,
			MaxCoordinateRetries: 10,
			MaxActivationRetries: 11,
			MaxParallelTools:     12,
		}
		got := in.WithDefaults()
		require.Equal(t, in, got)
	})
}

func TestRuntimeLimitsUnmarshalJSONLegacyAliases(t *testing.T) {
	t.Run("legacy names migrate to current fields", func(t *testing.T) {
		var l RuntimeLimits
		err := json.Unmarshal([]byte(`{
			"max_call_turns": 5,
			"max_tool_calls": 6,
			"max_parallel_calls": 7
		}`), &l)
		require.NoError(t, err)
		require.Equal(t, 5, l.MaxCallsPerTeamTurn)
		require.Equal(t, 6, l.MaxAgentRounds)
		require.Equal(t, 7, l.MaxParallelCalls)
	})

	t.Run("current fields take priority over legacy", func(t *testing.T) {
		var l RuntimeLimits
		err := json.Unmarshal([]byte(`{
			"max_calls_per_team_turn": 100,
			"max_call_turns": 5
		}`), &l)
		require.NoError(t, err)
		require.Equal(t, 100, l.MaxCallsPerTeamTurn)
	})

	t.Run("legacy zero does not override current", func(t *testing.T) {
		var l RuntimeLimits
		err := json.Unmarshal([]byte(`{
			"max_calls_per_team_turn": 100,
			"max_call_turns": 0
		}`), &l)
		require.NoError(t, err)
		require.Equal(t, 100, l.MaxCallsPerTeamTurn)
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		var l RuntimeLimits
		err := json.Unmarshal([]byte(`{`), &l)
		require.Error(t, err)
	})
}

func TestContentPartMarshalUnmarshal(t *testing.T) {
	t.Run("text part roundtrip", func(t *testing.T) {
		in := ContentPart{Type: "text", Text: "hello"}
		data, err := json.Marshal(in)
		require.NoError(t, err)

		var out ContentPart
		require.NoError(t, json.Unmarshal(data, &out))
		require.Equal(t, in, out)
	})

	t.Run("image source roundtrip", func(t *testing.T) {
		in := ContentPart{
			Type: "image",
			Media: &MediaAttachment{
				ID:         "img-1",
				Name:       "photo.png",
				MIMEType:   "image/png",
				SizeBytes:  1024,
				SourceType: "base64",
				DataBase64: "aGVsbG8=",
			},
		}
		data, err := json.Marshal(in)
		require.NoError(t, err)

		// Marshal must emit the "source" form, not nested "media".
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		require.Contains(t, raw, "source")
		require.NotContains(t, raw, "media")

		var out ContentPart
		require.NoError(t, json.Unmarshal(data, &out))
		require.Equal(t, in.Type, out.Type)
		require.NotNil(t, out.Media)
		require.Equal(t, in.Media.ID, out.Media.ID)
		require.Equal(t, in.Media.Name, out.Media.Name)
		require.Equal(t, in.Media.MIMEType, out.Media.MIMEType)
		require.Equal(t, in.Media.SizeBytes, out.Media.SizeBytes)
		require.Equal(t, in.Media.SourceType, out.Media.SourceType)
		require.Equal(t, in.Media.DataBase64, out.Media.DataBase64)
	})

	t.Run("file roundtrip", func(t *testing.T) {
		in := ContentPart{
			Type: "file",
			Media: &MediaAttachment{
				ID:         "f-1",
				Name:       "report.pdf",
				MIMEType:   "application/pdf",
				SizeBytes:  2048,
				SourceType: "stored",
				StorageRef: "ref-123",
				SHA256:     "abc",
			},
		}
		data, err := json.Marshal(in)
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		require.Contains(t, raw, "file")
		require.NotContains(t, raw, "media")

		var out ContentPart
		require.NoError(t, json.Unmarshal(data, &out))
		require.NotNil(t, out.Media)
		require.Equal(t, "report.pdf", out.Media.Name)
		require.Equal(t, "ref-123", out.Media.StorageRef)
		require.Equal(t, "abc", out.Media.SHA256)
	})

	t.Run("unmarshal legacy media form", func(t *testing.T) {
		var out ContentPart
		err := json.Unmarshal([]byte(`{
			"type": "image",
			"media": {"id": "m-1", "name": "x.png", "mime_type": "image/png"}
		}`), &out)
		require.NoError(t, err)
		require.NotNil(t, out.Media)
		require.Equal(t, "m-1", out.Media.ID)
		require.Equal(t, "x.png", out.Media.Name)
	})

	t.Run("media kind filled from type", func(t *testing.T) {
		var out ContentPart
		err := json.Unmarshal([]byte(`{
			"type": "audio",
			"source": {"type": "base64", "data": "AAAA"}
		}`), &out)
		require.NoError(t, err)
		require.NotNil(t, out.Media)
		require.Equal(t, "audio", out.Media.Kind)
		require.Equal(t, "base64", out.Media.SourceType)
		require.Equal(t, "AAAA", out.Media.DataBase64)
	})
}

func TestRouteUnmarshalYAML(t *testing.T) {
	t.Run("short list form", func(t *testing.T) {
		var r Route
		err := yaml.Unmarshal([]byte("[research, fix]\n"), &r)
		require.NoError(t, err)
		require.Equal(t, NextProceed, r.Action)
		require.Equal(t, []string{"research", "fix"}, r.Teams)
	})

	t.Run("mapping form", func(t *testing.T) {
		var r Route
		err := yaml.Unmarshal([]byte(`
action: coordinate
teams: [research, fix]
reason: needs diagnosis
`), &r)
		require.NoError(t, err)
		require.Equal(t, NextCoordinate, r.Action)
		require.Equal(t, []string{"research", "fix"}, r.Teams)
		require.Equal(t, "needs diagnosis", r.Reason)
	})

	t.Run("mapping with caller team", func(t *testing.T) {
		var r Route
		err := yaml.Unmarshal([]byte(`
action: return
caller_team: coordinator
`), &r)
		require.NoError(t, err)
		require.Equal(t, NextReturn, r.Action)
		require.Equal(t, "coordinator", r.CallerTeam)
	})
}

func TestOutputSpecIsZero(t *testing.T) {
	tests := []struct {
		name string
		in   OutputSpec
		want bool
	}{
		{"empty", OutputSpec{}, true},
		{"from set", OutputSpec{From: "x"}, false},
		{"record set", OutputSpec{Record: "x"}, false},
		{"records set", OutputSpec{Records: []OutputBinding{{From: "a", Record: "b"}}}, false},
		{"publish set", OutputSpec{Publish: true}, false},
		{"scope only", OutputSpec{Scope: "flow"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.in.IsZero())
		})
	}
}

func TestCallValidateRejectsInvalidReplayPolicy(t *testing.T) {
	tests := []struct {
		name string
		call Call
	}{
		{
			name: "command invalid policy",
			call: Call{
				ID:   "c",
				Type: CallCommand,
				Command: &CommandSpec{
					Command:      "go test ./...",
					ReplayPolicy: "bogus",
				},
			},
		},
		{
			name: "webhook invalid policy",
			call: Call{
				ID:   "w",
				Type: CallWebhook,
				Webhook: &WebhookSpec{
					URL:          "https://example.com/hook",
					ReplayPolicy: "bogus",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call.Validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), "unsupported replay_policy")
		})
	}
}

func TestCallValidateAcceptsValidReplayPolicies(t *testing.T) {
	tests := []struct {
		name string
		call Call
	}{
		{
			name: "command never",
			call: Call{
				ID:   "c",
				Type: CallCommand,
				Command: &CommandSpec{
					Command:      "go test ./...",
					ReplayPolicy: ReplayNever,
				},
			},
		},
		{
			name: "command allow",
			call: Call{
				ID:   "c",
				Type: CallCommand,
				Command: &CommandSpec{
					Command:      "go test ./...",
					ReplayPolicy: ReplayAllow,
				},
			},
		},
		{
			name: "webhook allow",
			call: Call{
				ID:   "w",
				Type: CallWebhook,
				Webhook: &WebhookSpec{
					URL:          "https://example.com/hook",
					ReplayPolicy: ReplayAllow,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, tt.call.Validate())
		})
	}
}
