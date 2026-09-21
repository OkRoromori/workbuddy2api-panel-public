// responses.go OpenAI **Responses API**（`POST /v1/responses`）兼容层。
//
// 为什么需要：Codex CLI 只会调 `/v1/responses`，没有"降级到 chat/completions"的开关。
// 本网关上游是 Chat Completions 协议，所以这里做一层双向翻译：
//
//	入站  Responses 请求  → Chat Completions 请求  → 上游
//	出站  上游 Chat 响应  → Responses 响应/SSE 事件 → 客户端
//
// 翻译只碰**结构**，不碰语义：消息、工具、（流式）增量原样搬运。
// 流式是主要成本——Responses 的事件名（response.output_text.delta 等）与
// Chat 的 `data: {"choices":[{"delta":…}]}` 完全不同，必须逐块转写。
package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 入站：Responses 请求 → Chat Completions 请求
// ---------------------------------------------------------------------------

// respInputItem Responses 的 input 数组元素。字段是**联合体**：
// 一条 item 可能是 message / function_call / function_call_output / reasoning，
// 靠 type 区分，所以这里全部用 RawMessage 接，再按 type 分发。
type respInputItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	// function_call
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	CallID    string `json:"call_id"`
	// function_call_output
	Output string `json:"output"`
}

type respTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	// 兼容客户端把 Chat 形状（嵌套 function）塞进来的情况
	Function *struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// responsesToChat 把 Responses 请求体翻译成 Chat Completions 请求体。
//
// 保留的字段：model / stream / temperature / top_p / tools / tool_choice /
// max_output_tokens→max_tokens。其余（store、include、previous_response_id 等
// 服务端状态类字段）上游不支持，直接丢弃——上游是无状态转发，留着只会 400。
func responsesToChat(body []byte) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("responses 请求不是合法 JSON: %w", err)
	}

	out := map[string]any{}
	for _, k := range []string{"model", "stream", "temperature", "top_p", "tool_choice"} {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	// max_output_tokens → max_tokens（名字不同，语义相同）
	if v, ok := in["max_output_tokens"]; ok {
		out["max_tokens"] = v
	}

	msgs, err := responsesInputToMessages(in)
	if err != nil {
		return nil, err
	}
	// instructions 等价于 system 提示，放在最前。
	if s, _ := in["instructions"].(string); strings.TrimSpace(s) != "" {
		msgs = append([]any{map[string]any{"role": "system", "content": s}}, msgs...)
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("responses 请求里没有可用输入（input 为空）")
	}
	out["messages"] = msgs

	if tl, ok := in["tools"].([]any); ok && len(tl) > 0 {
		out["tools"] = responsesToolsToChat(tl)
	}
	if tc, ok := out["tool_choice"]; ok {
		out["tool_choice"] = responsesToolChoiceToChat(tc)
	}
	return json.Marshal(out)
}

// responsesInputToMessages 把 input（字符串或 item 数组）翻成 chat messages。
func responsesInputToMessages(in map[string]any) ([]any, error) {
	raw, ok := in["input"]
	if !ok || raw == nil {
		return nil, nil
	}
	// 形态一：纯字符串 = 单条 user 消息
	if s, ok := raw.(string); ok {
		return []any{map[string]any{"role": "user", "content": s}}, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("input 既不是字符串也不是数组")
	}

	var msgs []any
	for _, e := range arr {
		it, ok := e.(map[string]any)
		if !ok {
			continue
		}
		switch it["type"] {
		case "function_call":
			// Responses 的 function_call 是独立 item；Chat 要求挂在 assistant.tool_calls 上。
			callID, _ := it["call_id"].(string)
			if callID == "" {
				callID, _ = it["id"].(string)
			}
			name, _ := it["name"].(string)
			args, _ := it["arguments"].(string)
			msgs = append(msgs, map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []any{map[string]any{
					"id": callID, "type": "function",
					"function": map[string]any{"name": name, "arguments": args},
				}},
			})
		case "function_call_output":
			callID, _ := it["call_id"].(string)
			out, _ := it["output"].(string)
			if out == "" {
				// output 也可能是结构化对象 → 序列化回字符串
				if o, ok := it["output"]; ok && o != nil {
					if b, err := json.Marshal(o); err == nil {
						out = string(b)
					}
				}
			}
			msgs = append(msgs, map[string]any{
				"role": "tool", "tool_call_id": callID, "content": out,
			})
		case "message", "":
			role, _ := it["role"].(string)
			if role == "" {
				role = "user"
			}
			content := responsesContentToChat(it["content"])
			if content == nil {
				continue
			}
			msgs = append(msgs, map[string]any{"role": role, "content": content})
		default:
			// reasoning 等类型上游不认识，丢掉（它们是思考过程的回显，不是输入）
		}
	}
	return msgs, nil
}

// responsesContentToChat content 可能是字符串，也可能是 part 数组
// （input_text / output_text / input_image / refusal）。Chat 侧同样支持数组形态，
// 这里只做类型名映射：input_text/output_text → text。
func responsesContentToChat(v any) any {
	switch c := v.(type) {
	case nil:
		return nil
	case string:
		return c
	case []any:
		var parts []any
		for _, e := range c {
			p, ok := e.(map[string]any)
			if !ok {
				continue
			}
			switch p["type"] {
			case "input_text", "output_text", "text":
				t, _ := p["text"].(string)
				parts = append(parts, map[string]any{"type": "text", "text": t})
			case "input_image":
				// Responses 的 image_url 本身就是字符串；Chat 要求包一层对象
				if u, ok := p["image_url"].(string); ok && u != "" {
					parts = append(parts, map[string]any{
						"type": "image_url", "image_url": map[string]any{"url": u},
					})
				}
			case "refusal":
				if t, ok := p["refusal"].(string); ok {
					parts = append(parts, map[string]any{"type": "text", "text": t})
				}
			}
		}
		if len(parts) == 0 {
			return nil
		}
		return parts
	}
	return nil
}

// responsesToolsToChat Responses 的工具是**扁平**结构（name 在顶层），
// Chat 要求嵌在 function 下。这是两个协议最容易翻错的地方。
func responsesToolsToChat(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		// 已经是 Chat 形状（有嵌套 function）→ 原样保留
		if _, nested := m["function"]; nested {
			out = append(out, m)
			continue
		}
		if ty, _ := m["type"].(string); ty != "" && ty != "function" {
			// 非 function 类工具（web_search 等）上游不支持，跳过
			continue
		}
		fn := map[string]any{"name": m["name"]}
		if d, ok := m["description"]; ok {
			fn["description"] = d
		}
		if p, ok := m["parameters"]; ok {
			fn["parameters"] = p
		} else {
			// 上游要求 parameters 存在；缺省给空对象 schema
			fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

// responsesToolChoiceToChat 只改形状不改语义：
//
//	{"type":"function","name":"x"} → {"type":"function","function":{"name":"x"}}
//	"auto"/"required"/"none"       → 原样
//	其余（对象但没 name）           → "auto"（丢掉不如给个能用的默认）
func responsesToolChoiceToChat(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	name, _ := m["name"].(string)
	if name == "" {
		return "auto"
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name}}
}
