package proxy

import (
	"encoding/json"
	"testing"
)

func TestConvertAnthropicMessagesToOpenAI_BasicText(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-haiku-20240307",
		"max_tokens": 123,
		"messages": [{"role":"user","content":"Hello"}]
	}`)

	out, model, err := convertAnthropicMessagesToOpenAI(in)
	if err != nil {
		t.Fatalf("convertAnthropicMessagesToOpenAI error: %v", err)
	}
	if model != "claude-3-haiku-20240307" {
		t.Fatalf("model mismatch: %q", model)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal openai output: %v", err)
	}
	if m["model"] != "claude-3-haiku-20240307" {
		t.Fatalf("openai model mismatch: %#v", m["model"])
	}
	if int(m["max_tokens"].(float64)) != 123 {
		t.Fatalf("openai max_tokens mismatch: %#v", m["max_tokens"])
	}
	msgs := m["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg0 := msgs[0].(map[string]any)
	if msg0["role"] != "user" || msg0["content"] != "Hello" {
		t.Fatalf("unexpected message: %#v", msg0)
	}
}

func TestConvertAnthropicMessagesToOpenAI_SystemAndToolUse(t *testing.T) {
	in := []byte(`{
		"model": "m1",
		"max_tokens": 100,
		"system": "You are helpful",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type":"text","text":"Calling tool"},
					{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type":"tool_result","tool_use_id":"toolu_1","content":"Sunny"}
				]
			}
		]
	}`)

	out, _, err := convertAnthropicMessagesToOpenAI(in)
	if err != nil {
		t.Fatalf("convertAnthropicMessagesToOpenAI error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal openai output: %v", err)
	}

	msgs := m["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (system, assistant, tool), got %d", len(msgs))
	}

	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "You are helpful" {
		t.Fatalf("unexpected system message: %#v", sys)
	}

	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Fatalf("unexpected assistant role: %#v", asst["role"])
	}
	if asst["content"] != "Calling tool" {
		t.Fatalf("unexpected assistant content: %#v", asst["content"])
	}
	toolCalls := asst["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(toolCalls))
	}
	call := toolCalls[0].(map[string]any)
	if call["id"] != "toolu_1" {
		t.Fatalf("unexpected tool_call id: %#v", call["id"])
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("unexpected tool_call name: %#v", fn["name"])
	}
	if fn["arguments"] != `{"city":"SF"}` {
		t.Fatalf("unexpected tool_call arguments: %#v", fn["arguments"])
	}

	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "toolu_1" || tool["content"] != "Sunny" {
		t.Fatalf("unexpected tool message: %#v", tool)
	}
}

func TestConvertOpenAIChatCompletionToAnthropic_Text(t *testing.T) {
	in := []byte(`{
		"id":"chatcmpl_123",
		"model":"gpt-4.1-mini",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}
	}`)

	out, err := convertOpenAIChatCompletionToAnthropic(in, "")
	if err != nil {
		t.Fatalf("convertOpenAIChatCompletionToAnthropic error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal anthropic output: %v", err)
	}
	if m["type"] != "message" || m["role"] != "assistant" {
		t.Fatalf("unexpected anthropic envelope: %#v", m)
	}
	if m["model"] != "gpt-4.1-mini" {
		t.Fatalf("unexpected model: %#v", m["model"])
	}
	if m["stop_reason"] != "end_turn" {
		t.Fatalf("unexpected stop_reason: %#v", m["stop_reason"])
	}
	content := m["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "Hello" {
		t.Fatalf("unexpected content block: %#v", block)
	}
	usage := m["usage"].(map[string]any)
	if int(usage["input_tokens"].(float64)) != 5 || int(usage["output_tokens"].(float64)) != 7 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
}

func TestConvertOpenAIChatCompletionToAnthropic_ToolCalls(t *testing.T) {
	in := []byte(`{
		"id":"chatcmpl_1",
		"model":"gpt-test",
		"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}
	}`)

	out, err := convertOpenAIChatCompletionToAnthropic(in, "")
	if err != nil {
		t.Fatalf("convertOpenAIChatCompletionToAnthropic error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal anthropic output: %v", err)
	}
	if m["stop_reason"] != "tool_use" {
		t.Fatalf("unexpected stop_reason: %#v", m["stop_reason"])
	}
	content := m["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block (tool_use), got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "tool_use" || block["id"] != "call1" || block["name"] != "get_weather" {
		t.Fatalf("unexpected tool_use block: %#v", block)
	}
	input := block["input"].(map[string]any)
	if input["city"] != "SF" {
		t.Fatalf("unexpected tool_use input: %#v", input)
	}
}

func TestConvertOpenAIErrorToAnthropic(t *testing.T) {
	out := convertOpenAIErrorToAnthropic(401, []byte(`{"error":{"message":"Invalid key"}}`))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal error output: %v", err)
	}
	if m["type"] != "error" {
		t.Fatalf("unexpected type: %#v", m["type"])
	}
	errObj := m["error"].(map[string]any)
	if errObj["type"] != "authentication_error" || errObj["message"] != "Invalid key" {
		t.Fatalf("unexpected error object: %#v", errObj)
	}
}
