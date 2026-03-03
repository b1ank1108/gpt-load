package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

type toolcallCompatTransformer struct {
	idSeed   string
	trigger string
}

func newToolcallCompatTransformer(idSeed string, trigger string) *toolcallCompatTransformer {
	return &toolcallCompatTransformer{idSeed: idSeed, trigger: trigger}
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
	if !isStream {
		if converted, ok := restoreToolCallsInChatCompletion(decompressed, t.idSeed, t.trigger); ok {
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

	return transformOpenAIStreamToolcallCompat(resp.Body, openAISSEEmitter{w: c.Writer, flush: flusher.Flush}, t.idSeed, t.trigger)
}

func transformOpenAIStreamToolcallCompat(r io.Reader, emitter openAISSEEmitter, idSeed string, triggerSignal string) error {
	triggerSignal = strings.TrimSpace(triggerSignal)
	if triggerSignal == "" {
		return fmt.Errorf("toolcall compat: missing trigger signal")
	}

	reader := bufio.NewReader(r)
	var dataLines []string

	envelope := openAIStreamEnvelope{}

	protocolBuffer := ""
	triggered := false
	sawRole := false
	detector := newToolcallCompatTriggerDetector(triggerSignal)

	writeLine := func(line string) error {
		if _, err := io.WriteString(emitter.w, line); err != nil {
			return err
		}
		if emitter.flush != nil {
			emitter.flush()
		}
		return nil
	}

	emitRawPayload := func(payload string) error {
		return writeLine("data: " + payload + "\n\n")
	}

	flushDetectorRemainder := func() error {
		text := detector.FlushRemainder()
		if text == "" {
			return nil
		}
		delta := map[string]any{"content": text}
		if !sawRole {
			delta["role"] = "assistant"
			sawRole = true
		}
		return emitter.emit(envelope.withChoice(map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": nil,
		}))
	}

	emitToolCallsAndFinish := func(calls []toolcallCompatInvokeCall) error {
		if err := flushDetectorRemainder(); err != nil {
			return err
		}

		seed := toolcallCompatIDSeed(idSeed)
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

		delta := map[string]any{
			"tool_calls": deltaCalls,
		}
		if !sawRole {
			delta["role"] = "assistant"
			sawRole = true
		}

		if err := emitter.emit(envelope.withChoice(map[string]any{
			"index":         0,
			"delta":         delta,
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
		if err := flushDetectorRemainder(); err != nil {
			return err
		}
		if err := emitter.emit(envelope.withChoice(map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		})); err != nil {
			return err
		}
		return emitter.done()
	}

	emitProtocolTextAndStop := func(text string) error {
		if err := flushDetectorRemainder(); err != nil {
			return err
		}
		if text != "" {
			delta := map[string]any{"content": text}
			if !sawRole {
				delta["role"] = "assistant"
				sawRole = true
			}
			if err := emitter.emit(envelope.withChoice(map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			})); err != nil {
				return err
			}
		}
		if err := emitter.emit(envelope.withChoice(map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		})); err != nil {
			return err
		}
		return emitter.done()
	}

	finalizeFromProtocol := func() error {
		invokes, ok := extractAllInvokeXML(protocolBuffer)
		if !ok {
			logrus.WithField("seed", seedForLog(idSeed)).Debug("toolcall compat: stream invoke missing or incomplete")
			logrus.WithField("seed", seedForLog(idSeed)).Warn("toolcall compat: failed to parse invoke from stream")
			return emitProtocolTextAndStop(strings.TrimSpace(protocolBuffer))
		}

		calls := make([]toolcallCompatInvokeCall, 0, len(invokes))
		for _, invokeXML := range invokes {
			call, parsed := parseToolcallCompatInvokeXML(invokeXML)
			if parsed {
				calls = append(calls, call)
			}
		}
		if len(calls) == 0 {
			logrus.WithField("seed", seedForLog(idSeed)).Debug("toolcall compat: stream invoke parse failed")
			logrus.WithField("seed", seedForLog(idSeed)).Warn("toolcall compat: failed to parse tool call from stream")
			return emitProtocolTextAndStop(strings.TrimSpace(protocolBuffer))
		}
		logrus.WithFields(logrus.Fields{
			"seed":      seedForLog(idSeed),
			"tool_name": strings.TrimSpace(calls[0].ToolName),
			"tool_calls": len(calls),
		}).Debug("toolcall compat: stream invoke parsed")
		return emitToolCallsAndFinish(calls)
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
					return finalizeFromProtocol()
				}
				if err := flushDetectorRemainder(); err != nil {
					return err
				}
				return emitter.done()
			}

			var chunk map[string]any
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				if !triggered {
					if err := emitRawPayload(payload); err != nil {
						return err
					}
				}
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
				if !triggered {
					if err := emitRawPayload(payload); err != nil {
						return err
					}
				}
				continue
			}
			choice, _ := choices[0].(map[string]any)
			if choice == nil {
				if !triggered {
					if err := emitRawPayload(payload); err != nil {
						return err
					}
				}
				continue
			}

			delta, _ := choice["delta"].(map[string]any)
			if delta == nil {
				if !triggered {
					if err := emitRawPayload(payload); err != nil {
						return err
					}
				}
				continue
			}

			if role, ok := delta["role"].(string); ok && strings.TrimSpace(role) != "" {
				sawRole = true
			}

			finishReason, _ := choice["finish_reason"].(string)
			finishReason = strings.TrimSpace(finishReason)

			content, _ := delta["content"].(string)
			if !triggered {
				if content != "" {
					detected, emitText := detector.Process(content)
					needsRewrite := emitText != content

					if detected {
						triggered = true
						protocolBuffer = detector.TakeRemainder()
						choice["finish_reason"] = nil
						needsRewrite = true
						logrus.WithField("seed", seedForLog(idSeed)).Debug("toolcall compat: stream protocol triggered")
					}

					if emitText == "" {
						delete(delta, "content")
					} else {
						delta["content"] = emitText
					}

					if !triggered && finishReason != "" {
						choice["finish_reason"] = nil
						out, err := json.Marshal(chunk)
						if err != nil {
							return err
						}
						if err := emitRawPayload(string(out)); err != nil {
							return err
						}
						if err := flushDetectorRemainder(); err != nil {
							return err
						}
						if err := emitter.emit(envelope.withChoice(map[string]any{
							"index":         0,
							"delta":         map[string]any{},
							"finish_reason": finishReason,
						})); err != nil {
							return err
						}
						continue
					}

					if needsRewrite {
						out, err := json.Marshal(chunk)
						if err != nil {
							return err
						}
						if err := emitRawPayload(string(out)); err != nil {
							return err
						}
					} else {
						if err := emitRawPayload(payload); err != nil {
							return err
						}
					}

						if triggered {
							if len(protocolBuffer) > maxProtocolBufferBytes {
								logrus.WithField("seed", seedForLog(idSeed)).Warn("toolcall compat: stream protocol buffer exceeded limit")
								return emitStopAndDone()
							}
						}
						continue
					}

				if finishReason != "" {
					if err := flushDetectorRemainder(); err != nil {
						return err
					}
				}
				if err := emitRawPayload(payload); err != nil {
					return err
				}
				continue
			}

			if content != "" {
				protocolBuffer += content
			}
				if len(protocolBuffer) > maxProtocolBufferBytes {
					logrus.WithField("seed", seedForLog(idSeed)).Warn("toolcall compat: stream protocol buffer exceeded limit")
					return emitStopAndDone()
				}
				if finishReason != "" {
					return finalizeFromProtocol()
				}
				continue
			}

		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}

		if !triggered {
			if err := writeLine(line + "\n"); err != nil {
				return err
			}
		}
	}

	if triggered {
		return finalizeFromProtocol()
	}
	if err := flushDetectorRemainder(); err != nil {
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
	Calls []toolcallCompatFunctionCallXML `xml:"function_call"`
}

