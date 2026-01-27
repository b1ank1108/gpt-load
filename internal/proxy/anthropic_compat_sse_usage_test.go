package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type parsedSSEEvent struct {
	Event string
	Data  string
}

func parseSSEEvents(body string) []parsedSSEEvent {
	scanner := bufio.NewScanner(strings.NewReader(body))

	var (
		out       []parsedSSEEvent
		eventName string
		dataLines []string
	)

	flush := func() {
		if eventName == "" && len(dataLines) == 0 {
			return
		}
		out = append(out, parsedSSEEvent{
			Event: eventName,
			Data:  strings.Join(dataLines, "\n"),
		})
		eventName = ""
		dataLines = dataLines[:0]
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
	}
	flush()
	return out
}

func TestAnthropicCompatTransformer_Stream_UsageTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	openAIModel := "gpt-3.5-turbo"
	openAIReq := []byte(`{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"Hello"}]}`)
	inputTokens := estimateOpenAIRequestInputTokens(openAIReq, "")
	if inputTokens <= 0 {
		t.Fatalf("expected inputTokens > 0, got %d", inputTokens)
	}

	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello "},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	expectedOutputTokens := estimateTokens("Hello ", openAIModel) + estimateTokens(`{"city":"SF"}`, openAIModel)
	if expectedOutputTokens <= 0 {
		t.Fatalf("expected expectedOutputTokens > 0, got %d", expectedOutputTokens)
	}

	errCh := make(chan error, 1)
	router := gin.New()
	router.GET("/sse", func(c *gin.Context) {
		transformer := newAnthropicCompatTransformer("claude-test").WithStreamingUsage(openAIModel, inputTokens)
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

	events := parseSSEEvents(string(bodyBytes))

	var (
		gotInputTokens  = -1
		gotOutputTokens = -1
	)
	for _, e := range events {
		switch e.Event {
		case "message_start":
			var payload map[string]any
			if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
				t.Fatalf("unmarshal message_start: %v", err)
			}
			msg, _ := payload["message"].(map[string]any)
			usage, _ := msg["usage"].(map[string]any)
			gotInputTokens = asInt(usage["input_tokens"])
		case "message_delta":
			var payload map[string]any
			if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
				t.Fatalf("unmarshal message_delta: %v", err)
			}
			usage, _ := payload["usage"].(map[string]any)
			gotOutputTokens = asInt(usage["output_tokens"])
		}
	}

	if gotInputTokens != inputTokens {
		t.Fatalf("input_tokens mismatch: got %d want %d", gotInputTokens, inputTokens)
	}
	if gotOutputTokens != expectedOutputTokens {
		t.Fatalf("output_tokens mismatch: got %d want %d", gotOutputTokens, expectedOutputTokens)
	}
}
