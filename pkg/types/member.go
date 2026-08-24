package types

import (
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// MemberType identifies how a Team member is executed.
//
// V1 deliberately has only three member types. A persistent, actively
// communicating "agent" is reserved for a future runtime and is not a valid
// V1 member type.
type MemberType string

const (
	MemberSubagent MemberType = "subagent"
	MemberCommand  MemberType = "command"
	MemberWebhook  MemberType = "webhook"
)

// CommandSpec describes a command member.
type CommandSpec struct {
	Command string            `yaml:"command" json:"command"`
	Args    []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Timeout string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// WebhookSpec describes a webhook member.
type WebhookSpec struct {
	URL     string            `yaml:"url" json:"url"`
	Method  string            `yaml:"method,omitempty" json:"method,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Timeout string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// InputSpec describes the context a Team or Member may receive.
//
// V1 accepts both a compact mapping form and an explicit list of bindings:
//
//	inputs:
//	  user_message: true
//	  flow_records: [DiagnosisReport]
//
//	inputs:
//	  - from: diagnose
//	    record: DiagnosisReport
//
// The loader normalizes both forms into this structure.
type InputSpec struct {
	UserMessage     bool           `yaml:"user_message,omitempty" json:"user_message,omitempty"`
	TeamUserMessage bool           `yaml:"team_user_message,omitempty" json:"team_user_message,omitempty"`
	TeamMemory      string         `yaml:"team_memory,omitempty" json:"team_memory,omitempty"`
	FlowRecords     []string       `yaml:"flow_records,omitempty" json:"flow_records,omitempty"`
	TeamRecords     []string       `yaml:"team_records,omitempty" json:"team_records,omitempty"`
	Records         []InputBinding `yaml:"records,omitempty" json:"records,omitempty"`
}

// UnmarshalYAML supports both the compact map and explicit list forms.
func (s *InputSpec) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.SequenceNode {
		var records []InputBinding
		if err := node.Decode(&records); err != nil {
			return err
		}
		s.Records = records
		return nil
	}

	type inputSpec InputSpec
	var decoded inputSpec
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*s = InputSpec(decoded)
	return nil
}

// InputBinding selects one named SharedRecord visible to a Team or Member.
//
// From is intentionally a string reference for now. The config validator will
// resolve whether it refers to the flow, team, member, or a named producer.
type InputBinding struct {
	From            string     `yaml:"from,omitempty" json:"from,omitempty"`
	Record          string     `yaml:"record,omitempty" json:"record,omitempty"`
	As              string     `yaml:"as,omitempty" json:"as,omitempty"`
	UserMessage     bool       `yaml:"user_message,omitempty" json:"user_message,omitempty"`
	TeamUserMessage bool       `yaml:"team_user_message,omitempty" json:"team_user_message,omitempty"`
	TeamMemory      string     `yaml:"team_memory,omitempty" json:"team_memory,omitempty"`
	View            RecordView `yaml:"view,omitempty" json:"view,omitempty"`
}

// RecordView limits which parts of a SharedRecord are exposed.
type RecordView struct {
	Include  []string `yaml:"include,omitempty" json:"include,omitempty"`
	MaxChars int      `yaml:"max_chars,omitempty" json:"max_chars,omitempty"`
	Adapter  string   `yaml:"adapter,omitempty" json:"adapter,omitempty"`
}

// OutputSpec describes which results a member or Team publishes.
//
// Record is the convenient one-record form. Records is the explicit form used
// when a Team publishes several named records.
type OutputSpec struct {
	From    string          `yaml:"from,omitempty" json:"from,omitempty"`
	Record  string          `yaml:"record,omitempty" json:"record,omitempty"`
	Scope   string          `yaml:"scope,omitempty" json:"scope,omitempty"`
	Records []OutputBinding `yaml:"records,omitempty" json:"records,omitempty"`
	Publish bool            `yaml:"publish,omitempty" json:"publish,omitempty"`
}

// OutputBinding promotes a member result to a named SharedRecord.
type OutputBinding struct {
	From   string `yaml:"from" json:"from"`
	Record string `yaml:"record" json:"record"`
	Scope  string `yaml:"scope,omitempty" json:"scope,omitempty"`
}

// Member is a Team execution member.
//
// AgentID points to an Agent Definition when Type is subagent. Command and
// Webhook contain the corresponding deterministic execution specification.
type Member struct {
	ID             string       `yaml:"id" json:"id"`
	Type           MemberType   `yaml:"type" json:"type"`
	AgentID        string       `yaml:"agent,omitempty" json:"agent,omitempty"`
	Command        *CommandSpec `yaml:"command,omitempty" json:"command,omitempty"`
	Webhook        *WebhookSpec `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	DependsOn      []string     `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Inputs         InputSpec    `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Output         OutputSpec   `yaml:"output,omitempty" json:"output,omitempty"`
	Responsibility string       `yaml:"responsibility,omitempty" json:"responsibility,omitempty"`
	Timeout        string       `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// UnmarshalYAML accepts the documented direct webhook form:
//
//	type: webhook
//	url: https://example.test/hook
//
// as well as the nested form:
//
//	webhook:
//	  url: https://example.test/hook
func (m *Member) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		ID             string            `yaml:"id"`
		Type           MemberType        `yaml:"type"`
		AgentID        string            `yaml:"agent"`
		Command        yaml.Node         `yaml:"command"`
		Webhook        yaml.Node         `yaml:"webhook"`
		URL            string            `yaml:"url"`
		Method         string            `yaml:"method"`
		Headers        map[string]string `yaml:"headers"`
		DependsOn      []string          `yaml:"depends_on"`
		Inputs         InputSpec         `yaml:"inputs"`
		Output         OutputSpec        `yaml:"output"`
		Responsibility string            `yaml:"responsibility"`
		Timeout        string            `yaml:"timeout"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}

	var command *CommandSpec
	if raw.Command.Kind != 0 {
		switch raw.Command.Kind {
		case yaml.ScalarNode:
			var commandText string
			if err := raw.Command.Decode(&commandText); err != nil {
				return fmt.Errorf("member %q: decode command: %w", raw.ID, err)
			}
			command = &CommandSpec{Command: commandText}
		case yaml.MappingNode:
			var spec CommandSpec
			if err := raw.Command.Decode(&spec); err != nil {
				return fmt.Errorf("member %q: decode command: %w", raw.ID, err)
			}
			command = &spec
		default:
			return fmt.Errorf("member %q: command must be a string or mapping", raw.ID)
		}
	}

	var webhook *WebhookSpec
	if raw.Webhook.Kind != 0 {
		if raw.Webhook.Kind != yaml.MappingNode {
			return fmt.Errorf("member %q: webhook must be a mapping", raw.ID)
		}
		var spec WebhookSpec
		if err := raw.Webhook.Decode(&spec); err != nil {
			return fmt.Errorf("member %q: decode webhook: %w", raw.ID, err)
		}
		webhook = &spec
	}

	*m = Member{
		ID:             raw.ID,
		Type:           raw.Type,
		AgentID:        raw.AgentID,
		Command:        command,
		Webhook:        webhook,
		DependsOn:      raw.DependsOn,
		Inputs:         raw.Inputs,
		Output:         raw.Output,
		Responsibility: raw.Responsibility,
		Timeout:        raw.Timeout,
	}
	if m.Webhook == nil && strings.TrimSpace(raw.URL) != "" {
		m.Webhook = &WebhookSpec{
			URL:     raw.URL,
			Method:  raw.Method,
			Headers: raw.Headers,
			Timeout: raw.Timeout,
		}
	}
	return nil
}

// Validate checks the type-specific invariants that are independent of the
// complete Flow graph.
func (m Member) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("member id is required")
	}

	switch m.Type {
	case MemberSubagent:
		if strings.TrimSpace(m.AgentID) == "" {
			return fmt.Errorf("subagent member %q: agent is required", m.ID)
		}
		if m.Command != nil || m.Webhook != nil {
			return fmt.Errorf("subagent member %q: command/webhook must be empty", m.ID)
		}
	case MemberCommand:
		if m.Command == nil || strings.TrimSpace(m.Command.Command) == "" {
			return fmt.Errorf("command member %q: command is required", m.ID)
		}
		if strings.TrimSpace(m.AgentID) != "" || m.Webhook != nil {
			return fmt.Errorf("command member %q: agent/webhook must be empty", m.ID)
		}
	case MemberWebhook:
		if m.Webhook == nil || strings.TrimSpace(m.Webhook.URL) == "" {
			return fmt.Errorf("webhook member %q: url is required", m.ID)
		}
		if _, err := url.ParseRequestURI(m.Webhook.URL); err != nil {
			return fmt.Errorf("webhook member %q: invalid url: %w", m.ID, err)
		}
		if strings.TrimSpace(m.AgentID) != "" || m.Command != nil {
			return fmt.Errorf("webhook member %q: agent/command must be empty", m.ID)
		}
	default:
		return fmt.Errorf("member %q: unsupported type %q", m.ID, m.Type)
	}

	return nil
}