type toolcallCompatFunctionCallXML struct {
	ToolName string `xml:"tool"`
	ArgsJSON string `xml:"args_json"`
	Raw      string `xml:",chardata"`
}

var (
	toolcallCompatInvokeNameRE  = regexp.MustCompile(`(?is)<invoke[^>]*\bname\s*=\s*(?:"([^"]+)"|'([^']+)'|([^\s>]+))[^>]*>`)
	toolcallCompatParameterName = regexp.MustCompile(`(?is)<parameter[^>]*\bname\s*=\s*(?:"([^"]+)"|'([^']+)'|([^\s>]+))[^>]*>(.*?)</parameter>`)
	toolcallCompatInvokeStartRE = regexp.MustCompile(`(?i)<invoke`)
	toolcallCompatInvokeEndRE   = regexp.MustCompile(`(?i)</invoke>`)
	toolcallCompatTrailingComma = regexp.MustCompile(`,\s*([}\]])`)
	toolcallCompatScalarQuoted  = regexp.MustCompile(`(?i):[ \t]*"(true|false|null)"`)
)

type toolcallCompatInvokeCall struct {
	ToolName string
	ArgsJSON string
}

func restoreToolCallsInChatCompletion(body []byte, idSeed string, triggerSignal string) ([]byte, bool) {
	triggerSignal = strings.TrimSpace(triggerSignal)
	if triggerSignal == "" {
		return body, false
	}

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

	plain, calls, triggered, parsed := extractToolcallCompatInvokeCalls(content, triggerSignal)
	if !triggered {
		return body, false
	}

	if strings.TrimSpace(plain) == "" {
		msg["content"] = nil
	} else {
		msg["content"] = plain
	}

	if parsed {
		seed := toolcallCompatIDSeed(idSeed)
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
		msg["tool_calls"] = toolCalls
		choice0["finish_reason"] = "tool_calls"
	}

	choices[0] = choice0
	payload["choices"] = choices

	converted, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return converted, true
}

