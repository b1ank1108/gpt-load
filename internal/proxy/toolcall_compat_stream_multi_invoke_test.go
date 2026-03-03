package proxy

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTransformOpenAIStreamToolcallCompat_MultiInvoke(t *testing.T) {
	idSeed := "compat"
	trigger := "<<CALL_multi>>"

	invoke1 := strings.Join([]string{
		`<invoke name="get_weather">`,
		`<parameter name="city">SF</parameter>`,
		`</invoke>`,
	}, "\n")
	invoke2 := strings.Join([]string{
		`<invoke name="get_time">`,
		`<parameter name="tz">UTC</parameter>`,
		`</invoke>`,
	}, "\n")

	payload := strings.Join([]string{
		"Hello",
		"",
		trigger,
		invoke1,
		invoke2,
	}, "\n")

	upstream := openAIUpstreamSSE([]string{payload})
	var out bytes.Buffer
	if err := transformOpenAIStreamToolcallCompat(strings.NewReader(upstream), openAISSEEmitter{w: &out}, idSeed, trigger); err != nil {
		t.Fatalf("transformOpenAIStreamToolcallCompat error: %v", err)
	}

	raw := out.String()
	if strings.Contains(raw, "CALL_multi") {
		t.Fatalf("expected trigger not to leak: %q", raw)
	}
	if strings.Contains(strings.ToLower(raw), "<invoke") || strings.Contains(strings.ToLower(raw), "\\u003cinvoke") {
		t.Fatalf("expected invoke tags not to leak: %q", raw)
	}

	content, toolCalls, finishReason := parseOpenAIStreamToolcallCompatToolCalls(t, raw)
	if !strings.Contains(content, "Hello") {
		t.Fatalf("expected prefix content preserved, got: %q", content)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("unexpected finish_reason: %q", finishReason)
	}

	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d: %#v", len(toolCalls), toolCalls)
	}

	if toolCalls[0].name != "get_weather" || toolCalls[1].name != "get_time" {
		t.Fatalf("unexpected tool call names: %#v", toolCalls)
	}

	if !reflect.DeepEqual(toolCalls[0].args, map[string]any{"city": "SF"}) {
		t.Fatalf("unexpected args[0]: %#v", toolCalls[0].args)
	}
	if !reflect.DeepEqual(toolCalls[1].args, map[string]any{"tz": "UTC"}) {
		t.Fatalf("unexpected args[1]: %#v", toolCalls[1].args)
	}
}

type parsedToolCall struct {
	name string
	args map[string]any
}

func parseOpenAIStreamToolcallCompatToolCalls(t *testing.T, s string) (string, []parsedToolCall, string) {
	t.Helper()

	var content strings.Builder
	var calls []parsedToolCall
	finishReason := ""

	for _, block := range strings.Split(s, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if strings.HasPrefix(block, "data: [DONE]") {
			continue
		}

		var payloadLine string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data: ") {
				payloadLine = strings.TrimPrefix(line, "data: ")
				break
			}
		}
		if strings.TrimSpace(payloadLine) == "" {
			continue
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(payloadLine), &chunk); err != nil {
			continue
		}

		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice0, _ := choices[0].(map[string]any)
		if fr, ok := choice0["finish_reason"].(string); ok && strings.TrimSpace(fr) != "" {
			finishReason = strings.TrimSpace(fr)
		}

		delta, _ := choice0["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		if c, ok := delta["content"].(string); ok && c != "" {
			content.WriteString(c)
		}

		toolCallsAny, _ := delta["tool_calls"].([]any)
		if len(toolCallsAny) == 0 {
			continue
		}

		for _, item := range toolCallsAny {
			obj, _ := item.(map[string]any)
			fnObj, _ := obj["function"].(map[string]any)
			name, _ := fnObj["name"].(string)
			argStr, _ := fnObj["arguments"].(string)
			if strings.TrimSpace(argStr) == "" {
				t.Fatalf("missing tool call arguments in: %q", s)
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(argStr), &args); err != nil {
				t.Fatalf("unmarshal tool call arguments: %v", err)
			}
			calls = append(calls, parsedToolCall{name: name, args: args})
		}
	}

	if len(calls) == 0 {
		t.Fatalf("expected tool_calls delta in output: %q", s)
	}
	if finishReason == "" {
		t.Fatalf("expected finish_reason in output: %q", s)
	}
	return content.String(), calls, finishReason
}

