package proxy

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	app_errors "gpt-load/internal/errors"
	"sort"
	"strings"
)

type toolcallCompatRequestMeta struct {
	IDSeed  string
	Trigger string
}

type toolcallBackref struct{}

func preprocessToolcallCompatChatCompletionsRequest(body []byte) ([]byte, *toolcallCompatRequestMeta, *app_errors.APIError) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error())
	}

	rawMessages, hasMessages := payload["messages"]
	messages, ok := rawMessages.([]any)
	if !ok {
		if hasMessages {
			return nil, nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid messages format")
		}
		return body, nil, nil
	}

	hasToolsOrChoice := payload["tools"] != nil || payload["tool_choice"] != nil

	hasCompatSignals := hasToolsOrChoice
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role == "tool" {
			hasCompatSignals = true
			break
		}
		if m["tool_calls"] != nil {
			hasCompatSignals = true
			break
		}
	}

	if !hasCompatSignals {
		return body, nil, nil
	}

	seed := generateToolcallCompatIDSeed()
	trigger := generateToolcallCompatTriggerSignal()

	if hasToolsOrChoice {
		prompt, err := buildToolcallCompatSystemPrompt(payload["tools"], trigger)
		if err != nil {
			return nil, nil, app_errors.NewAPIError(app_errors.ErrBadRequest, err.Error())
		}
		delete(payload, "tools")
		delete(payload, "tool_choice")

		systemMsg := map[string]any{
			"role":    "system",
			"content": prompt,
		}
		messages = append([]any{systemMsg}, messages...)
	}

	toolCallsByID := make(map[string]toolcallBackref)
	for i := range messages {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		if role == "assistant" && msg["tool_calls"] != nil {
			toolCalls, backrefs, err := parseOpenAIToolCalls(msg["tool_calls"])
			if err != nil {
				return nil, nil, app_errors.NewAPIError(app_errors.ErrBadRequest, err.Error())
			}
			for id := range backrefs {
				toolCallsByID[id] = toolcallBackref{}
			}

			appendProtocolizedToolCalls(msg, trigger, toolCalls)
			delete(msg, "tool_calls")
			continue
		}

		if role == "tool" {
			callID, ok := msg["tool_call_id"].(string)
			if !ok || strings.TrimSpace(callID) == "" {
				return nil, nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "tool message missing tool_call_id")
			}
			_, ok = toolCallsByID[callID]
			if !ok {
				return nil, nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("tool_call_id not found: %s", callID))
			}
			result := stringifyToolResultContent(msg["content"])
			msg["role"] = "user"
			msg["content"] = buildToolcallCompatToolResultText(callID, result)
			delete(msg, "tool_call_id")
		}
	}

	payload["messages"] = messages

	out, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, app_errors.NewAPIError(app_errors.ErrInternalServer, "failed to serialize request")
	}
	return out, &toolcallCompatRequestMeta{IDSeed: seed, Trigger: trigger}, nil
}

type toolcallInfo struct {
	ToolName string
	ArgsJSON string
}

func parseOpenAIToolCalls(raw any) ([]toolcallInfo, map[string]toolcallBackref, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("invalid tool_calls format")
	}

	infos := make([]toolcallInfo, 0, len(items))
	backrefs := make(map[string]toolcallBackref, len(items))
	for _, item := range items {
		call, ok := item.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("invalid tool_call entry")
		}

		id, _ := call["id"].(string)
		fn, ok := call["function"].(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("invalid tool_call function")
		}
		name, _ := fn["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, nil, fmt.Errorf("tool_call missing function name")
		}

		args := ""
		if v, ok := fn["arguments"].(string); ok {
			args = v
		} else if fn["arguments"] != nil {
			args = stringifyContent(fn["arguments"])
		}

		infos = append(infos, toolcallInfo{
			ToolName: name,
			ArgsJSON: args,
		})
		if strings.TrimSpace(id) != "" {
			backrefs[id] = toolcallBackref{}
		}
	}
	return infos, backrefs, nil
}

func buildToolcallCompatSystemPrompt(tools any, triggerSignal string) (string, error) {
	toolsXML, err := convertOpenAIToolsToXML(tools)
	if err != nil {
		return "", err
	}

	template := toolcallCompatSystemPromptTemplate
	template = strings.ReplaceAll(template, `\b`, "\b")
	template = strings.ReplaceAll(template, "{trigger_signal}", triggerSignal)
	template = strings.ReplaceAll(template, "{tools_list}", toolsXML)
	return template, nil
}