func extractToolcallCompatInvokeCalls(content string, triggerSignal string) (string, []toolcallCompatInvokeCall, bool, bool) {
	if strings.TrimSpace(content) == "" {
		return content, nil, false, false
	}
	triggerSignal = strings.TrimSpace(triggerSignal)
	if triggerSignal == "" {
		return content, nil, false, false
	}

	idx := strings.Index(content, triggerSignal)
	if idx == -1 {
		return content, nil, false, false
	}

	prefix := content[:idx]
	rest := content[idx+len(triggerSignal):]
	plainFallback := strings.TrimSpace(prefix + rest)

	invokes, nonInvokeText, ok := extractInvokeXMLAndPlain(rest)
	if !ok {
		return plainFallback, nil, true, false
	}

	calls := make([]toolcallCompatInvokeCall, 0, len(invokes))
	for _, invokeXML := range invokes {
		call, parsed := parseToolcallCompatInvokeXML(invokeXML)
		if parsed {
			calls = append(calls, call)
		}
	}
	if len(calls) == 0 {
		return plainFallback, nil, true, false
	}

	plain := strings.TrimSpace(prefix + nonInvokeText)
	return plain, calls, true, true
}

func parseToolcallCompatInvokeXML(xml string) (toolcallCompatInvokeCall, bool) {
	toolName := ""
	if m := toolcallCompatInvokeNameRE.FindStringSubmatch(xml); len(m) >= 4 {
		toolName = strings.TrimSpace(firstNonEmpty(m[1], m[2], m[3]))
	}
	if toolName == "" {
		toolName = strings.TrimSpace(fuzzyExtractInvokeName(xml))
	}
	if toolName == "" {
		return toolcallCompatInvokeCall{}, false
	}

	args := make(map[string]any)
	if matches := toolcallCompatParameterName.FindAllStringSubmatch(xml, -1); len(matches) > 0 {
		for _, sub := range matches {
			if len(sub) < 5 {
				continue
			}
			key := strings.TrimSpace(firstNonEmpty(sub[1], sub[2], sub[3]))
			if key == "" {
				continue
			}
			args[key] = parseToolcallCompatParameterValue(sub[4])
		}
	} else {
		for _, p := range fuzzyExtractInvokeParameters(xml) {
			key := strings.TrimSpace(p.name)
			if key == "" {
				continue
			}
			args[key] = parseToolcallCompatParameterValue(p.value)
		}
	}

	argsJSON := "{}"
	if b, err := json.Marshal(args); err == nil {
		argsJSON = string(b)
	}

	return toolcallCompatInvokeCall{
		ToolName: toolName,
		ArgsJSON: argsJSON,
	}, true
}

