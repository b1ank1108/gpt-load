package proxy

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

type toolcallCompatTransformer struct {
	triggerSignal string
}

func newToolcallCompatTransformer(triggerSignal string) *toolcallCompatTransformer {
	return &toolcallCompatTransformer{triggerSignal: triggerSignal}
}

func (t *toolcallCompatTransformer) HandleUpstreamError(c *gin.Context, statusCode int, upstreamBody []byte) bool {
	return false
}

func (t *toolcallCompatTransformer) HandleSuccess(c *gin.Context, resp *http.Response, isStream bool) error {
	if isStream {
		return t.streamOpenAIWithToolcallCompat(c, resp)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read upstream body: %w", err)
	}

	decompressed, err := utils.DecompressResponse(resp.Header.Get("Content-Encoding"), bodyBytes)
	if err != nil {
		decompressed = bodyBytes
	}

	out := decompressed
	if !isStream && t.triggerSignal != "" {
		if converted, ok := restoreToolCallsInChatCompletion(decompressed, t.triggerSignal); ok {
			out = converted
		}
	}

	c.Header("Content-Type", "application/json")
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(out)
	return nil
}

type openAIStreamEnvelope struct {
	ID      string
	Object  string
	Created any
	Model   string
}

func (e openAIStreamEnvelope) withChoice(choice map[string]any) map[string]any {
	out := make(map[string]any, 5)
	if strings.TrimSpace(e.ID) != "" {
		out["id"] = e.ID
	}
	if strings.TrimSpace(e.Object) != "" {
		out["object"] = e.Object
	}
	if e.Created != nil {
		out["created"] = e.Created
	}
	if strings.TrimSpace(e.Model) != "" {
		out["model"] = e.Model
	}
	out["choices"] = []any{choice}
	return out
}

type openAISSEEmitter struct {
	w     io.Writer
	flush func()
}

func (e openAISSEEmitter) emit(data any) error {
	if err := writeOpenAISSE(e.w, data); err != nil {
		return err
	}
	if e.flush != nil {
		e.flush()
	}
	return nil
}

func (e openAISSEEmitter) done() error {
	if err := writeOpenAIDone(e.w); err != nil {
		return err
	}
	if e.flush != nil {
		e.flush()
	}
	return nil
}

func (t *toolcallCompatTransformer) streamOpenAIWithToolcallCompat(c *gin.Context, resp *http.Response) error {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported by writer")
	}

	return transformOpenAIStreamToolcallCompat(resp.Body, openAISSEEmitter{w: c.Writer, flush: flusher.Flush}, t.triggerSignal)
}

