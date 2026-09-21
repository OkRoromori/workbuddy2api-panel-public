package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 出站：Chat Completions 响应 → Responses 响应
// ---------------------------------------------------------------------------

// respID 由 chat 的 id 派生一个 Responses 风格的 id。
// 前缀只是外观（resp_ / msg_ / fc_），Codex 用它做串联，不校验格式。
func respID(prefix, seed string) string {
	if seed == "" {
		seed = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return prefix + "_" + seed
}

// chatResponseToResponses 把**非流式** Chat 响应翻成 Responses 响应。
//
// 结构差异：
//   - Chat 把内容放在 choices[0].message.content；Responses 放在 output[] 的 item 里
//   - Chat 的 tool_calls 是 message 的字段；Responses 要求它是**独立的 output item**
//   - usage 键名不同：prompt_tokens/completion_tokens → input_tokens/output_tokens
func chatResponseToResponses(chat []byte, model string) ([]byte, error) {
	var c struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
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
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(chat, &c); err != nil {
		return nil, fmt.Errorf("上游响应不是合法 chat 结构: %w", err)
	}
	if model == "" {
		model = c.Model
	}

	var output []any
	for _, ch := range c.Choices {
		// item 顺序：**message 在前、function_call 在后**。
		//
		// 为什么必须是这个顺序：客户端会把拿到的 item 原样回放进下一轮 input，
		// 我们的翻译层再把它们映射回 chat messages。若 function_call 排在前面，
		// 回放后就是「assistant(tool_calls) → assistant(正文) → tool(结果)」——
		// 工具结果被一条 assistant 隔断，正是上游 deepseek 系判 11148
		// （tool_call_sequence_broken）的形态，而且出站三步管线都修不了
		// （merge 只并「后一条为空正文」、repack 只搬结果之后的插入消息）。
		// message 在前则回放为「assistant(正文) → assistant(tool_calls) → tool」，
		// 工具调用紧跟自己的结果，合法。
		//
		// 这也与 OpenAI 自己的 Responses 输出一致（先说话、再决定调工具）。
		// 注：tool_pairing.foldTextIntoPrevToolCall 另有出站兜底，覆盖客户端
		// 仍按旧序回放的历史数据。
		if ch.Message.Content != "" || len(ch.Message.ToolCalls) == 0 {
			output = append(output, map[string]any{
				"id":     respID("msg", c.ID),
				"type":   "message",
				"status": "completed",
				"role":   "assistant",
				"content": []any{map[string]any{
					"type": "output_text", "text": ch.Message.Content, "annotations": []any{},
				}},
			})
		}
		for _, tc := range ch.Message.ToolCalls {
			output = append(output, map[string]any{
				"id":        respID("fc", tc.ID),
				"type":      "function_call",
				"status":    "completed",
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
				"call_id":   tc.ID,
			})
		}
	}
	if output == nil {
		output = []any{}
	}

	resp := map[string]any{
		"id":         respID("resp", c.ID),
		"object":     "response",
		"created_at": time.Now().Unix(),
		"status":     "completed",
		"model":      model,
		"output":     output,
		"usage": map[string]any{
			"input_tokens":  c.Usage.PromptTokens,
			"output_tokens": c.Usage.CompletionTokens,
			"total_tokens":  c.Usage.TotalTokens,
		},
	}
	return json.Marshal(resp)
}

// ---------------------------------------------------------------------------
// 出站：Chat SSE → Responses SSE
// ---------------------------------------------------------------------------

// respStreamer 把上游的 Chat Completions SSE 逐块转写成 Responses 事件。
//
// 为什么要做成结构体而不是一个纯函数：Responses 的事件是**有状态的**——
// 必须先发 response.created，再发 output_item.added，然后才是若干 delta，
// 最后 done/completed。每一步都要记住已经开了哪些 item，所以状态必须抱着走。
type respStreamer struct {
	w       io.Writer
	model   string
	respID  string
	msgID   string
	started bool
	// output_index 按 item 首次出现的顺序分配。不能直接复用 Chat tool index：
	// 文本 message 也是一个 output item，会占掉一个位置。
	nextOutputIndex int
	msgOutputIndex  int
	text            strings.Builder
	// 已发出的 function_call item：index-key → item id（用于 arguments.delta）
	fcItems map[string]string
	// fcOutputIndexes / fcArguments 记录每个工具 item 的稳定位置与完整参数。
	fcOutputIndexes map[string]int
	fcArguments     map[string]string
	// fcCallIDs / fcNames 只在首片能取到，记下来供收尾的 done 事件用。
	fcCallIDs map[string]string
	fcNames   map[string]string
	// 已发出的 item 顺序（收尾时要按序补 done 事件）
	fcOrder []string
	sawText bool
	// finished 是否已经发过收尾事件（response.completed / response.failed）。
	finished bool
	// failed / failMsg：上游以 error 帧报错（200 已开流后的 6004 限流、内容拦截等）。
	// 收尾时改发 response.failed（status=failed + error 对象），不再谎报 completed。
	failed  bool
	failMsg string
	usage   map[string]any
	flush   func()
}

func newRespStreamer(w io.Writer, model string) *respStreamer {
	return &respStreamer{
		w: w, model: model,
		respID:          respID("resp", ""),
		msgID:           respID("msg", ""),
		msgOutputIndex:  -1,
		fcItems:         map[string]string{},
		fcOutputIndexes: map[string]int{},
		fcArguments:     map[string]string{},
		fcCallIDs:       map[string]string{},
		fcNames:         map[string]string{},
	}
}

// send 写一条 SSE 事件。Responses 流里**只有 data 行**（没有 event: 行），
// 事件类型写在 JSON 的 type 字段里——这点和 Chat/Anthropic 都不同。
func (s *respStreamer) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	if s.flush != nil {
		s.flush()
	}
}

