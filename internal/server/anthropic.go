// anthropic.go Anthropic **Messages API**（`POST /v1/messages`）兼容层。
//
// 为什么需要：Claude Code / ccswitch 默认说 Anthropic 协议，只认 `/v1/messages`。
// 本网关上游是 Chat Completions，所以这里做双向翻译（与 responses.go 同构）：
//
//	入站  Anthropic 请求 → Chat 请求 → 上游
//	出站  上游 Chat 响应 → Anthropic 响应/SSE 事件 → 客户端
//
// 与 Responses 层最大的结构差异：
//   - system 是**顶层字段**（不是 messages 里的一条）
//   - content **总是** block 数组（text / image / tool_use / tool_result）
//   - 工具用 input_schema（不是 parameters），工具调用是**内容块**（不是 message 字段）
//   - tool_result 出现在 **user** 消息里，Chat 要求它是独立的 tool 消息
package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

// anthropicToChat 把 Anthropic Messages 请求翻成 Chat Completions 请求。
//
// max_tokens 在 Anthropic 侧是**必填**，直接搬；stop_sequences → stop。
// 上游不认识的字段（metadata、thinking、anthropic_beta 等）一律丢弃——
// 留着会让上游 400，而它们对上游语义无意义。
func anthropicToChat(body []byte) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("anthropic 请求不是合法 JSON: %w", err)
	}
	out := map[string]any{}
	for _, k := range []string{"model", "stream", "temperature", "top_p", "max_tokens"} {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	// Anthropic 的 max_tokens 必填；缺了给个保守默认（上游也要求存在）
	if _, ok := out["max_tokens"]; !ok {
		out["max_tokens"] = 4096
	}
	if v, ok := in["stop_sequences"]; ok {
		out["stop"] = v
	}

	var msgs []any
	// system 可以是字符串，也可以是 [{type:text,text:...}] 数组
	if s := anthropicSystemText(in["system"]); s != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": s})
	}

	rawMsgs, _ := in["messages"].([]any)
	for _, e := range rawMsgs {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		text, toolCalls, toolResults := anthropicBlocks(m["content"])

		// tool_result 必须变成独立的 tool 消息（Chat 的协议要求），
		// 且**先于**同一条 user 消息里的文字——顺序反了模型会看不懂上下文。
		for _, tr := range toolResults {
			msgs = append(msgs, tr)
		}
		if text == "" && len(toolCalls) == 0 {
			continue
		}
		msg := map[string]any{"role": role}
		if text == "" {
			msg["content"] = nil
		} else {
			msg["content"] = text
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		msgs = append(msgs, msg)
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("anthropic 请求里没有可用消息")
	}
	out["messages"] = msgs

	if tl, ok := in["tools"].([]any); ok && len(tl) > 0 {
		out["tools"] = anthropicToolsToChat(tl)
	}
	if tc, ok := in["tool_choice"]; ok {
		out["tool_choice"] = anthropicToolChoiceToChat(tc)
	}
	return json.Marshal(out)
}

// anthropicSystemText system 字段归并成一段文本（字符串或 text block 数组两种形态）。
func anthropicSystemText(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []any:
		var b strings.Builder
		for _, e := range s {
			if m, ok := e.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}

// anthropicBlocks 拆一条 Anthropic 消息的 content block 数组，归并成
// Chat 需要的三样东西：纯文本、tool_calls、独立的 tool 消息。
func anthropicBlocks(v any) (text string, toolCalls []any, toolResults []any) {
	// content 也可能是裸字符串
	if s, ok := v.(string); ok {
		return s, nil, nil
	}
	arr, _ := v.([]any)
	var texts []string
	for _, e := range arr {
		b, ok := e.(map[string]any)
		if !ok {
			continue
		}
		switch b["type"] {
		case "text":
			if t, ok := b["text"].(string); ok {
				texts = append(texts, t)
			}
		case "image":
			// 上游是文本模型，图片无法表达 → 不静默丢弃，留一行可读的占位，
			// 免得模型以为用户什么都没发。
			texts = append(texts, "[图片]")
		case "tool_use":
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			args, _ := json.Marshal(b["input"])
			toolCalls = append(toolCalls, map[string]any{
				"id": id, "type": "function",
				"function": map[string]any{"name": name, "arguments": string(args)},
			})
		case "tool_result":
			id, _ := b["tool_use_id"].(string)
			toolResults = append(toolResults, map[string]any{
				"role": "tool", "tool_call_id": id,
				"content": anthropicToolResultText(b["content"]),
			})
		}
	}
	return strings.Join(texts, "\n"), toolCalls, toolResults
}

// anthropicToolResultText tool_result 的 content 可能是字符串，也可能是 block 数组。
func anthropicToolResultText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	arr, ok := v.([]any)
	if !ok {
		if v == nil {
			return ""
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	var texts []string
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			if t, ok := m["text"].(string); ok {
				texts = append(texts, t)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// anthropicToolsToChat Anthropic 工具用 input_schema，Chat 用 parameters。
func anthropicToolsToChat(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn := map[string]any{"name": m["name"]}
		if d, ok := m["description"]; ok {
			fn["description"] = d
		}
		if s, ok := m["input_schema"]; ok {
			fn["parameters"] = s
		} else {
			fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

// anthropicToolChoiceToChat 三个形态的语义映射：
//
//	{"type":"auto"}            → "auto"      模型自行决定
//	{"type":"any"}             → "required"  **必须**用某个工具
//	{"type":"tool","name":"x"} → {"type":"function","function":{"name":"x"}}
//	{"type":"none"}            → "none"
func anthropicToolChoiceToChat(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	switch m["type"] {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		name, _ := m["name"].(string)
		if name == "" {
			return "auto"
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}
	}
	return "auto"
}

// stopReasonFromFinish 把 Chat 的 finish_reason 映射成 Anthropic 的 stop_reason。
//
// 两边枚举不同名，映射错了客户端会误判（例如把 tool_use 当成正常结束，
// 于是不再执行工具、直接断掉）。
func stopReasonFromFinish(fr string) (string, bool) {
	switch fr {
	case "stop":
		return "end_turn", false
	case "length":
		return "max_tokens", false
	case "tool_calls", "function_call":
		return "tool_use", true
	case "content_filter":
		return "refusal", false
	}
	return "end_turn", false
}