func transformOpenAIStreamToolcallCompat(r io.Reader, emitter openAISSEEmitter, triggerSignal string) error {
	reader := bufio.NewReader(r)
	var dataLines []string

	envelope := openAIStreamEnvelope{}

	pendingText := ""
	protocolBuffer := ""
	triggered := false
	roleEmitted := false
	pendingRole := ""

	flushContent := func(text string) error {
		if text == "" {
			return nil
		}
		delta := map[string]any{"content": text}
		if !roleEmitted && strings.TrimSpace(pendingRole) != "" {
			delta["role"] = pendingRole
			roleEmitted = true
			pendingRole = ""
		}
		return emitter.emit(envelope.withChoice(map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": nil,
		}))
	}

	emitRoleOnly := func(role string) error {
		if roleEmitted || strings.TrimSpace(role) == "" {
			return nil
		}
		roleEmitted = true
		pendingRole = ""
		return emitter.emit(envelope.withChoice(map[string]any{
			"index":         0,
			"delta":         map[string]any{"role": role},
			"finish_reason": nil,
		}))
	}

	emitFinish := func(reason string) error {
		if err := flushContent(pendingText); err != nil {
			return err
		}
		pendingText = ""
		return emitter.emit(envelope.withChoice(map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": reason,
		}))
	}

	emitToolCallsAndFinish := func(calls []toolcallCompatFunctionCall) error {
		if err := flushContent(pendingText); err != nil {
			return err
		}
		pendingText = ""

		seed := toolcallCompatIDSeed(triggerSignal)
		deltaCalls := make([]any, 0, len(calls))
		for i, call := range calls {
			deltaCalls = append(deltaCalls, map[string]any{
				"index": i,
				"id":    fmt.Sprintf("call_%s_%d", seed, i+1),
				"type":  "function",
				"function": map[string]any{
					"name":      call.ToolName,
					"arguments": strings.TrimSpace(call.ArgsJSON),
				},
			})
		}

		if err := emitter.emit(envelope.withChoice(map[string]any{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": deltaCalls,
			},
			"finish_reason": nil,
		})); err != nil {
			return err
		}

		if err := emitter.emit(envelope.withChoice(map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "tool_calls",
		})); err != nil {
			return err
		}

		return emitter.done()
	}

	emitStopAndDone := func() error {
		if err := emitFinish("stop"); err != nil {
			return err
		}
		return emitter.done()
	}

	maybeFinalizeFromProtocol := func() (bool, error) {
		if !strings.Contains(protocolBuffer, "</function_calls>") {
			return false, nil
		}
		_, calls, ok := extractToolcallCompatFunctionCalls(protocolBuffer, triggerSignal)
		if !ok {
			logrus.WithField("trigger", triggerSignal).Warn("toolcall compat: failed to parse tool calls from stream")
			return true, emitStopAndDone()
		}
		return true, emitToolCallsAndFinish(calls)
	}

	const maxProtocolBufferBytes = 512 * 1024

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("read upstream stream: %w", err)
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 {
				continue
			}
			payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
			dataLines = dataLines[:0]
			if payload == "" {
				continue
			}
			if payload == "[DONE]" {
				if triggered {
					logrus.WithField("trigger", triggerSignal).Warn("toolcall compat: stream ended before parsing tool calls")
					return emitStopAndDone()
				}
				if err := flushContent(pendingText); err != nil {
					return err
				}
				pendingText = ""
				return emitter.done()
			}

			var chunk map[string]any
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}

			if envelope.ID == "" {
				envelope.ID, _ = chunk["id"].(string)
			}
			if envelope.Object == "" {
				envelope.Object, _ = chunk["object"].(string)
			}
			if envelope.Created == nil {
				envelope.Created = chunk["created"]
			}
			if envelope.Model == "" {
				envelope.Model, _ = chunk["model"].(string)
			}

			choices, _ := chunk["choices"].([]any)
			if len(choices) == 0 {
				continue
			}
			choice, _ := choices[0].(map[string]any)
			if choice == nil {
				continue
			}

			if fr, ok := choice["finish_reason"].(string); ok && strings.TrimSpace(fr) != "" && !triggered {
				if err := emitFinish(fr); err != nil {
					return err
				}
				continue
			}

			delta, _ := choice["delta"].(map[string]any)
			if delta == nil {
				continue
			}

			if role, ok := delta["role"].(string); ok && strings.TrimSpace(role) != "" {
				if !roleEmitted && pendingRole == "" {
					pendingRole = role
				}
				if err := emitRoleOnly(role); err != nil {
					return err
				}
			}

			content, _ := delta["content"].(string)
			if content == "" {
				continue
			}

			if !triggered {
				pendingText += content
				if idx := strings.Index(pendingText, triggerSignal); idx != -1 {
					plain := pendingText[:idx]
					protocolBuffer = pendingText[idx:]
					pendingText = ""
					triggered = true
					if err := flushContent(plain); err != nil {
						return err
					}
					if done, err := maybeFinalizeFromProtocol(); done {
						return err
					}
					continue
				}

				if len(pendingText) > len(triggerSignal) {
					flushLen := len(pendingText) - len(triggerSignal)
					if flushLen > 0 {
						toFlush := pendingText[:flushLen]
						pendingText = pendingText[flushLen:]
						if err := flushContent(toFlush); err != nil {
							return err
						}
					}
				}
				continue
			}

			protocolBuffer += content
			if len(protocolBuffer) > maxProtocolBufferBytes {
				logrus.WithField("trigger", triggerSignal).Warn("toolcall compat: stream protocol buffer exceeded limit")
				return emitStopAndDone()
			}
			if done, err := maybeFinalizeFromProtocol(); done {
				return err
			}

			continue
		}

		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if triggered {
		logrus.WithField("trigger", triggerSignal).Warn("toolcall compat: stream ended unexpectedly")
		return emitStopAndDone()
	}
	if err := flushContent(pendingText); err != nil {
		return err
	}
	return emitter.done()
}

