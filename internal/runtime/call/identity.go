package call

import "github.com/heron-ai/heron-engine/pkg/types"

func callTurnID(req types.CallRequest) string {
	if req.CallTurnID != "" {
		return req.CallTurnID
	}
	return req.TeamTurn.ID + ":" + req.Call.ID
}
