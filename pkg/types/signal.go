package types

// Signal represents the outcome signal from an agent or team execution
type Signal string

const (
	SignalContinue        Signal = "continue"
	SignalWaitInput       Signal = "wait_input"
	SignalGoalAchieved    Signal = "goal_achieved"
	SignalGoalFailed      Signal = "goal_failed"
	SignalGoalImpossible  Signal = "goal_impossible"
)

// RunAction represents what the flow engine should do next
type RunAction int

const (
	ActionContinue  RunAction = iota
	ActionWaitInput
	ActionEnd
)
