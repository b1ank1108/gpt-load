package proxy

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRestoreToolCallsInChatCompletion_Success(t *testing.T) {
	trigger := "TOOLCALL_COMPAT_TRIGGER_0123456789abcdef"
	content := "Hello\n\n" + trigger + "\n" +
		"<function_calls>" +
		"<function_call><tool>get_weather</tool><args_json><![CDATA[{\"city\":\"SF\"}]]></args_json></function_call>" +
		"</function_calls>"

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

	out, ok := restoreToolCallsInChatCompletion(in, trigger)
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

	out, ok := restoreToolCallsInChatCompletion(in, "TOOLCALL_COMPAT_TRIGGER_x")
	if ok {
		t.Fatalf("expected restore not ok")
	}
	if !bytes.Equal(out, in) {
		t.Fatalf("expected passthrough body")
	}
}

func TestRestoreToolCallsInChatCompletion_PassthroughWhenMalformedXML(t *testing.T) {
	trigger := "TOOLCALL_COMPAT_TRIGGER_x"
	content := "Hello\n" + trigger + "\n<function_calls><function_call><tool>get_weather</tool></function_call>"

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

	out, ok := restoreToolCallsInChatCompletion(in, trigger)
	if ok {
		t.Fatalf("expected restore not ok")
	}
	if !bytes.Equal(out, in) {
		t.Fatalf("expected passthrough body")
	}
}

func TestExtractToolcallCompatFunctionCalls_EmptyToolName(t *testing.T) {
	trigger := "TOOLCALL_COMPAT_TRIGGER_x"
	content := trigger + "\n<function_calls><function_call><tool> </tool><args_json><![CDATA[{}]]></args_json></function_call></function_calls>"

	_, calls, ok := extractToolcallCompatFunctionCalls(content, trigger)
	if ok || calls != nil {
		t.Fatalf("expected extract not ok, got ok=%v calls=%#v", ok, calls)
	}
}