const toolcallCompatSystemPromptTemplate = `
In this environment you have access to a set of tools you can use to answer the user's question.  

When you need to use a tool, you MUST strictly follow the format below.

**1. Available Tools:**
Here is the list of tools you can use. You have access ONLY to these tools and no others.
<antml\b:tools>
{tools_list}
</antml\b:tools>

**2. Tool Call Procedure:**
When you decide to call a tool, you MUST output EXACTLY this trigger signal: {trigger_signal}
The trigger signal MUST be output on a completely empty line by itself before any tool calls.
Do NOT add any other text, spaces, or characters before or after {trigger_signal} on that line.
You may provide explanations or reasoning before outputting {trigger_signal}, but once you decide to make a tool call, {trigger_signal} must come first.
You MUST output the trigger signal {trigger_signal} ONLY ONCE per response. Never output multiple trigger signals in a single response.

After outputting the trigger signal, immediately provide your tool calls enclosed in <invoke> XML tags.

**3. XML Format for Tool Calls:**
Your tool calls must be structured EXACTLY as follows. This is the ONLY format you can use, and any deviation will result in failure.

<antml\b:format>
{trigger_signal}
<invoke name="Write">
<parameter name="file_path">C:\path\weather.css</parameter>
<parameter name="content"> body {{ background-color: lightblue; }} </parameter>
</invoke>
</antml\b:format>

IMPORTANT RULES:
  - You may provide explanations or reasoning before deciding to call a tool.
  - Once you decide to call a tool, you must first output the trigger signal {trigger_signal} on a separate line by itself.
  - The trigger signal may only appear once per response and must not be repeated.
  - Tool calls must use the exact XML format below: immediately after the trigger signal, use <invoke> and <parameter> tags.
  - No additional text may be added after the closing </invoke> tag.
  - Parameters must retain punctuation (including hyphen prefixes) exactly as defined.
  - Encode arrays and objects in JSON before placing inside <parameter>.
  - Be concise when not using tools.
  - 在调用工具后会得到工具调用结果，所以请在一次工具调用得到结果后再调用下一个。
  
  `

func appendProtocolizedToolCalls(message map[string]any, triggerSignal string, toolCalls []toolcallInfo) {
	payload := buildToolcallCompatToolCallsText(triggerSignal, toolCalls)
	appendToMessageContent(message, payload)
}

func buildToolcallCompatToolCallsText(triggerSignal string, toolCalls []toolcallInfo) string {
	var b strings.Builder
	for i, call := range toolCalls {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(triggerSignal)
		b.WriteString("\n")
		b.WriteString(buildToolcallCompatInvokeXML(call.ToolName, call.ArgsJSON))
	}
	return b.String()
}

func buildToolcallCompatInvokeXML(toolName string, argsJSON string) string {
	argsAny := coerceJSONString(argsJSON)
	args, _ := argsAny.(map[string]any)
	if args == nil {
		args = map[string]any{}
	}

	keys := make([]string, 0, len(args))
	for k := range args {
		if strings.TrimSpace(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var params strings.Builder
	for i, k := range keys {
		if i > 0 {
			params.WriteString("\n")
		}
		params.WriteString(`<parameter name="`)
		params.WriteString(escapeXMLAttr(k))
		params.WriteString(`">`)
		params.WriteString(toolcallCompatFormatParameterValue(args[k]))
		params.WriteString(`</parameter>`)
	}

	return `<invoke name="` + escapeXMLAttr(toolName) + `">` + "\n" + params.String() + "\n</invoke>"
}

func toolcallCompatFormatParameterValue(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	encoded, err := marshalJSONNoHTMLEscape(v)
	if err == nil {
		return encoded
	}
	return stringifyContent(v)
}

func buildToolcallCompatToolResultText(toolCallID string, result string) string {
	return `<tool_result id="` + escapeXMLAttr(toolCallID) + `">` + result + `</tool_result>`
}

func appendToMessageContent(message map[string]any, extra string) {
	current := stringifyContent(message["content"])
	current = strings.TrimRight(current, "\n")
	if current != "" {
		current += "\n\n"
	}
	message["content"] = current + extra
}

func stringifyContent(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

func stringifyToolResultContent(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	encoded, err := marshalJSONNoHTMLEscape(v)
	if err == nil {
		return encoded
	}
	return stringifyContent(v)
}

func buildToolcallCompatFunctionCallJSON(toolName string, argsJSON string) string {
	payload := map[string]any{
		"function_call": map[string]any{
			"name":      toolName,
			"arguments": coerceJSONString(argsJSON),
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return `{"function_call":{"name":"` + toolName + `","arguments":{}}}`
	}
	return string(b)
}

func coerceJSONString(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}

	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
		return v
	}
	return trimmed
}

func generateToolcallCompatIDSeed() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "compat"
	}
	return hex.EncodeToString(b[:])
}

func generateToolcallCompatTriggerSignal() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "<<CALL_000000>>"
	}
	return "<<CALL_" + hex.EncodeToString(b[:]) + ">>"
}

func describeToolChoice(toolChoice any) string {
	if toolChoice == nil {
		return ""
	}

	if s, ok := toolChoice.(string); ok {
		switch strings.TrimSpace(s) {
		case "none":
			return "工具选择策略已指定：不得调用任何工具。"
		case "required":
			return "工具选择策略已指定：必须调用至少一个工具。"
		default:
			return ""
		}
	}

	if m, ok := toolChoice.(map[string]any); ok {
		if t, _ := m["type"].(string); strings.TrimSpace(t) == "function" {
			if fn, ok := m["function"].(map[string]any); ok {
				if name, _ := fn["name"].(string); strings.TrimSpace(name) != "" {
					return fmt.Sprintf("工具选择策略已指定：必须调用工具 %q。", strings.TrimSpace(name))
				}
			}
		}
	}

	return ""
}

