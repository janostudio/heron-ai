// Command mockllm is a minimal OpenAI-compatible mock LLM server for load
// testing Heron's HTTP path without a real model endpoint. It serves a single
// /tencent/v1/chat/completions route (matching the default models.json
// base_url http://127.0.0.1:15721/tencent/v1) and returns a fixed reply with a
// realistic usage shape.
package main

import (
	"encoding/json"
	"log"
	"net/http"
)

type chatCompletion struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   usage    `json:"usage"`
}

type choice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type usage struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
}

func main() {
	addr := ":15721"
	http.HandleFunc("/tencent/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		resp := chatCompletion{
			ID:      "chatcmpl-mock",
			Object:  "chat.completion",
			Created: 0,
			Model:   "mock",
			Choices: []choice{{
				Index:        0,
				Message:      message{Role: "assistant", Content: "mock 回答"},
				FinishReason: "stop",
			}},
			Usage: usage{
				PromptTokens:          1913,
				CompletionTokens:      192,
				TotalTokens:           2105,
				PromptCacheMissTokens: 1913,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	log.Printf("mock LLM listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
