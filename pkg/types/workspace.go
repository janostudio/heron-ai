package types

import "time"

// WorkspaceOperation is the audit fact for every real cwd read/write/test
// operation. It is not itself a cross-Team collaboration record.
type WorkspaceOperation struct {
	OperationID  string    `json:"operation_id"`
	TurnID       string    `json:"turn_id,omitempty"`
	Kind         string    `json:"kind"` // read | search | write | test | bash
	Path         string    `json:"path,omitempty"`
	Revision     string    `json:"revision,omitempty"`
	BaseRevision string    `json:"base_revision,omitempty"`
	Lines        []int     `json:"lines,omitempty"`
	Excerpt      string    `json:"excerpt,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	Command      string    `json:"command,omitempty"`
	ExitCode     int       `json:"exit_code,omitempty"`
	Truncated    bool      `json:"truncated,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}

// WorkspaceConfig declares the execution backend for a Flow/Team/Agent. It is
// resolved by three-level inheritance (agent -> team -> flow -> default local).
type WorkspaceConfig struct {
	Type string     `yaml:"type" json:"type"` // local | ssh | docker
	SSH  *SSHConfig `yaml:"ssh,omitempty" json:"ssh,omitempty"`
}

// SSHConfig holds connection parameters for a remote SSH workspace.
type SSHConfig struct {
	Host     string `yaml:"host" json:"host"`
	Port     int    `yaml:"port" json:"port"`
	User     string `yaml:"user" json:"user"`
	KeyPath  string `yaml:"key_path,omitempty" json:"key_path,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	Root     string `yaml:"root,omitempty" json:"root,omitempty"` // 缺省 /root/workspace
	// Insecure 显式跳过 host key 校验，仅内网/测试环境。默认 false 会走 known_hosts 校验。
	Insecure bool `yaml:"insecure,omitempty" json:"insecure,omitempty"`
}