func extractInvokeXMLAndPlain(s string) ([]string, string, bool) {
	if strings.TrimSpace(s) == "" {
		return nil, s, false
	}

	var (
		invokes []string
		plain   strings.Builder
	)

	i := 0
	for {
		locStart := toolcallCompatInvokeStartRE.FindStringIndex(s[i:])
		if locStart == nil {
			plain.WriteString(s[i:])
			break
		}
		start := i + locStart[0]
		plain.WriteString(s[i:start])

		locEnd := toolcallCompatInvokeEndRE.FindStringIndex(s[start:])
		if locEnd == nil {
			if len(invokes) == 0 {
				return nil, s, false
			}
			return invokes, plain.String(), true
		}
		end := start + locEnd[1]
		invokes = append(invokes, s[start:end])
		i = end
	}

	if len(invokes) == 0 {
		return nil, s, false
	}
	return invokes, plain.String(), true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseToolcallCompatParameterValue(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
		return v
	}

	if !strings.ContainsAny(trimmed, "{[") {
		return trimmed
	}

	repaired := repairJSON(trimmed)
	if err := json.Unmarshal([]byte(repaired), &v); err == nil {
		return v
	}

	lastResort := sanitizeControlCharsForJSON(repaired)
	if err := json.Unmarshal([]byte(lastResort), &v); err == nil {
		return v
	}

	return trimmed
}

func fuzzyExtractInvokeName(xml string) string {
	lower := strings.ToLower(xml)
	start := strings.Index(lower, "<invoke")
	if start == -1 {
		return ""
	}
	tagEndRel := strings.Index(lower[start:], ">")
	if tagEndRel == -1 {
		return ""
	}
	openTag := xml[start : start+tagEndRel+1]
	return parseFuzzyAttr(openTag, "name")
}

type fuzzyParam struct {
	name  string
	value string
}

func fuzzyExtractInvokeParameters(xml string) []fuzzyParam {
	lower := strings.ToLower(xml)
	out := make([]fuzzyParam, 0, 4)

	i := 0
	for {
		start := strings.Index(lower[i:], "<parameter")
		if start == -1 {
			return out
		}
		start += i

		openEndRel := strings.Index(lower[start:], ">")
		if openEndRel == -1 {
			return out
		}
		openEnd := start + openEndRel + 1
		openTag := xml[start:openEnd]
		name := parseFuzzyAttr(openTag, "name")

		closeStartRel := strings.Index(lower[openEnd:], "</parameter>")
		if closeStartRel == -1 {
			return out
		}
		closeStart := openEnd + closeStartRel
		value := xml[openEnd:closeStart]

		out = append(out, fuzzyParam{name: name, value: value})
		i = closeStart + len("</parameter>")
	}
}

