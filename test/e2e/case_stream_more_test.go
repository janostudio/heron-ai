//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestStream3SubscriptionSurvivesUntilConclusion covers I-STREAM-3: after a
// handoff the subscription has to keep delivering until the conclusion arrives.
// A subscriber that stops at the handoff turn sees an empty reply and has no
// way to know more is coming.
func TestStream3SubscriptionSurvivesUntilConclusion(t *testing.T) {
	judge(t, "I-STREAM-3", "stream")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-STREAM-3", fixture: "dispatch"})
	e.Mock.script(dispatchScript(300*time.Millisecond, true)...)

	result := e.runTurn(map[string]any{"input": "当前版本是多少"})
	// Subscribe after the handoff turn returned: the stream is durable, so the
	// conclusion turn's frames must still arrive.
	subscription := startStream(t, e, result.Session.ID, 0)
	frames := subscription.waitUntil(25*time.Second, func(frame sseFrame) bool {
		payload := decodePayload(frame.Data)
		if payload == nil {
			return false
		}
		if intermediate, _ := payload["intermediate"].(bool); intermediate {
			return false
		}
		reply, _ := payload["reply"].(string)
		return strings.TrimSpace(reply) != ""
	})

	conclusion := ""
	for _, frame := range frames {
		payload := decodePayload(frame.Data)
		if payload == nil {
			continue
		}
		if intermediate, _ := payload["intermediate"].(bool); intermediate {
			continue
		}
		if reply, _ := payload["reply"].(string); strings.TrimSpace(reply) != "" {
			conclusion = strings.TrimSpace(reply)
		}
	}

	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/stream", Status: http.StatusOK},
		result.Session.ID, map[string]any{"frames": len(frames), "conclusion": conclusion})

	if conclusion == "" {
		t.Fatalf("subscription opened after the handoff never delivered a conclusion frame (%d frames seen)", len(frames))
	}
}

// TestLoop3StreamWithEmptyFramesCompletes covers I-LOOP-3: providers emit
// usage-only frames with an empty choices array mid-stream, and the turn has to
// keep reading through them. A loop that stops there reports an empty answer on
// a perfectly good response.
func TestLoop3StreamWithEmptyFramesCompletes(t *testing.T) {
	judge(t, "I-LOOP-3", "loop")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-LOOP-3", streamModel: true})
	e.Mock.script(step{text: "流式答复", stream: true, emptyChoices: 2})

	result := e.runTurn(map[string]any{"input": "用流式回答"})
	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, turnTerminated)

	reply := replyOf(result)
	if reply != "流式答复" {
		t.Fatalf("streamed reply=%q; want 流式答复 (empty choices frames must not end the read)", reply)
	}

	tokens := 0.0
	for _, ev := range events {
		if ev.Type != "session.agent.model_response" {
			continue
		}
		payload, _ := ev.Raw["payload"].(map[string]any)
		usage, _ := payload["usage"].(map[string]any)
		if value, ok := usage["total_tokens"].(float64); ok {
			tokens = value
		}
	}
	if tokens <= 0 {
		t.Fatalf("streamed response reported %v total tokens; usage must survive the empty choices frames", tokens)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"reply": reply, "total_tokens": tokens})
}

// decodePayload parses a session frame's payload.
func decodePayload(data string) map[string]any {
	var envelope struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return nil
	}
	return envelope.Payload
}
