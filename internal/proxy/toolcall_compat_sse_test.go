package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTransformOpenAIStreamToolcallCompat_EmitsToolCalls(t *testing.T) {
	idSeed := "0123456789abcdef"
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello "},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"world\\n"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"<think>ignored</think>\\n"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"\\n<function_call>{\"function_call\":{\"name\":\"get_weather\",\"arguments\":{\"city\":\"SF\"}}}</function_call>"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	var out bytes.Buffer
	if err := transformOpenAIStreamToolcallCompat(strings.NewReader(upstream), openAISSEEmitter{w: &out}, idSeed); err != nil {
		t.Fatalf("transformOpenAIStreamToolcallCompat error: %v", err)
	}

	s := out.String()
	if strings.Contains(s, "<function_calls>") || strings.Contains(s, "<function_call>") {
		t.Fatalf("expected protocol block not to leak to client: %q", s)
	}
	if !strings.Contains(s, "Hello ") || !strings.Contains(s, "world") {
		t.Fatalf("expected normal content passthrough before tool call: %q", s)
	}
	if !strings.Contains(s, `"tool_calls"`) || !strings.Contains(s, `"name":"get_weather"`) {
		t.Fatalf("expected tool_calls delta in stream: %q", s)
	}
	if !strings.Contains(s, `{\"city\":\"SF\"}`) {
		t.Fatalf("expected serialized tool arguments: %q", s)
	}
	if !strings.Contains(s, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected finish_reason=tool_calls: %q", s)
	}
	if !strings.Contains(s, "data: [DONE]") {
		t.Fatalf("expected [DONE] terminator: %q", s)
	}
}

func TestAnthropicCompatWithToolcallCompatTransformer_Stream_EmitsToolUse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	idSeed := "0123456789abcdef"
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"\\n<function_call>{\"function_call\":{\"name\":\"get_weather\",\"arguments\":{\"city\":\"SF\"}}}</function_call>"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	errCh := make(chan error, 1)
	router := gin.New()
	router.GET("/sse", func(c *gin.Context) {
		base := newAnthropicCompatTransformer("claude-test")
		transformer := newAnthropicCompatWithToolcallCompatTransformer(base, idSeed)
		resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(upstream))}
		errCh <- transformer.HandleSuccess(c, resp, true)
	})

	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sse")
	if err != nil {
		t.Fatalf("http get: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if transformerErr := <-errCh; transformerErr != nil {
		t.Fatalf("transformer error: %v", transformerErr)
	}

	body := string(bodyBytes)
	if !strings.Contains(body, `"type":"tool_use"`) || !strings.Contains(body, `"name":"get_weather"`) {
		t.Fatalf("expected tool_use in anthropic sse: %q", body)
	}
}

func TestAnthropicCompatWithToolcallCompatTransformer_NonStream_EmitsToolUse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	idSeed := "0123456789abcdef"
	upstream := `{
		"id":"chatcmpl_1",
		"model":"m",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hello\n\n<function_call>{\"function_call\":{\"name\":\"get_weather\",\"arguments\":{\"city\":\"SF\"}}}</function_call>"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}
	}`

	errCh := make(chan error, 1)
	router := gin.New()
	router.GET("/json", func(c *gin.Context) {
		base := newAnthropicCompatTransformer("claude-test")
		transformer := newAnthropicCompatWithToolcallCompatTransformer(base, idSeed)
		resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(upstream))}
		errCh <- transformer.HandleSuccess(c, resp, false)
	})

	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/json")
	if err != nil {
		t.Fatalf("http get: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if transformerErr := <-errCh; transformerErr != nil {
		t.Fatalf("transformer error: %v", transformerErr)
	}

	body := string(bodyBytes)
	if strings.Contains(body, "<function_calls>") || strings.Contains(body, "<function_call>") {
		t.Fatalf("expected protocol block not to leak to client: %q", body)
	}

	var payload map[string]any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	content, ok := payload["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("expected non-empty content blocks: %#v", payload["content"])
	}

	foundToolUse := false
	foundHelloText := false
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			if txt, ok := block["text"].(string); ok && strings.Contains(txt, "Hello") {
				foundHelloText = true
			}
		case "tool_use":
			foundToolUse = true
			if block["id"] != "call_0123456789ab_1" || block["name"] != "get_weather" {
				t.Fatalf("unexpected tool_use block: %#v", block)
			}
			input, _ := block["input"].(map[string]any)
			if input["city"] != "SF" {
				t.Fatalf("unexpected tool_use input: %#v", block["input"])
			}
		}
	}
	if !foundToolUse {
		t.Fatalf("expected tool_use block in anthropic json: %q", body)
	}
	if !foundHelloText {
		t.Fatalf("expected text content 'Hello' to be preserved in anthropic json: %q", body)
	}
}

func TestTransformOpenAIStreamToolcallCompat_UTF8SafeHoldback(t *testing.T) {
	idSeed := "compat"
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"中aaaaaaaaaaaaaa"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	var out bytes.Buffer
	if err := transformOpenAIStreamToolcallCompat(strings.NewReader(upstream), openAISSEEmitter{w: &out}, idSeed); err != nil {
		t.Fatalf("transformOpenAIStreamToolcallCompat error: %v", err)
	}

	s := out.String()
	if !strings.Contains(s, "中") || !strings.Contains(s, "aaaaaaaaaaaaaa") {
		t.Fatalf("expected utf-8 content preserved: %q", s)
	}
	if strings.Contains(s, "�") || strings.Contains(strings.ToLower(s), "\\ufffd") {
		t.Fatalf("expected no replacement characters: %q", s)
	}
}