func parseFuzzyAttr(tagText string, attrName string) string {
	if tagText == "" || attrName == "" {
		return ""
	}

	lower := strings.ToLower(tagText)
	attr := strings.ToLower(attrName)

	for {
		idx := strings.Index(lower, attr)
		if idx == -1 {
			return ""
		}

		// ensure word boundary
		if idx > 0 {
			prev := lower[idx-1]
			if prev != ' ' && prev != '\t' && prev != '\n' && prev != '\r' && prev != '<' {
				lower = lower[idx+len(attr):]
				tagText = tagText[idx+len(attr):]
				continue
			}
		}

		j := idx + len(attr)
		for j < len(lower) && (lower[j] == ' ' || lower[j] == '\t' || lower[j] == '\n' || lower[j] == '\r') {
			j++
		}
		if j >= len(lower) || lower[j] != '=' {
			lower = lower[j:]
			tagText = tagText[j:]
			continue
		}
		j++
		for j < len(lower) && (lower[j] == ' ' || lower[j] == '\t' || lower[j] == '\n' || lower[j] == '\r') {
			j++
		}
		if j >= len(lower) {
			return ""
		}

		quote := lower[j]
		if quote == '"' || quote == '\'' {
			j++
			start := j
			for j < len(lower) && lower[j] != quote {
				j++
			}
			if j <= start || j >= len(lower) {
				return ""
			}
			return strings.TrimSpace(tagText[start:j])
		}

		start := j
		for j < len(lower) {
			c := lower[j]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '>' {
				break
			}
			j++
		}
		if j <= start {
			return ""
		}
		return strings.TrimSpace(tagText[start:j])
	}
}

func repairJSON(s string) string {
	fixed := strings.TrimSpace(s)
	if fixed == "" {
		return fixed
	}

	fixed = extractFirstJSONArrayOrObject(fixed)
	fixed = toolcallCompatTrailingComma.ReplaceAllString(fixed, "$1")
	fixed = escapeNewlinesInJSONString(fixed)
	fixed = toolcallCompatScalarQuoted.ReplaceAllStringFunc(fixed, func(match string) string {
		sub := toolcallCompatScalarQuoted.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		return strings.Replace(match, `"`+sub[1]+`"`, strings.ToLower(sub[1]), 1)
	})
	fixed = closeUnbalancedJSONDelimiters(fixed)
	return fixed
}

func extractFirstJSONArrayOrObject(s string) string {
	fixed := strings.TrimSpace(s)

	objStart := strings.Index(fixed, "{")
	objEnd := strings.LastIndex(fixed, "}")
	objOK := objStart != -1 && objEnd != -1 && objEnd > objStart

	arrStart := strings.Index(fixed, "[")
	arrEnd := strings.LastIndex(fixed, "]")
	arrOK := arrStart != -1 && arrEnd != -1 && arrEnd > arrStart

	if objOK && (!arrOK || objStart <= arrStart) {
		return fixed[objStart : objEnd+1]
	}
	if arrOK {
		return fixed[arrStart : arrEnd+1]
	}
	return fixed
}

func escapeNewlinesInJSONString(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	inString := false
	escaped := false

	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}

		if inString && r == '\\' {
			b.WriteRune(r)
			escaped = true
			continue
		}

		if r == '"' {
			inString = !inString
			b.WriteRune(r)
			continue
		}

		if inString {
			switch r {
			case '\n':
				b.WriteString(`\n`)
				continue
			case '\r':
				b.WriteString(`\r`)
				continue
			}
		}

		b.WriteRune(r)
	}
	return b.String()
}

func closeUnbalancedJSONDelimiters(s string) string {
	stack := make([]rune, 0, 16)
	inString := false
	escaped := false

	for _, r := range s {
		if escaped {
			escaped = false
			continue
		}

		if inString && r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}

		switch r {
		case '{', '[':
			stack = append(stack, r)
		case '}':
			if len(stack) > 0 && stack[len(stack)-1] == '{' {
				stack = stack[:len(stack)-1]
			}
		case ']':
			if len(stack) > 0 && stack[len(stack)-1] == '[' {
				stack = stack[:len(stack)-1]
			}
		}
	}

	if len(stack) == 0 {
		return s
	}

	var b strings.Builder
	b.Grow(len(s) + len(stack))
	b.WriteString(s)
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			b.WriteRune('}')
		} else {
			b.WriteRune(']')
		}
	}
	return b.String()
}