// Start 发 response.created（流的第一条事件）。
func (s *respStreamer) Start() {
	if s.started {
		return
	}
	s.started = true
	s.send(map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id": s.respID, "object": "response", "status": "in_progress",
			"model": s.model, "output": []any{},
		},
	})
}

// ensureMessage 惰性打开 message item——只有真的要吐文字时才开，
// 免得纯工具调用的一轮里多出一个空的 message item（Codex 会当成空回复）。
func (s *respStreamer) ensureMessage() {
	if s.sawText {
		return
	}
	s.sawText = true
	s.msgOutputIndex = s.nextOutputIndex
	s.nextOutputIndex++
	s.send(map[string]any{
		"type": "response.output_item.added", "output_index": s.msgOutputIndex,
		"item": map[string]any{
			"id": s.msgID, "type": "message", "status": "in_progress",
			"role": "assistant", "content": []any{},
		},
	})
	s.send(map[string]any{
		"type": "response.content_part.added", "item_id": s.msgID,
		"output_index": s.msgOutputIndex, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

// Delta 处理一段文本增量。
func (s *respStreamer) Delta(text string) {
	if text == "" {
		return
	}
	s.ensureMessage()
	s.text.WriteString(text)
	s.send(map[string]any{
		"type": "response.output_text.delta", "item_id": s.msgID,
		"output_index": s.msgOutputIndex, "content_index": 0, "delta": text,
	})
}

// ToolCallDelta 处理工具调用增量。
//
// Chat 的 tool_calls 按 index 分片：**只有首片带 id/name**，后续片只带 arguments
// 增量。所以归并的键必须是 **index**——用 call_id 归并的话，第二片起 call_id 为空，
// 每片都会被当成一个新工具调用（实测两片就建出 2 个 item，Codex 拼不出参数）。
// call_id 只在首片取到，单独记下来供收尾事件用。
func (s *respStreamer) ToolCallDelta(idx int, callID, name, argsDelta string) {
	key := fmt.Sprintf("#%d", idx)
	itemID, ok := s.fcItems[key]
	if !ok {
		itemID = respID("fc", callID)
		s.fcItems[key] = itemID
		s.fcOutputIndexes[key] = s.nextOutputIndex
		s.nextOutputIndex++
		s.fcCallIDs[key] = callID
		s.fcNames[key] = name
		s.fcOrder = append(s.fcOrder, key)
		s.send(map[string]any{
			"type": "response.output_item.added", "output_index": s.fcOutputIndexes[key],
			"item": map[string]any{
				"id": itemID, "type": "function_call", "status": "in_progress",
				"name": name, "arguments": "", "call_id": callID,
			},
		})
	}
	if argsDelta != "" {
		s.fcArguments[key] += argsDelta
		s.send(map[string]any{
			"type":    "response.function_call_arguments.delta",
			"item_id": itemID, "output_index": s.fcOutputIndexes[key], "delta": argsDelta,
		})
	}
}

// Finish 收尾：把打开的 item 逐个 done，再发 response.completed。
// 幂等：重复调用只生效一次（上游可能既给 finish_reason 又给 [DONE]）。
func (s *respStreamer) Finish(finishReason string) {
	if s.finished {
		return
	}
	s.finished = true
	if s.failed {
		// 上游以 error 帧报错：发 response.failed（规范里的事件）并带上 error 对象。
		// 不再补 item done / response.completed —— 把失败谎报成"成功但空"，
		// 是这类事故里最难查的一种形态。
		s.send(map[string]any{
			"type": "response.failed",
			"response": map[string]any{
				"id": s.respID, "object": "response", "status": "failed",
				"model":  s.model,
				"output": []any{},
				"error":  map[string]any{"code": "upstream_error", "message": s.failMsg},
			},
		})
		return
	}
	output := make([]any, s.nextOutputIndex)
	if s.sawText {
		text := s.text.String()
		s.send(map[string]any{
			"type": "response.output_text.done", "item_id": s.msgID,
			"output_index": s.msgOutputIndex, "content_index": 0, "text": text,
		})
		s.send(map[string]any{
			"type": "response.content_part.done", "item_id": s.msgID,
			"output_index": s.msgOutputIndex, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		})
		item := map[string]any{
			"id": s.msgID, "type": "message", "status": "completed",
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "output_text", "text": text, "annotations": []any{},
			}},
		}
		s.send(map[string]any{
			"type": "response.output_item.done", "output_index": s.msgOutputIndex,
			"item": item,
		})
		output[s.msgOutputIndex] = item
	}
	for _, key := range s.fcOrder {
		idx := s.fcOutputIndexes[key]
		args := s.fcArguments[key]
		s.send(map[string]any{
			"type":    "response.function_call_arguments.done",
			"item_id": s.fcItems[key], "output_index": idx, "arguments": args,
		})
		item := map[string]any{
			"id": s.fcItems[key], "type": "function_call",
			"status": "completed", "name": s.fcNames[key],
			"call_id": s.fcCallIDs[key], "arguments": args,
		}
		s.send(map[string]any{
			"type": "response.output_item.done", "output_index": idx,
			"item": item,
		})
		output[idx] = item
	}
	usage := s.usage
	if usage == nil {
		usage = map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	}
	status := "completed"
	if finishReason == "length" {
		status = "incomplete"
	}
	s.send(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": s.respID, "object": "response", "status": status,
			"model": s.model, "output": output, "usage": usage,
		},
	})
}

