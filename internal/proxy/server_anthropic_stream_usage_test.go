package proxy

import (
	"encoding/json"
	"testing"
)

func TestApplyAnthropicStreamingUsage_NoOpWhenNotStream(t *testing.T) {
	body := []byte(`{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"Hello"}]}`)
	tr := newAnthropicCompatTransformer("claude-test")

	applyAnthropicStreamingUsage(tr, false, body)

	if tr.inputTokens != 0 {
		t.Fatalf("expected inputTokens unchanged (0), got %d", tr.inputTokens)
	}
	if tr.tokenModel != "" {
		t.Fatalf("expected tokenModel unchanged (empty), got %q", tr.tokenModel)
	}
}

func TestApplyAnthropicStreamingUsage_SetsInputTokensFromFinalBody(t *testing.T) {
	model := "gpt-3.5-turbo"
	messages := []any{
		map[string]any{"role": "user", "content": "Hello"},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "world"}}},
	}
	body, err := json.Marshal(map[string]any{
		"model":    model,
		"messages": messages,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	nonStringContent, err := json.Marshal(messages[1].(map[string]any)["content"])
	if err != nil {
		t.Fatalf("marshal non-string content: %v", err)
	}

	want := estimateTokens("Hello", model) + estimateTokens(string(nonStringContent), model)

	tr := newAnthropicCompatTransformer("claude-test")
	applyAnthropicStreamingUsage(tr, true, body)

	if tr.inputTokens != want {
		t.Fatalf("inputTokens mismatch: got %d want %d", tr.inputTokens, want)
	}
	if tr.tokenModel != model {
		t.Fatalf("tokenModel mismatch: got %q want %q", tr.tokenModel, model)
	}
}