func sanitizeControlCharsForJSON(s string) string {
	if s == "" {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

type toolcallCompatFunctionCall struct {
	ToolName string
	ArgsJSON string
}

func extractToolcallCompatFunctionCalls(content string) (string, []toolcallCompatFunctionCall, bool) {
	if strings.TrimSpace(content) == "" {
		return content, nil, false
	}

	calls, ok := parseToolcallCompatFunctionCalls(content)
	if !ok || len(calls) == 0 {
		return content, nil, false
	}

	plain := stripToolcallCompatProtocol(content)
	return plain, calls, true
}

func toolcallCompatIDSeed(idSeed string) string {
	signal := strings.TrimSpace(idSeed)
	if signal == "" {
		return "compat"
	}
	seed := signal
	if len(seed) > 12 {
		return seed[:12]
	}
	return seed
}

func parseToolcallCompatFunctionCalls(text string) ([]toolcallCompatFunctionCall, bool) {
	blocks, ok := extractToolcallCompatFunctionCallBlocks(text)
	if !ok || len(blocks) == 0 {
		return nil, false
	}

	calls := make([]toolcallCompatFunctionCall, 0, len(blocks))
	for _, block := range blocks {
		call, ok := parseToolcallCompatFunctionCallBlock(block)
		if !ok {
			continue
		}
		calls = append(calls, call)
	}
	if len(calls) == 0 {
		return nil, false
	}
	return calls, true
}

func extractToolcallCompatFunctionCallBlocks(text string) ([]string, bool) {
	if strings.TrimSpace(text) == "" {
		return nil, false
	}

	var blocks []string
	s := text
	for {
		start := strings.Index(s, "<function_call>")
		if start == -1 {
			break
		}
		s = s[start+len("<function_call>"):]
		end := strings.Index(s, "</function_call>")
		if end == -1 {
			return nil, false
		}
		block := strings.TrimSpace(s[:end])
		block = stripCDATA(block)
		blocks = append(blocks, strings.TrimSpace(block))
		s = s[end+len("</function_call>"):]
	}
	if len(blocks) == 0 {
		return nil, false
	}
	return blocks, true
}

func parseToolcallCompatFunctionCallBlock(block string) (toolcallCompatFunctionCall, bool) {
	if name, args, ok := parseToolcallCompatLegacyBlock(block); ok {
		return toolcallCompatFunctionCall{
			ToolName: strings.TrimSpace(name),
			ArgsJSON: strings.TrimSpace(args),
		}, strings.TrimSpace(name) != ""
	}
	return parseToolcallCompatFunctionCallJSON(block)
}

func parseToolcallCompatLegacyBlock(block string) (string, string, bool) {
	toolStart := strings.Index(block, "<tool>")
	toolEnd := strings.Index(block, "</tool>")
	if toolStart == -1 || toolEnd == -1 || toolEnd <= toolStart {
		return "", "", false
	}
	name := strings.TrimSpace(block[toolStart+len("<tool>") : toolEnd])
	if name == "" {
		return "", "", false
	}

	argsStart := strings.Index(block, "<args_json>")
	argsEnd := strings.Index(block, "</args_json>")
	if argsStart == -1 || argsEnd == -1 || argsEnd <= argsStart {
		return name, "", true
	}
	args := strings.TrimSpace(block[argsStart+len("<args_json>") : argsEnd])
	args = stripCDATA(args)
	return name, args, true
}

func parseToolcallCompatFunctionCallJSON(raw string) (toolcallCompatFunctionCall, bool) {
	raw = strings.TrimSpace(stripCodeFences(raw))
	if raw == "" {
		return toolcallCompatFunctionCall{}, false
	}

	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return toolcallCompatFunctionCall{}, false
	}

	obj, _ := v.(map[string]any)
	if obj == nil {
		return toolcallCompatFunctionCall{}, false
	}

	if _, ok := obj["function_call_record"]; ok {
		return toolcallCompatFunctionCall{}, false
	}

	name, args := extractNameAndArgs(obj)
	if strings.TrimSpace(name) == "" {
		return toolcallCompatFunctionCall{}, false
	}

	argsJSON := "{}"
	switch vv := args.(type) {
	case nil:
		argsJSON = "{}"
	case string:
		if strings.TrimSpace(vv) != "" {
			argsJSON = strings.TrimSpace(vv)
		}
	default:
		if b, err := json.Marshal(vv); err == nil {
			argsJSON = string(b)
		}
	}

	return toolcallCompatFunctionCall{
		ToolName: strings.TrimSpace(name),
		ArgsJSON: strings.TrimSpace(argsJSON),
	}, true
}

func extractNameAndArgs(obj map[string]any) (string, any) {
	if fc, ok := obj["function_call"].(map[string]any); ok {
		if name, _ := fc["name"].(string); strings.TrimSpace(name) != "" {
			return name, fc["arguments"]
		}
	}
	if name, _ := obj["name"].(string); strings.TrimSpace(name) != "" {
		return name, obj["arguments"]
	}
	if fn, ok := obj["function"].(map[string]any); ok {
		if name, _ := fn["name"].(string); strings.TrimSpace(name) != "" {
			return name, fn["arguments"]
		}
	}

	// Fallback: if the object has exactly one key, try its nested object.
	if len(obj) == 1 {
		for _, v := range obj {
			if nested, ok := v.(map[string]any); ok {
				if name, _ := nested["name"].(string); strings.TrimSpace(name) != "" {
					return name, nested["arguments"]
				}
			}
		}
	}
	return "", nil
}

func stripCodeFences(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimLeft(trimmed, " \t\r\n")
	if idx := strings.Index(trimmed, "\n"); idx != -1 {
		head := strings.TrimSpace(trimmed[:idx])
		if head == "json" || head == "JSON" {
			trimmed = trimmed[idx+1:]
		}
	}
	trimmed = strings.TrimSuffix(trimmed, "```")
	return strings.TrimSpace(trimmed)
}

func stripCDATA(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "<![CDATA[") && strings.HasSuffix(trimmed, "]]>") {
		return strings.TrimSuffix(strings.TrimPrefix(trimmed, "<![CDATA["), "]]>")
	}
	return trimmed
}

