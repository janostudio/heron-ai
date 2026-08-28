package types

import (
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// CallType identifies the execution target of one Team call.
//
// A Call is a Team execution item. It may invoke an Agent, a Shell Command,
// or a Webhook. It is not an additional orchestration layer.
type CallType string

const (
	CallAgent   CallType = "agent"
	CallCommand CallType = "command"
	CallWebhook CallType = "webhook"
)

// CommandSpec describes a command call.
type CommandSpec struct {
	Command        string            `yaml:"command" json:"command"`
	Args           []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env            map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Timeout        string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	IdempotencyKey string            `yaml:"idempotency_key,omitempty" json:"idempotency_key,omitempty"`
	ReplayPolicy   string            `yaml:"replay_policy,omitempty" json:"replay_policy,omitempty"` // never | idempotent | allow
}

// WebhookSpec describes a webhook call.
type WebhookSpec struct {
	URL            string            `yaml:"url" json:"url"`
	Method         string            `yaml:"method,omitempty" json:"method,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Timeout        string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	IdempotencyKey string            `yaml:"idempotency_key,omitempty" json:"idempotency_key,omitempty"`
	ReplayPolicy   string            `yaml:"replay_policy,omitempty" json:"replay_policy,omitempty"` // never | idempotent | allow
}

const (
	ReplayNever      = "never"
	ReplayIdempotent = "idempotent"
	ReplayAllow      = "allow"
)

// InputSpec describes the context a Team or Call may receive.
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

// InputBinding selects one named SharedRecord visible to a Team or Call.
//
// From is intentionally a string reference for now. The config validator will
// resolve whether it refers to the flow, team, call, or a named producer.
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

// OutputSpec describes which results a Call or Team publishes.
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

// OutputBinding promotes a call result to a named SharedRecord.
type OutputBinding struct {
	From   string `yaml:"from" json:"from"`
	Record string `yaml:"record" json:"record"`
	Scope  string `yaml:"scope,omitempty" json:"scope,omitempty"`
}

// Call is one Team execution definition.
//
// AgentID points to an Agent Definition when Type is CallAgent. Command and
// Webhook contain the corresponding deterministic execution specification.
type Call struct {
	ID             string       `yaml:"id" json:"id"`
	Type           CallType     `yaml:"type" json:"type"`
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
func (c *Call) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		ID             string            `yaml:"id"`
		Type           CallType          `yaml:"type"`
		AgentID        string            `yaml:"agent"`
		Command        yaml.Node         `yaml:"command"`
		Webhook        yaml.Node         `yaml:"webhook"`
		URL            string            `yaml:"url"`
		Method         string            `yaml:"method"`
		Headers        map[string]string `yaml:"headers"`
		IdempotencyKey string            `yaml:"idempotency_key"`
		ReplayPolicy   string            `yaml:"replay_policy"`
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
				return fmt.Errorf("call %q: decode command: %w", raw.ID, err)
			}
			command = &CommandSpec{Command: commandText}
		case yaml.MappingNode:
			var spec CommandSpec
			if err := raw.Command.Decode(&spec); err != nil {
				return fmt.Errorf("call %q: decode command: %w", raw.ID, err)
			}
			command = &spec
		default:
			return fmt.Errorf("call %q: command must be a string or mapping", raw.ID)
		}
	}

	var webhook *WebhookSpec
	if raw.Webhook.Kind != 0 {
		if raw.Webhook.Kind != yaml.MappingNode {
			return fmt.Errorf("call %q: webhook must be a mapping", raw.ID)
		}
		var spec WebhookSpec
		if err := raw.Webhook.Decode(&spec); err != nil {
			return fmt.Errorf("call %q: decode webhook: %w", raw.ID, err)
		}
		webhook = &spec
	}

	*c = Call{
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
	if c.Webhook == nil && strings.TrimSpace(raw.URL) != "" {
		c.Webhook = &WebhookSpec{
			URL:            raw.URL,
			Method:         raw.Method,
			Headers:        raw.Headers,
			Timeout:        raw.Timeout,
			IdempotencyKey: raw.IdempotencyKey,
			ReplayPolicy:   raw.ReplayPolicy,
		}
	}
	return nil
}

// Validate checks the type-specific invariants that are independent of the
// complete Flow graph.
func (c Call) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("call id is required")
	}

	switch c.Type {
	case CallAgent:
		if strings.TrimSpace(c.AgentID) == "" {
			return fmt.Errorf("agent call %q: agent is required", c.ID)
		}
		if c.Command != nil || c.Webhook != nil {
			return fmt.Errorf("agent call %q: command/webhook must be empty", c.ID)
		}
	case CallCommand:
		if c.Command == nil || strings.TrimSpace(c.Command.Command) == "" {
			return fmt.Errorf("command call %q: command is required", c.ID)
		}
		if err := validateReplayPolicy(c.Command.ReplayPolicy, c.Command.IdempotencyKey); err != nil {
			return fmt.Errorf("command call %q: %w", c.ID, err)
		}
		if strings.TrimSpace(c.AgentID) != "" || c.Webhook != nil {
			return fmt.Errorf("command call %q: agent/webhook must be empty", c.ID)
		}
	case CallWebhook:
		if c.Webhook == nil || strings.TrimSpace(c.Webhook.URL) == "" {
			return fmt.Errorf("webhook call %q: url is required", c.ID)
		}
		if err := validateReplayPolicy(c.Webhook.ReplayPolicy, c.Webhook.IdempotencyKey); err != nil {
			return fmt.Errorf("webhook call %q: %w", c.ID, err)
		}
		if _, err := url.ParseRequestURI(c.Webhook.URL); err != nil {
			return fmt.Errorf("webhook call %q: invalid url: %w", c.ID, err)
		}
		if strings.TrimSpace(c.AgentID) != "" || c.Command != nil {
			return fmt.Errorf("webhook call %q: agent/command must be empty", c.ID)
		}
	default:
		return fmt.Errorf("call %q: unsupported type %q", c.ID, c.Type)
	}

	return nil
}

func validateReplayPolicy(policy, idempotencyKey string) error {
	switch policy {
	case "", ReplayNever, ReplayAllow:
		return nil
	case ReplayIdempotent:
		if strings.TrimSpace(idempotencyKey) == "" {
			return fmt.Errorf("replay_policy idempotent requires idempotency_key")
		}
		return nil
	default:
		return fmt.Errorf("unsupported replay_policy %q", policy)
	}
}
