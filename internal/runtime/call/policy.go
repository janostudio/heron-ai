package call

import (
	"os"
	"strconv"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func commandEnv(req types.CallRequest) []string {
	env := make([]string, 0, len(req.Call.Command.Env)+1)
	for name, value := range req.Call.Command.Env {
		env = append(env, name+"="+resolveTemplate(value, req))
	}
	if key := req.Call.Command.IdempotencyKey; key != "" {
		env = append(env, "HERON_IDEMPOTENCY_KEY="+resolveTemplate(key, req))
	}
	return append(os.Environ(), env...)
}

func resolveTemplate(value string, req types.CallRequest) string {
	replacements := map[string]string{
		"${flow_session_id}": req.FlowSession.ID,
		"${flow_turn_id}":    req.FlowTurn.ID,
		"${team_id}":         req.TeamTurn.TeamID,
		"${team_turn_id}":    req.TeamTurn.ID,
		"${call_id}":         req.Call.ID,
		"${call_turn_id}":    req.CallTurnID,
		"${attempt}":         strconv.Itoa(req.Attempt),
		"${recovery_of}":     req.RecoveryOf,
	}
	for placeholder, replacement := range replacements {
		value = strings.ReplaceAll(value, placeholder, replacement)
	}
	return value
}