func stripToolcallCompatProtocol(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}

	var out strings.Builder
	s := text
	for {
		start := strings.Index(s, "<function_call>")
		if start == -1 {
			out.WriteString(s)
			break
		}
		out.WriteString(s[:start])
		s = s[start+len("<function_call>"):]
		end := strings.Index(s, "</function_call>")
		if end == -1 {
			return strings.TrimSpace(text)
		}
		s = s[end+len("</function_call>"):]
	}

	plain := out.String()
	plain = strings.ReplaceAll(plain, "<function_calls>", "")
	plain = strings.ReplaceAll(plain, "</function_calls>", "")
	plain = stripLegacyToolcallTriggerLines(plain)
	return strings.TrimSpace(plain)
}

func stripLegacyToolcallTriggerLines(text string) string {
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "TOOLCALL_COMPAT_TRIGGER_") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

type toolcallCompatStreamDetector struct {
	buf        string
	thinkDepth int
	triggered  bool
}

func newToolcallCompatStreamDetector() *toolcallCompatStreamDetector {
	return &toolcallCompatStreamDetector{}
}

func (d *toolcallCompatStreamDetector) Process(delta string) (bool, string) {
	if d.triggered {
		return false, ""
	}
	if delta == "" {
		return false, ""
	}

	d.buf += delta

	holdback := len("<function_calls>")
	if holdback < len("<function_call>") {
		holdback = len("<function_call>")
	}
	if holdback < len("</function_call>") {
		holdback = len("</function_call>")
	}
	if holdback < len("<think>") {
		holdback = len("<think>")
	}
	if holdback < len("</think>") {
		holdback = len("</think>")
	}

	var out strings.Builder
	i := 0
	for i < len(d.buf) {
		if strings.HasPrefix(d.buf[i:], "<think>") {
			d.thinkDepth++
			out.WriteString("<think>")
			i += len("<think>")
			continue
		}
		if strings.HasPrefix(d.buf[i:], "</think>") {
			if d.thinkDepth > 0 {
				d.thinkDepth--
			}
			out.WriteString("</think>")
			i += len("</think>")
			continue
		}

		if d.thinkDepth == 0 && (strings.HasPrefix(d.buf[i:], "<function_call>") || strings.HasPrefix(d.buf[i:], "<function_calls>")) {
			emitted := out.String()
			d.buf = d.buf[i:]
			d.triggered = true
			return true, emitted
		}

		remaining := len(d.buf) - i
		if remaining < holdback {
			break
		}

		_, size := utf8.DecodeRuneInString(d.buf[i:])
		if size <= 0 {
			break
		}
		out.WriteString(d.buf[i : i+size])
		i += size
	}

	d.buf = d.buf[i:]
	return false, out.String()
}

