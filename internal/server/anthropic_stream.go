package server

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ---------------------------------------------------------------------------
// 出站：Chat 响应 → Anthropic 响应
// ---------------------------------------------------------------------------

// chatResponseToAnthropic 把非流式 Chat 响应翻成 Anthropic Messages 响应。
//
// 形状差异：
//   - Anthropic 的 content 是 **block 数组**（text block + tool_use block），
//     而 Chat 把文字放 content、工具调用放 tool_calls
//   - stop_reason 枚举不同名（见 stopReasonFromFinish）
//   - usage 只有 input_tokens / output_tokens，没有 total_tokens
func chatResponseToAnthropic(chat []byte, model string) ([]byte, error) {
	var c struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(chat, &c); err != nil {
		return nil, fmt.Errorf("上游响应不是合法 chat 结构: %w", err)
	}
	if model == "" {
		model = c.Model
	}
	content := []any{}
	stopReason := "end_turn"
	for _, ch := range c.Choices {
		if ch.Message.Content != "" {
			content = append(content, map[string]any{"type": "text", "text": ch.Message.Content})
		}
		for _, tc := range ch.Message.ToolCalls {
			var input any = map[string]any{}
			// arguments 是 JSON **字符串**，Anthropic 要求 input 是**对象**
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
					input = map[string]any{"_raw": tc.Function.Arguments}
				}
			}
			content = append(content, map[string]any{
				"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input,
			})
		}
		if r, _ := stopReasonFromFinish(ch.FinishReason); r != "" {
			stopReason = r
		}
	}
	resp := map[string]any{
		"id":          anthropicID(c.ID),
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     content,
		"stop_reason": stopReason,
		// Anthropic 要求该字段存在（无命中时为 null）
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  c.Usage.PromptTokens,
			"output_tokens": c.Usage.CompletionTokens,
		},
	}
	return json.Marshal(resp)
}

func anthropicID(seed string) string {
	if seed == "" {
		return respID("msg", "")
	}
	return respID("msg", seed)
}

// ---------------------------------------------------------------------------
// 出站：Chat SSE → Anthropic SSE
// ---------------------------------------------------------------------------

// anthropicStreamer 把 Chat SSE 帧转写成 Anthropic 事件流。
//
// 与 Responses 最大的不同：Anthropic 的 SSE **同时有 `event:` 行和 `data:` 行**，
// 而且事件是有状态的序号流（content_block_start → delta* → stop，index 递增）。
// 文本块和工具块各有自己的 index，必须按"用到才开、变了就关"来管理。
type anthropicStreamer struct {
	w        io.Writer
	model    string
	msgID    string
	started  bool
	finished bool
	// 当前打开的块：-1 = 没开。文本与工具分开记，因为它们的 index 要连续递增。
	textIdx  int
	textOpen bool
	toolIdx  map[int]int    // chat 的 tool index → anthropic 的 content block index
	toolName map[int]string // 首个分片里的函数名（收尾要用）
	nextIdx  int            // 下一个可用的 block index
	usage    map[string]any
	flush    func()
}

func newAnthropicStreamer(w io.Writer, model string) *anthropicStreamer {
	return &anthropicStreamer{
		w: w, model: model, msgID: anthropicID(""),
		textIdx: -1, toolIdx: map[int]int{}, toolName: map[int]string{},
	}
}

// send 写一条 Anthropic SSE 事件（event: + data: 两行）。
func (s *anthropicStreamer) send(event string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b)
	if s.flush != nil {
		s.flush()
	}
}

