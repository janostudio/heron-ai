package types

type FlowConfig struct {
	Name          string      `yaml:"name" json:"name"`
	LoopMaxRounds int         `yaml:"loop_max_rounds" json:"loop_max_rounds"`
	Stages        []FlowStage `yaml:"stages" json:"stages"`
}

type FlowStage struct {
	Name     string            `yaml:"name" json:"name"`
	Team     string            `yaml:"team" json:"team"`
	OnSignal FlowStageSignals  `yaml:"on_signal" json:"on_signal"`
}

type FlowStageSignals struct {
	Continue  *string `yaml:"continue,omitempty" json:"continue,omitempty"`
	WaitInput *string `yaml:"wait_input,omitempty" json:"wait_input,omitempty"`
}
