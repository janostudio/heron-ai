//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
)

// TestMedia1AttachmentReachesTheModel covers the whole point of accepting an
// attachment: it has to arrive at the model.
//
// The failure this catches is silent in the reply and loud in the product: the
// turn answers "图收到了" from the text alone, so nothing looks broken, while the
// image never left the process. Text and attachment have to be judged
// separately — a text-only assertion cannot see this.
func TestMedia1AttachmentReachesTheModel(t *testing.T) {
	judge(t, "I-MEDIA-1", "loop")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-MEDIA-1", fixture: "dispatch"})
	e.Mock.script(step{match: []string{"You are 接待", "看图"}, text: "图收到了"})

	e.runTurn(map[string]any{
		"input": "看图",
		"attachments": []map[string]any{{
			"source_type": "base64", "data_base64": "aGk=",
			"kind": "image", "mime_type": "image/png", "name": "direct.png",
		}},
	})

	requests := e.Mock.requests()
	if len(requests) == 0 {
		t.Fatal("the model was never called")
	}

	carried := false
	for _, req := range requests {
		if !strings.Contains(requestText(req), "看图") {
			continue
		}
		raw := string(req.Raw)
		if !strings.Contains(raw, "image_url") {
			t.Fatalf("the request that carried the text had no image part: %s", truncate(raw, 400))
		}
		if !strings.Contains(raw, "aGk=") {
			t.Fatalf("the image part carries no payload (base64 missing): %s", truncate(raw, 400))
		}
		carried = true
	}
	if !carried {
		t.Fatal("no request carried the text and the image together")
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		"", map[string]any{"requests": len(requests), "image_carried": carried})
}
