package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func TestToolcallCompatInvokeXML_RoundTrip_PBT(t *testing.T) {
	rng := rand.New(rand.NewSource(1337))

	for i := 0; i < 100; i++ {
		args := randomJSONArgs(rng, 2)
		argsJSON, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}

		toolName := fmt.Sprintf("tool_%d", i)
		invoke := buildToolcallCompatInvokeXML(toolName, string(argsJSON))

		call, ok := parseToolcallCompatInvokeXML(invoke)
		if !ok {
			t.Fatalf("expected parse ok for invoke: %q", invoke)
		}
		if call.ToolName != toolName {
			t.Fatalf("unexpected tool name: got %q want %q", call.ToolName, toolName)
		}

		var got map[string]any
		if err := json.Unmarshal([]byte(call.ArgsJSON), &got); err != nil {
			t.Fatalf("unmarshal parsed args: %v", err)
		}
		if !reflect.DeepEqual(got, args) {
			t.Fatalf("args mismatch: got %#v want %#v", got, args)
		}
	}
}

func TestTransformOpenAIStreamToolcallCompat_TriggerSplitPositions(t *testing.T) {
	idSeed := "compat"
	trigger := "<<CALL_aa11bb>>"

	invoke := strings.Join([]string{
		`<invoke name="get_weather">`,
		`<parameter name="city">SF</parameter>`,
		`<parameter name="days">3</parameter>`,
		`</invoke>`,
	}, "\n")

	prefix := "Hello\n\n"
	suffix := "\n" + invoke

	for split := 0; split <= len(trigger); split++ {
		t.Run(fmt.Sprintf("split_%d", split), func(t *testing.T) {
			seg1 := prefix + trigger[:split]
			seg2 := trigger[split:] + suffix

			upstream := openAIUpstreamSSE([]string{seg1, seg2})
			var out bytes.Buffer
			if err := transformOpenAIStreamToolcallCompat(strings.NewReader(upstream), openAISSEEmitter{w: &out}, idSeed, trigger); err != nil {
				t.Fatalf("transformOpenAIStreamToolcallCompat error: %v", err)
			}

			raw := out.String()
			if strings.Contains(raw, "CALL_aa11bb") {
				t.Fatalf("expected trigger not to leak: %q", raw)
			}
			if strings.Contains(strings.ToLower(raw), "<invoke") || strings.Contains(strings.ToLower(raw), "\\u003cinvoke") {
				t.Fatalf("expected invoke tags not to leak: %q", raw)
			}

			content, args, finishReason := parseOpenAIStreamToolcallCompatOutput(t, raw)
			if !strings.Contains(content, "Hello") {
				t.Fatalf("expected prefix content preserved, got: %q", content)
			}
			if finishReason != "tool_calls" {
				t.Fatalf("unexpected finish_reason: %q", finishReason)
			}

			wantArgs := map[string]any{
				"city": "SF",
				"days": float64(3),
			}
			if !reflect.DeepEqual(args, wantArgs) {
				t.Fatalf("args mismatch: got %#v want %#v", args, wantArgs)
			}
		})
	}
}

func openAIUpstreamSSE(contentSegments []string) string {
	var b strings.Builder
	for i, seg := range contentSegments {
		delta := map[string]any{"content": seg}
		if i == 0 {
			delta["role"] = "assistant"
		}
		chunk := map[string]any{
			"id":      "chatcmpl_1",
			"object":  "chat.completion.chunk",
			"created": 1,
			"model":   "m",
			"choices": []any{
				map[string]any{
					"index":         0,
					"delta":         delta,
					"finish_reason": nil,
				},
			},
		}
		payload, _ := json.Marshal(chunk)
		b.WriteString("data: ")
		b.WriteString(string(payload))
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func parseOpenAIStreamToolcallCompatOutput(t *testing.T, s string) (string, map[string]any, string) {
	t.Helper()

	var content strings.Builder
	var args map[string]any
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
		first, _ := toolCallsAny[0].(map[string]any)
		fnObj, _ := first["function"].(map[string]any)
		argStr, _ := fnObj["arguments"].(string)
		if strings.TrimSpace(argStr) == "" {
			t.Fatalf("missing tool call arguments in: %q", s)
		}
		if err := json.Unmarshal([]byte(argStr), &args); err != nil {
			t.Fatalf("unmarshal tool call arguments: %v", err)
		}
	}

	if args == nil {
		t.Fatalf("expected tool_calls delta in output: %q", s)
	}
	if finishReason == "" {
		t.Fatalf("expected finish_reason in output: %q", s)
	}
	return content.String(), args, finishReason
}

func randomJSONArgs(rng *rand.Rand, depth int) map[string]any {
	if depth <= 0 {
		return map[string]any{}
	}
	n := rng.Intn(5)
	out := make(map[string]any, n)
	for len(out) < n {
		key := fmt.Sprintf("k%d", rng.Intn(50))
		out[key] = randomJSONValue(rng, depth-1)
	}
	return out
}

func randomJSONValue(rng *rand.Rand, depth int) any {
	switch rng.Intn(5) {
	case 0:
		return fmt.Sprintf("v%d", rng.Intn(100))
	case 1:
		return float64(rng.Intn(100))
	case 2:
		return rng.Intn(2) == 0
	case 3:
		n := rng.Intn(4)
		arr := make([]any, 0, n)
		for i := 0; i < n; i++ {
			arr = append(arr, fmt.Sprintf("s%d", rng.Intn(50)))
		}
		return arr
	default:
		if depth <= 0 {
			return fmt.Sprintf("v%d", rng.Intn(100))
		}
		return randomJSONArgs(rng, depth)
	}
}