func convertOpenAIToolsToXML(raw any) (string, error) {
	if raw == nil {
		return "<function_list>None</function_list>", nil
	}

	items, ok := raw.([]any)
	if !ok {
		return "", fmt.Errorf("invalid tools format")
	}

	var b strings.Builder
	validTools := 0
	for _, item := range items {
		toolObj, ok := item.(map[string]any)
		if !ok {
			return "", fmt.Errorf("invalid tools format")
		}
		fn, ok := toolObj["function"].(map[string]any)
		if !ok {
			continue
		}

		name, _ := fn["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		desc, _ := fn["description"].(string)
		desc = strings.TrimSpace(desc)
		if desc == "" {
			desc = "None"
		}

		required := make([]string, 0)
		requiredSet := make(map[string]struct{})
		props := make(map[string]any)
		if schema, ok := fn["parameters"].(map[string]any); ok {
			if propsAny, ok := schema["properties"].(map[string]any); ok && propsAny != nil {
				props = propsAny
			}
			if reqAny, ok := schema["required"].([]any); ok {
				for _, r := range reqAny {
					s, ok := r.(string)
					s = strings.TrimSpace(s)
					if !ok || s == "" {
						continue
					}
					required = append(required, s)
					requiredSet[s] = struct{}{}
				}
			}
		}

		paramNames := make([]string, 0, len(props))
		for k := range props {
			if strings.TrimSpace(k) == "" {
				continue
			}
			paramNames = append(paramNames, k)
		}
		sort.Strings(paramNames)

		parametersXML := ""
		if len(paramNames) > 0 {
			var pb strings.Builder
			for _, paramName := range paramNames {
				propAny := props[paramName]
				paramInfo, _ := propAny.(map[string]any)
				pType, _ := paramInfo["type"].(string)
				pType = strings.TrimSpace(pType)
				if pType == "" {
					pType = "any"
				}
				pDesc, _ := paramInfo["description"].(string)
				pDesc = strings.TrimSpace(pDesc)

				enumAny := paramInfo["enum"]
				enumJSON := ""
				if enumAny != nil {
					if encoded, err := marshalJSONNoHTMLEscape(enumAny); err == nil {
						enumJSON = encoded
					}
				}

				_, req := requiredSet[paramName]

				lines := []string{
					`    <parameter name="` + escapeXMLAttr(paramName) + `">`,
					`      <type>` + pType + `</type>`,
					`      <required>` + fmt.Sprintf("%t", req) + `</required>`,
				}
				if pDesc != "" {
					lines = append(lines, `      <description>`+escapeToolcallCompatText(pDesc)+`</description>`)
				}
				if enumJSON != "" {
					lines = append(lines, `      <enum>`+escapeToolcallCompatText(enumJSON)+`</enum>`)
				}
				lines = append(lines, "    </parameter>")
				if pb.Len() > 0 {
					pb.WriteString("\n")
				}
				pb.WriteString(strings.Join(lines, "\n"))
			}
			parametersXML = pb.String()
		}

		requiredXML := "    <param>None</param>"
		if len(required) > 0 {
			var rb strings.Builder
			for i, r := range required {
				if i > 0 {
					rb.WriteString("\n")
				}
				rb.WriteString("    <param>")
				rb.WriteString(r)
				rb.WriteString("</param>")
			}
			requiredXML = rb.String()
		}

		if validTools == 0 {
			b.WriteString("<function_list>\n")
		} else {
			b.WriteString("\n")
		}

		validTools++
		b.WriteString(fmt.Sprintf(`  <tool id="%d">`, validTools))
		b.WriteString("\n")
		b.WriteString("    <name>")
		b.WriteString(name)
		b.WriteString("</name>\n")
		b.WriteString("    <description>")
		b.WriteString(escapeToolcallCompatText(desc))
		b.WriteString("</description>\n")
		b.WriteString("    <required>\n")
		b.WriteString(requiredXML)
		b.WriteString("\n")
		b.WriteString("    </required>\n")
		if parametersXML == "" {
			b.WriteString("    <parameters>None</parameters>\n")
		} else {
			b.WriteString("    <parameters>\n")
			b.WriteString(parametersXML)
			b.WriteString("\n")
			b.WriteString("    </parameters>\n")
		}
		b.WriteString("  </tool>")
	}

	if validTools == 0 {
		return "<function_list>None</function_list>", nil
	}

	b.WriteString("\n</function_list>")
	return b.String(), nil
}

func escapeToolcallCompatText(text string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(text)
}

func marshalJSONNoHTMLEscape(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func escapeXMLAttr(s string) string {
	if s == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		`&`, "&amp;",
		`<`, "&lt;",
		`>`, "&gt;",
		`"`, "&quot;",
		`'`, "&apos;",
	)
	return replacer.Replace(s)
}
