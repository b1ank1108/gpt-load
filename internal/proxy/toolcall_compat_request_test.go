package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPreprocessToolcallCompatChatCompletionsRequest_PassthroughWhenNoSignals(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)

	out, meta, apiErr := preprocessToolcallCompatChatCompletionsRequest(in)
	if apiErr != nil {
		t.Fatalf("unexpected api error: %v", apiErr)
	}
	if meta != nil {
		t.Fatalf("expected nil meta, got %#v", meta)
	}
	if !bytes.Equal(out, in) {
		t.Fatalf("expected passthrough body")
	}
}

func TestPreprocessToolcallCompatChatCompletionsRequest_ToolsInjectedAndRemoved(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4.1-mini",
		"messages": [{"role":"user","content":"Hello"}],
		"tools": [{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],
		"tool_choice": {"type":"function","function":{"name":"get_weather"}}
	}`)

	out, meta, apiErr := preprocessToolcallCompatChatCompletionsRequest(in)
	if apiErr != nil {
		t.Fatalf("unexpected api error: %v", apiErr)
	}
	if meta == nil || strings.TrimSpace(meta.IDSeed) == "" {
		t.Fatalf("unexpected meta: %#v", meta)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if _, ok := m["tools"]; ok {
		t.Fatalf("expected tools removed")
	}
	if _, ok := m["tool_choice"]; ok {
		t.Fatalf("expected tool_choice removed")
	}

	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" {
		t.Fatalf("expected system role, got %#v", sys["role"])
	}
	sysContent, _ := sys["content"].(string)
	if !strings.Contains(sysContent, "<tool_list>") || !strings.Contains(sysContent, "</tool_list>") {
		t.Fatalf("system prompt missing tool_list")
	}
	if !strings.Contains(sysContent, `name="get_weather"`) {
		t.Fatalf("system prompt missing tool name")
	}
	if !strings.Contains(sysContent, "<function_call>") || !strings.Contains(sysContent, `"function_call"`) {
		t.Fatalf("system prompt missing function_call format")
	}
	if !strings.Contains(sysContent, "必须调用工具") {
		t.Fatalf("system prompt missing tool_choice hint")
	}
	user := msgs[1].(map[string]any)
	if user["role"] != "user" || user["content"] != "Hello" {
		t.Fatalf("unexpected user message: %#v", user)
	}
}

func TestPreprocessToolcallCompatChatCompletionsRequest_AssistantToolCallsProtocolized(t *testing.T) {
	in := []byte(`{
		"messages": [{
			"role":"assistant",
			"content":"Calling tool",
			"tool_calls":[{"id":"call1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]
		}]
	}`)

	out, meta, apiErr := preprocessToolcallCompatChatCompletionsRequest(in)
	if apiErr != nil {
		t.Fatalf("unexpected api error: %v", apiErr)
	}
	if meta == nil {
		t.Fatalf("expected meta")
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	msgs := m["messages"].([]any)
	asst := msgs[0].(map[string]any)
	if _, ok := asst["tool_calls"]; ok {
		t.Fatalf("expected tool_calls removed")
	}
	content, _ := asst["content"].(string)
	if !strings.Contains(content, "Calling tool") ||
		!strings.Contains(content, "<function_call>") ||
		!strings.Contains(content, `"name":"get_weather"`) ||
		!strings.Contains(content, `"city":"SF"`) {
		t.Fatalf("unexpected assistant content: %q", content)
	}
}

func TestPreprocessToolcallCompatChatCompletionsRequest_ToolMessageBackrefConverted(t *testing.T) {
	in := []byte(`{
		"messages": [
			{
				"role":"assistant",
				"content":"Calling tool",
				"tool_calls":[{"id":"call1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]
			},
			{
				"role":"tool",
				"tool_call_id":"call1",
				"content":"Sunny"
			}
		]
	}`)

	out, meta, apiErr := preprocessToolcallCompatChatCompletionsRequest(in)
	if apiErr != nil {
		t.Fatalf("unexpected api error: %v", apiErr)
	}
	if meta == nil {
		t.Fatalf("expected meta")
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	toolMsg := msgs[1].(map[string]any)
	if toolMsg["role"] != "user" {
		t.Fatalf("expected converted tool role=user, got %#v", toolMsg["role"])
	}
	if _, ok := toolMsg["tool_call_id"]; ok {
		t.Fatalf("expected tool_call_id removed")
	}
	content, _ := toolMsg["content"].(string)
	if !strings.Contains(content, "<function_call>") ||
		!strings.Contains(content, `"function_call_record"`) ||
		!strings.Contains(content, `"name":"get_weather"`) ||
		!strings.Contains(content, `"city":"SF"`) ||
		!strings.Contains(content, `"response":"Sunny"`) {
		t.Fatalf("unexpected converted tool content: %q", content)
	}
}

func TestPreprocessToolcallCompatChatCompletionsRequest_ErrorMissingToolCallID(t *testing.T) {
	in := []byte(`{"messages":[{"role":"tool","content":"x"}]}`)

	out, meta, apiErr := preprocessToolcallCompatChatCompletionsRequest(in)
	if apiErr == nil {
		t.Fatalf("expected api error")
	}
	if out != nil || meta != nil {
		t.Fatalf("expected nil output/meta on error")
	}
	if apiErr.Code != "BAD_REQUEST" {
		t.Fatalf("unexpected error code: %#v", apiErr)
	}
}

func TestPreprocessToolcallCompatChatCompletionsRequest_ErrorToolCallIDNotFound(t *testing.T) {
	in := []byte(`{"messages":[{"role":"tool","tool_call_id":"missing","content":"x"}]}`)

	out, meta, apiErr := preprocessToolcallCompatChatCompletionsRequest(in)
	if apiErr == nil {
		t.Fatalf("expected api error")
	}
	if out != nil || meta != nil {
		t.Fatalf("expected nil output/meta on error")
	}
	if apiErr.Code != "BAD_REQUEST" || !strings.Contains(apiErr.Message, "tool_call_id not found") {
		t.Fatalf("unexpected api error: %#v", apiErr)
	}
}

func TestPreprocessToolcallCompatChatCompletionsRequest_ErrorInvalidToolCallsFormat(t *testing.T) {
	in := []byte(`{"messages":[{"role":"assistant","content":"","tool_calls":{"id":"call1"}}]}`)

	out, meta, apiErr := preprocessToolcallCompatChatCompletionsRequest(in)
	if apiErr == nil {
		t.Fatalf("expected api error")
	}
	if out != nil || meta != nil {
		t.Fatalf("expected nil output/meta on error")
	}
	if apiErr.Code != "BAD_REQUEST" || !strings.Contains(apiErr.Message, "invalid tool_calls format") {
		t.Fatalf("unexpected api error: %#v", apiErr)
	}
}

func TestPreprocessToolcallCompatChatCompletionsRequest_ErrorInvalidJSON(t *testing.T) {
	out, meta, apiErr := preprocessToolcallCompatChatCompletionsRequest([]byte("{"))
	if apiErr == nil {
		t.Fatalf("expected api error")
	}
	if out != nil || meta != nil {
		t.Fatalf("expected nil output/meta on error")
	}
	if apiErr.Code != "INVALID_JSON" {
		t.Fatalf("unexpected api error: %#v", apiErr)
	}
}
