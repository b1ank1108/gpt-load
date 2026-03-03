package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRestoreToolCallsInChatCompletion_Success(t *testing.T) {
	idSeed := "0123456789abcdef"
	trigger := "<<CALL_aa11bb>>"
	content := "Hello\n\n" +
		trigger + "\n" +
		"<invoke name=\"get_weather\">\n" +
		"<parameter name=\"city\">\"SF\"</parameter>\n" +
		"</invoke>\n"

	in, err := json.Marshal(map[string]any{
		"id": "chatcmpl_1",
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	out, ok := restoreToolCallsInChatCompletion(in, idSeed, trigger)
	if !ok {
		t.Fatalf("expected restore ok")
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	choice0 := payload["choices"].([]any)[0].(map[string]any)
	if choice0["finish_reason"] != "tool_calls" {
		t.Fatalf("unexpected finish_reason: %#v", choice0["finish_reason"])
	}

	msg := choice0["message"].(map[string]any)
	if msg["content"] != "Hello" {
		t.Fatalf("unexpected content: %#v", msg["content"])
	}

	toolCalls := msg["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(toolCalls))
	}
	call0 := toolCalls[0].(map[string]any)
	if call0["id"] != "call_0123456789ab_1" {
		t.Fatalf("unexpected tool_call id: %#v", call0["id"])
	}
	fn := call0["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"SF"}` {
		t.Fatalf("unexpected tool_call function: %#v", fn)
	}
}

func TestRestoreToolCallsInChatCompletion_PassthroughWhenNoTrigger(t *testing.T) {
	trigger := "<<CALL_aa11bb>>"
	in, err := json.Marshal(map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "Hello",
				},
				"finish_reason": "stop",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	out, ok := restoreToolCallsInChatCompletion(in, "compat", trigger)
	if ok {
		t.Fatalf("expected restore not ok")
	}
	if !bytes.Equal(out, in) {
		t.Fatalf("expected passthrough body")
	}
}

func TestRestoreToolCallsInChatCompletion_ParseFailure_StripsTrigger(t *testing.T) {
	idSeed := "compat"
	trigger := "<<CALL_aa11bb>>"
	content := "Hello\n\n" + trigger + "\n" +
		"<invoke name=\"get_weather\">\n" +
		"<parameter name=\"city\">{\"bad\":</parameter>\n"

	in, err := json.Marshal(map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	out, ok := restoreToolCallsInChatCompletion(in, idSeed, trigger)
	if !ok {
		t.Fatalf("expected restore ok")
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	choice0 := payload["choices"].([]any)[0].(map[string]any)
	if choice0["finish_reason"] != "stop" {
		t.Fatalf("unexpected finish_reason: %#v", choice0["finish_reason"])
	}

	msg := choice0["message"].(map[string]any)
	contentOut, _ := msg["content"].(string)
	if strings.Contains(contentOut, trigger) {
		t.Fatalf("expected trigger stripped, got: %q", contentOut)
	}
	if !strings.Contains(contentOut, "<invoke") {
		t.Fatalf("expected invoke to fall back as text, got: %q", contentOut)
	}
	if _, ok := msg["tool_calls"]; ok {
		t.Fatalf("expected no tool_calls on parse failure")
	}
}
