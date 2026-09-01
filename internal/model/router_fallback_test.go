package model

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// fakeProvider is a controllable types.ModelProvider used to test fallback
// without constructing real HTTP clients.
type fakeProvider struct {
	name   string
	chatFn func() (*types.ChatResponse, error)
}

func (f *fakeProvider) Chat(context.Context, []types.Message, []types.JSONSchema, types.ModelConfig) (*types.ChatResponse, error) {
	return f.chatFn()
}

func (f *fakeProvider) ChatStream(context.Context, []types.Message, []types.JSONSchema, types.ModelConfig) (<-chan types.ChatChunk, error) {
	resp, err := f.chatFn()
	if err != nil {
		return nil, err
	}
	ch := make(chan types.ChatChunk, 1)
	ch <- types.ChatChunk{Text: resp.Text, Finished: true}
	close(ch)
	return ch, nil
}

func newTestRouter(primary string, profiles []types.ModelProfile, providers map[string]types.ModelProvider, now func() time.Time) *ProviderRouter {
	r := &ProviderRouter{
		defaultModel: primary,
		profiles:     make(map[string]types.ModelProfile),
		providers:    make(map[string]types.ModelProvider),
		cooldown:     make(map[string]time.Time),
		now:          now,
	}
	if r.now == nil {
		r.now = time.Now
	}
	for _, p := range profiles {
		r.profiles[p.Name] = p
		r.profiles[p.ID] = p
	}
	for name, provider := range providers {
		r.providers[name] = provider
	}
	return r
}

func okResponse(modelName string) (*types.ChatResponse, error) {
	return &types.ChatResponse{Text: "ok", Model: modelName}, nil
}

func errProvider(kind types.ProviderErrorKind) (*types.ChatResponse, error) {
	return nil, &types.ProviderError{Kind: kind, Message: "boom"}
}

func TestFallbackRateLimitSwitchesToBackup(t *testing.T) {
	router := newTestRouter("primary",
		[]types.ModelProfile{{ID: "primary", Name: "primary", Fallback: []string{"backup"}}},
		map[string]types.ModelProvider{
			"primary": &fakeProvider{name: "primary", chatFn: func() (*types.ChatResponse, error) { return errProvider(types.KindRateLimit) }},
			"backup":  &fakeProvider{name: "backup", chatFn: func() (*types.ChatResponse, error) { return okResponse("backup") }},
		},
		nil,
	)

	resp, err := router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	if err != nil {
		t.Fatalf("Chat error = %v, want nil", err)
	}
	if resp.Model != "backup" {
		t.Fatalf("Model = %q, want backup", resp.Model)
	}
}

func TestFallbackServerErrorSwitchesToBackup(t *testing.T) {
	router := newTestRouter("primary",
		[]types.ModelProfile{{ID: "primary", Name: "primary", Fallback: []string{"backup"}}},
		map[string]types.ModelProvider{
			"primary": &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return errProvider(types.KindServerError) }},
			"backup":  &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return okResponse("backup") }},
		},
		nil,
	)

	resp, err := router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	if err != nil {
		t.Fatalf("Chat error = %v, want nil", err)
	}
	if resp.Model != "backup" {
		t.Fatalf("Model = %q, want backup", resp.Model)
	}
}

func TestFallbackAuthReturnsImmediately(t *testing.T) {
	calledBackup := false
	router := newTestRouter("primary",
		[]types.ModelProfile{{ID: "primary", Name: "primary", Fallback: []string{"backup"}}},
		map[string]types.ModelProvider{
			"primary": &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return errProvider(types.KindAuth) }},
			"backup":  &fakeProvider{chatFn: func() (*types.ChatResponse, error) { calledBackup = true; return okResponse("backup") }},
		},
		nil,
	)

	_, err := router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	if err == nil {
		t.Fatal("Chat error = nil, want auth error")
	}
	var pe *types.ProviderError
	if !errors.As(err, &pe) || pe.Kind != types.KindAuth {
		t.Fatalf("err = %v, want KindAuth ProviderError", err)
	}
	if calledBackup {
		t.Fatal("backup was called, want immediate return")
	}
}

func TestFallbackBadRequestReturnsImmediately(t *testing.T) {
	calledBackup := false
	router := newTestRouter("primary",
		[]types.ModelProfile{{ID: "primary", Name: "primary", Fallback: []string{"backup"}}},
		map[string]types.ModelProvider{
			"primary": &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return errProvider(types.KindBadRequest) }},
			"backup":  &fakeProvider{chatFn: func() (*types.ChatResponse, error) { calledBackup = true; return okResponse("backup") }},
		},
		nil,
	)

	_, err := router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	if err == nil {
		t.Fatal("Chat error = nil, want bad_request error")
	}
	if calledBackup {
		t.Fatal("backup was called, want immediate return")
	}
}

