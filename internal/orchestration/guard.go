package orchestration

type LoopGuard struct {
	maxRounds int
	current   int
}

func NewLoopGuard(maxRounds int) *LoopGuard {
	return &LoopGuard{maxRounds: maxRounds}
}

func (g *LoopGuard) CanContinue() bool {
	if g.maxRounds <= 0 {
		return true // unlimited
	}
	return g.current < g.maxRounds
}

func (g *LoopGuard) Increment() {
	g.current++
}

func (g *LoopGuard) Reset() {
	g.current = 0
}

func (g *LoopGuard) Current() int {
	return g.current
}

func (g *LoopGuard) Max() int {
	return g.maxRounds
}