// Start 发 message_start。
func (s *anthropicStreamer) Start() {
	if s.started {
		return
	}
	s.started = true
	s.send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": s.msgID, "type": "message", "role": "assistant",
			"model": s.model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

// openText 惰性开文本块。
func (s *anthropicStreamer) openText() {
	if s.textOpen {
		return
	}
	s.textOpen = true
	s.textIdx = s.nextIdx
	s.nextIdx++
	s.send("content_block_start", map[string]any{
		"type": "content_block_start", "index": s.textIdx,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

// closeText 关掉已开的文本块（工具块开始前必须关，否则 index 会交叉）。
func (s *anthropicStreamer) closeText() {
	if !s.textOpen {
		return
	}
	s.textOpen = false
	s.send("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": s.textIdx,
	})
}

// Delta 文本增量。
func (s *anthropicStreamer) Delta(text string) {
	if text == "" {
		return
	}
	s.openText()
	s.send("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": s.textIdx,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

// ToolCallDelta 工具调用增量。
//
// Chat 按 index 分片、只有首片带 id/name；Anthropic 要求先
// content_block_start（带完整 tool_use 头和空 input），再发
// input_json_delta 拼参数。所以这里也要按 **index** 归并（同 responses 层的教训）。
func (s *anthropicStreamer) ToolCallDelta(idx int, id, name, argsDelta string) {
	blockIdx, ok := s.toolIdx[idx]
	if !ok {
		s.closeText() // 文本块先收尾，保证 index 不交叉
		blockIdx = s.nextIdx
		s.nextIdx++
		s.toolIdx[idx] = blockIdx
		if name != "" {
			s.toolName[idx] = name
		}
		s.send("content_block_start", map[string]any{
			"type": "content_block_start", "index": blockIdx,
			"content_block": map[string]any{
				"type": "tool_use", "id": id, "name": s.toolName[idx], "input": map[string]any{},
			},
		})
	}
	if argsDelta != "" {
		s.send("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": blockIdx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": argsDelta},
		})
	}
}

// Finish 收尾：关掉所有打开的块 → message_delta（带 stop_reason）→ message_stop。
func (s *anthropicStreamer) Finish(finishReason string) {
	if s.finished {
		return
	}
	s.finished = true
	s.closeText()
	for _, bi := range s.toolIdx {
		s.send("content_block_stop", map[string]any{
			"type": "content_block_stop", "index": bi,
		})
	}
	stop, _ := stopReasonFromFinish(finishReason)
	outTok := int64(0)
	if s.usage != nil {
		if v, ok := s.usage["output_tokens"].(int64); ok {
			outTok = v
		}
	}
	s.send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": outTok},
	})
	s.send("message_stop", map[string]any{"type": "message_stop"})
}

// Frame 处理一个 Chat SSE 帧（容错：整行含 "data: " 前缀也能收，见 responses 层的教训）。
func (s *anthropicStreamer) Frame(payload string) bool {
	payload = strings.TrimSpace(payload)
	if strings.HasPrefix(payload, "data:") {
		payload = strings.TrimSpace(strings.TrimPrefix(payload, "data:"))
	}
	if payload == "" {
		return false
	}
	if payload == "[DONE]" {
		s.Finish("stop")
		return true
	}
	// 上游 error 帧（200 已开流之后报错，如 6004 限流 / 内容拦截 / 审核）：
	// 按 Anthropic 规范发一个 error 事件即终止，**不吞**。此前这里只认 choices，
	// 于是 error 帧被静默丢弃 → 客户端拿到 message_start + end_turn + message_stop、
	// 零 content，看起来是一次"正常结束的空回复"，上游原文与原因全部丢失。
	if msg, ok := upstreamErrorFrame(payload); ok {
		s.send("error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": msg},
		})
		s.finished = true // error 事件即流终止，不再补 message_delta / message_stop
		return true
	}
	var ch struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &ch); err != nil {
		return false
	}
	if ch.Usage != nil {
		s.usage = map[string]any{
			"input_tokens":  ch.Usage.PromptTokens,
			"output_tokens": ch.Usage.CompletionTokens,
		}
	}
	for _, c := range ch.Choices {
		s.Delta(c.Delta.Content)
		for _, tc := range c.Delta.ToolCalls {
			s.ToolCallDelta(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
		if c.FinishReason != "" {
			s.Finish(c.FinishReason)
			return true
		}
	}
	return false
}