func TestFallbackAllExhausted(t *testing.T) {
	router := newTestRouter("primary",
		[]types.ModelProfile{{ID: "primary", Name: "primary", Fallback: []string{"backup1", "backup2"}}},
		map[string]types.ModelProvider{
			"primary": &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return errProvider(types.KindRateLimit) }},
			"backup1": &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return errProvider(types.KindServerError) }},
			"backup2": &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return errProvider(types.KindTimeout) }},
		},
		nil,
	)

	_, err := router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	if err == nil {
		t.Fatal("Chat error = nil, want exhausted error")
	}
	if !strings.Contains(err.Error(), "all fallback models exhausted") {
		t.Fatalf("err = %q, want to contain 'all fallback models exhausted'", err.Error())
	}
}

func TestFallbackSkipsMissingBackup(t *testing.T) {
	// backup1 is declared but has no provider; backup2 is available.
	router := newTestRouter("primary",
		[]types.ModelProfile{{ID: "primary", Name: "primary", Fallback: []string{"missing", "backup2"}}},
		map[string]types.ModelProvider{
			"primary": &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return errProvider(types.KindRateLimit) }},
			"backup2": &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return okResponse("backup2") }},
		},
		nil,
	)

	resp, err := router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	if err != nil {
		t.Fatalf("Chat error = %v, want nil", err)
	}
	if resp.Model != "backup2" {
		t.Fatalf("Model = %q, want backup2", resp.Model)
	}
}

func TestFallbackSkipsMissingBackupThenExhausted(t *testing.T) {
	router := newTestRouter("primary",
		[]types.ModelProfile{{ID: "primary", Name: "primary", Fallback: []string{"missing"}}},
		map[string]types.ModelProvider{
			"primary": &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return errProvider(types.KindRateLimit) }},
		},
		nil,
	)

	_, err := router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	if err == nil {
		t.Fatal("Chat error = nil, want exhausted error")
	}
	if !strings.Contains(err.Error(), "all fallback models exhausted") {
		t.Fatalf("err = %q, want 'all fallback models exhausted'", err.Error())
	}
}

func TestCooldownSkipsBackupThenRecovers(t *testing.T) {
	now := time.Now()
	fakeNow := func() time.Time { return now }

	// primary -> backup. backup first fails with rate limit, then recovers.
	backupCalls := 0
	router := newTestRouter("primary",
		[]types.ModelProfile{
			{ID: "primary", Name: "primary", Fallback: []string{"backup"}},
			{ID: "backup", Name: "backup", CooldownSeconds: 60},
		},
		map[string]types.ModelProvider{
			"primary": &fakeProvider{chatFn: func() (*types.ChatResponse, error) { return errProvider(types.KindRateLimit) }},
			"backup": &fakeProvider{chatFn: func() (*types.ChatResponse, error) {
				backupCalls++
				if backupCalls == 1 {
					return errProvider(types.KindRateLimit)
				}
				return okResponse("backup")
			}},
		},
		fakeNow,
	)

	// First call: primary rate-limits, backup rate-limits, exhausted.
	_, err := router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	if err == nil {
		t.Fatal("first Chat error = nil, want exhausted")
	}

	// Immediately retry: backup is now in cooldown, so only primary is tried.
	_, err = router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	if err == nil {
		t.Fatal("second Chat error = nil, want exhausted (backup in cooldown)")
	}
	if backupCalls != 1 {
		t.Fatalf("backupCalls = %d, want 1 (backup should be skipped while cooling)", backupCalls)
	}

	// Advance past cooldown: backup recovers.
	now = now.Add(61 * time.Second)
	resp, err := router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	if err != nil {
		t.Fatalf("third Chat error = %v, want nil", err)
	}
	if resp.Model != "backup" {
		t.Fatalf("Model = %q, want backup", resp.Model)
	}
}

func TestPrimaryAlwaysTried(t *testing.T) {
	now := time.Now()
	fakeNow := func() time.Time { return now }

	// primary fails, then stays failed; it must still be tried on every call
	// even though it was marked cooling.
	primaryCalls := 0
	router := newTestRouter("primary",
		[]types.ModelProfile{{ID: "primary", Name: "primary", CooldownSeconds: 60}},
		map[string]types.ModelProvider{
			"primary": &fakeProvider{chatFn: func() (*types.ChatResponse, error) {
				primaryCalls++
				return errProvider(types.KindRateLimit)
			}},
		},
		fakeNow,
	)

	_, _ = router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})
	_, _ = router.Chat(context.Background(), nil, nil, types.ModelConfig{Model: "primary"})

	if primaryCalls != 2 {
		t.Fatalf("primaryCalls = %d, want 2 (primary must always be tried)", primaryCalls)
	}
}