func (d *toolcallCompatStreamDetector) TakeRemainder() string {
	rest := d.buf
	d.buf = ""
	return rest
}

func (d *toolcallCompatStreamDetector) FlushRemainder() string {
	if d.triggered {
		return ""
	}
	return d.TakeRemainder()
}

func seedForLog(idSeed string) string {
	s := strings.TrimSpace(idSeed)
	if s == "" {
		return "compat"
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

type toolcallCompatTriggerDetector struct {
	buf       string
	trigger   string
	triggered bool
}

func newToolcallCompatTriggerDetector(triggerSignal string) *toolcallCompatTriggerDetector {
	return &toolcallCompatTriggerDetector{trigger: triggerSignal}
}

func (d *toolcallCompatTriggerDetector) Process(delta string) (bool, string) {
	if d.triggered || delta == "" {
		return false, ""
	}

	d.buf += delta

	if idx := strings.Index(d.buf, d.trigger); idx != -1 {
		emitted := d.buf[:idx]
		d.buf = d.buf[idx+len(d.trigger):]
		d.triggered = true
		return true, emitted
	}

	holdback := len(d.trigger) - 1
	if holdback <= 0 {
		emitted := d.buf
		d.buf = ""
		return false, emitted
	}

	if len(d.buf) <= holdback {
		return false, ""
	}

	cut := len(d.buf) - holdback
	for cut > 0 && !utf8.RuneStart(d.buf[cut]) {
		cut--
	}
	emitted := d.buf[:cut]
	d.buf = d.buf[cut:]
	return false, emitted
}

func (d *toolcallCompatTriggerDetector) TakeRemainder() string {
	rest := d.buf
	d.buf = ""
	return rest
}

func (d *toolcallCompatTriggerDetector) FlushRemainder() string {
	if d.triggered {
		return ""
	}
	return d.TakeRemainder()
}

func extractFirstInvokeXML(protocolBuffer string) (string, bool) {
	if strings.TrimSpace(protocolBuffer) == "" {
		return "", false
	}
	locStart := toolcallCompatInvokeStartRE.FindStringIndex(protocolBuffer)
	if locStart == nil {
		return "", false
	}
	start := locStart[0]

	locEnd := toolcallCompatInvokeEndRE.FindStringIndex(protocolBuffer[start:])
	if locEnd == nil {
		return "", false
	}
	end := start + locEnd[1]
	return protocolBuffer[start:end], true
}

func extractAllInvokeXML(protocolBuffer string) ([]string, bool) {
	invokes, _, ok := extractInvokeXMLAndPlain(protocolBuffer)
	return invokes, ok
}