// Frame 处理一个 chat SSE 的 data payload（不含 "data:" 前缀，不含换行）。
// 返回 true 表示流已收尾（收到 finish_reason 或 [DONE]）。
//
// 为什么按"帧"而不是按 io.Reader 设计：上游透传用的 upstream.Stream 是
// **一次 Write 写一整帧**并立刻 flush 的。写成帧接口后，包一层 ResponseWriter
// 就能直接复用，既保住了真流式（不缓冲），也不用把 chat 的旋转/超时/粘性
// 那一整套逻辑复制一遍。
func (s *respStreamer) Frame(payload string) bool {
	payload = strings.TrimSpace(payload)
	// 容错：调用方可能递整行（"data: {...}"）也可能递纯 payload。
	// 线上踩过——包 ResponseWriter 时 upstream.Stream 每次 Write 写的是**整帧**
	// 含 "data: " 前缀，而这里按纯 payload 解析，于是每一帧 JSON 解析都失败、
	// 被静默丢弃，客户端只收到 created + completed、一个字的正文都没有。
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
	// 上游 error 帧（200 已开流之后报错）：记下来，交给 Finish 发 response.failed。
	// 此前这里只认 choices → error 帧被静默丢弃，客户端拿到 created + completed、
	// output 为空、usage 全 0，看起来是一次"成功但什么都没说"的回复。
	if msg, ok := upstreamErrorFrame(payload); ok {
		s.failed = true
		s.failMsg = msg
		s.Finish("")
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
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &ch); err != nil {
		return false // 不认识的分片跳过，不打断整条流
	}
	if ch.Usage != nil {
		s.usage = map[string]any{
			"input_tokens":  ch.Usage.PromptTokens,
			"output_tokens": ch.Usage.CompletionTokens,
			"total_tokens":  ch.Usage.TotalTokens,
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

// streamChatToResponses 读上游 Chat SSE，逐块转写成 Responses 事件写出去。
func streamChatToResponses(r io.Reader, w io.Writer, model string) error {
	flusher, _ := w.(interface{ Flush() })
	rs := newRespStreamer(w, model)
	if flusher != nil {
		rs.flush = flusher.Flush
	}
	rs.Start()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // 单条 SSE 可能很大（工具参数）
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if rs.Frame(strings.TrimPrefix(line, "data:")) {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	rs.Finish("stop") // 上游没给 finish_reason 就断了，也补一个收尾
	return nil
}
