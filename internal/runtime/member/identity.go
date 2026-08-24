package member

import "github.com/heron-ai/heron-engine/pkg/types"

func memberTurnID(req types.MemberRequest) string {
	if req.MemberTurnID != "" {
		return req.MemberTurnID
	}
	return req.TeamTurn.ID + ":" + req.Member.ID
}