func writeOpenAISSE(w io.Writer, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, "data: "+string(payload)+"\n\n")
	return err
}

func writeOpenAIDone(w io.Writer) error {
	_, err := io.WriteString(w, "data: [DONE]\n\n")
	return err
}

type toolcallCompatFunctionCalls struct {
	Calls []toolcallCompatFunctionCall `xml:"function_call"`
}

type toolcallCompatFunctionCall struct {
	ToolName string `xml:"tool"`
	ArgsJSON string `xml:"args_json"`
}

func restoreToolCallsInChatCompletion(body []byte, triggerSignal string) ([]byte, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false
	}

	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		return body, false
	}
	choice0, ok := choices[0].(map[string]any)
	if !ok {
		return body, false
	}

	msg, ok := choice0["message"].(map[string]any)
	if !ok {
		return body, false
	}
	content, _ := msg["content"].(string)

	plain, calls, ok := extractToolcallCompatFunctionCalls(content, triggerSignal)
	if !ok || len(calls) == 0 {
		return body, false
	}

	seed := toolcallCompatIDSeed(triggerSignal)
	toolCalls := make([]any, 0, len(calls))
	for i, call := range calls {
		toolCalls = append(toolCalls, map[string]any{
			"id":   fmt.Sprintf("call_%s_%d", seed, i+1),
			"type": "function",
			"function": map[string]any{
				"name":      call.ToolName,
				"arguments": strings.TrimSpace(call.ArgsJSON),
			},
		})
	}

	msg["content"] = plain
	msg["tool_calls"] = toolCalls
	choice0["finish_reason"] = "tool_calls"
	choices[0] = choice0
	payload["choices"] = choices

	converted, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return converted, true
}

func extractToolcallCompatFunctionCalls(content string, triggerSignal string) (string, []toolcallCompatFunctionCall, bool) {
	idx := strings.Index(content, triggerSignal)
	if idx == -1 {
		return content, nil, false
	}

	plain := strings.TrimRight(content[:idx], "\n")
	rest := content[idx+len(triggerSignal):]

	start := strings.Index(rest, "<function_calls>")
	end := strings.Index(rest, "</function_calls>")
	if start == -1 || end == -1 || end < start {
		return content, nil, false
	}

	xmlBlob := rest[start : end+len("</function_calls>")]
	var parsed toolcallCompatFunctionCalls
	if err := xml.Unmarshal([]byte(xmlBlob), &parsed); err != nil {
		return content, nil, false
	}

	out := make([]toolcallCompatFunctionCall, 0, len(parsed.Calls))
	for _, call := range parsed.Calls {
		name := strings.TrimSpace(call.ToolName)
		if name == "" {
			continue
		}
		out = append(out, toolcallCompatFunctionCall{
			ToolName: name,
			ArgsJSON: call.ArgsJSON,
		})
	}

	if len(out) == 0 {
		return content, nil, false
	}
	return plain, out, true
}

func toolcallCompatIDSeed(triggerSignal string) string {
	signal := strings.TrimSpace(triggerSignal)
	if signal == "" {
		return "compat"
	}
	lastUnderscore := strings.LastIndex(signal, "_")
	if lastUnderscore == -1 || lastUnderscore == len(signal)-1 {
		return "compat"
	}
	seed := signal[lastUnderscore+1:]
	if len(seed) > 12 {
		return seed[:12]
	}
	return seed
}
