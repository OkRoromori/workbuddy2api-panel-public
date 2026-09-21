package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// 本文件是**协议兼容层的公共骨架**：Responses（Codex）与 Anthropic Messages
// （Claude Code / ccswitch）走同一套接线。
//
// 共同做法：把入站请求翻译成 Chat Completions，交给既有的 chatCompletions 管线，
// 只把**写出去的响应**包一层转回目标协议。
//
// 为什么不各写一套选号/轮转逻辑：那套东西（会话粘性、在途租约、超时不换号、413、
// 审计、指标、提示词改写）有十几处细节，复制两份就等于埋两颗「日后只修了一边」的雷。
// 包 ResponseWriter 的成本是几十行，收益是**零重复**。

// frameTranslator 一种目标协议的**流式**翻译器。
//
// 按"帧"而不是 io.Reader 设计：上游透传的 upstream.Stream 是**一次 Write 写一整帧**
// 并立即 flush 的，按帧接口就能直接复用，既保住真流式（不缓冲），又能逐帧转写。
type frameTranslator interface {
	Start()
	Frame(payload string) bool // 返回 true 表示已收尾
	Finish(reason string)
	IsFinished() bool
}

// upstreamErrorFrame 上游 SSE error 帧的消息原文。
//
// 上游「200 已开流 + 一帧 error」是真实形态（6004 限流、内容拦截、审核），
// chat 直连路径会把它原样透传给客户端；翻译路径（Anthropic / Responses）此前
// 只认 choices，于是 error 帧被静默丢弃 → 客户端拿到一个**内容为空的"成功"回复**，
// 既没有内容也不知道为什么。两个翻译器都用它把上游原文如实交出去。
//
// 返回 ok=false 表示这不是 error 帧（正常数据帧）。
func upstreamErrorFrame(payload string) (string, bool) {
	var env struct {
		Error *struct {
			Message string `json:"message"`
			Msg     string `json:"msg"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(payload), &env) != nil || env.Error == nil {
		return "", false
	}
	if s := strings.TrimSpace(env.Error.Message); s != "" {
		return s, true
	}
	if s := strings.TrimSpace(env.Error.Msg); s != "" {
		return s, true
	}
	return "upstream error", true
}

// protocolWriter 把 chatCompletions 写出的 Chat 格式就地转成目标协议格式。
//
//   - 流式：逐帧翻译（不缓冲，保真流式）
//   - 非流式：writeJSON 一次写完整个 body，在 Write 里整体翻译
//   - **非 200 与翻译失败一律原样透传**：宁可让客户端看到 chat 形状的错误，
//     也不能把错误吞掉变成空响应——空响应比错误难查得多。
type protocolWriter struct {
	http.ResponseWriter
	stream    bool
	model     string
	newStream func(w io.Writer, model string) frameTranslator
	nonStream func(chat []byte, model string) ([]byte, error)
	st        frameTranslator
	buf       bytes.Buffer
	passthru  bool
}

func (w *protocolWriter) WriteHeader(code int) {
	if code != http.StatusOK {
		w.passthru = true // 错误体照原样给，别翻译
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *protocolWriter) Write(p []byte) (int, error) {
	if w.passthru {
		return w.ResponseWriter.Write(p)
	}
	if w.stream {
		if w.st == nil {
			w.st = w.newStream(w.ResponseWriter, w.model)
			if f, ok := w.ResponseWriter.(http.Flusher); ok {
				if fs, ok2 := w.st.(flushSetter); ok2 {
					fs.setFlush(f.Flush)
				}
			}
			w.st.Start()
		}
		w.st.Frame(strings.TrimSpace(string(p)))
		return len(p), nil
	}
	// 非流式：上游整体写完才到这里
	w.buf.Write(p)
	out, err := w.nonStream(w.buf.Bytes(), w.model)
	if err != nil {
		return w.ResponseWriter.Write(p) // 翻译失败 → 原样透传，不吞响应
	}
	_, werr := w.ResponseWriter.Write(out)
	return len(p), werr
}

// flushSetter 可选接口：翻译器想拿到 http.Flusher 就实现它。
type flushSetter interface{ setFlush(func()) }

// finish 流式收尾：确保客户端至少拿到一个完整的收尾事件。
//
// 为什么必须做：上游若在没给 finish_reason 的情况下断流，客户端会拿到
// 200 + 半截流。那是**协议错误**，客户端只会报一句语焉不详的解析失败，
// 比一个明确的错误难查得多。
func (w *protocolWriter) finish() {
	if !w.stream || w.passthru || w.st == nil {
		return
	}
	if !w.st.IsFinished() {
		w.st.Finish("stop")
	}
}

// serveTranslated 兼容层的公共入口：读体 → 翻译 → 走既有管线。
//
// 请求体无大小上限（max_body_mb 已移除，对齐 v1.11 的 chatCompletions 口径）：
// 完整读入，超限类问题交由上游自然返回错误（其响应经既有错误分类链路透出，
// 信息量更大）。读错误就地 400，不把半截 JSON 喂上游。
func (h *Handler) serveTranslated(
	w http.ResponseWriter, r *http.Request,
	toChat func([]byte) ([]byte, error),
	newStream func(io.Writer, string) frameTranslator,
	nonStream func([]byte, string) ([]byte, error),
) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}

	chatBody, err := toChat(raw)
	if err != nil {
		// 翻译失败是**客户端请求问题**：不打上游（换账号也没用）、不罚账号，
		// 本地就说清楚，比让上游回一个 400 更有信息量。
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	var peek struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	_ = json.Unmarshal(chatBody, &peek)

	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(chatBody))
	r2.ContentLength = int64(len(chatBody))

	pw := &protocolWriter{
		ResponseWriter: w, stream: peek.Stream, model: peek.Model,
		newStream: newStream, nonStream: nonStream,
	}
	h.chatCompletions(pw, r2)
	pw.finish()
}

// respStreamAdapter 让 *respStreamer 满足 frameTranslator。
type respStreamAdapter struct {
	s   *respStreamer
	out io.Writer
	mdl string
}

func (a *respStreamAdapter) Start()             { a.s.Start() }
func (a *respStreamAdapter) Frame(p string) bool { return a.s.Frame(p) }
func (a *respStreamAdapter) Finish(r string)    { a.s.Finish(r) }
func (a *respStreamAdapter) IsFinished() bool   { return a.s.finished }
func (a *respStreamAdapter) setFlush(f func())  { a.s.flush = f }

// anthStreamAdapter 让 *anthropicStreamer 满足 frameTranslator。
type anthStreamAdapter struct {
	s   *anthropicStreamer
	out io.Writer
	mdl string
}

func (a *anthStreamAdapter) Start()              { a.s.Start() }
func (a *anthStreamAdapter) Frame(p string) bool { return a.s.Frame(p) }
func (a *anthStreamAdapter) Finish(r string)     { a.s.Finish(r) }
func (a *anthStreamAdapter) IsFinished() bool    { return a.s.finished }
func (a *anthStreamAdapter) setFlush(f func())   { a.s.flush = f }

// responses `POST /v1/responses`（OpenAI Responses API，Codex 用）。
func (h *Handler) responses(w http.ResponseWriter, r *http.Request) {
	h.serveTranslated(w, r, responsesToChat,
		func(out io.Writer, model string) frameTranslator {
			return &respStreamAdapter{s: newRespStreamer(out, model)}
		},
		chatResponseToResponses)
}

// messages `POST /v1/messages`（Anthropic Messages API，Claude Code / ccswitch 用）。
func (h *Handler) messages(w http.ResponseWriter, r *http.Request) {
	h.serveTranslated(w, r, anthropicToChat,
		func(out io.Writer, model string) frameTranslator {
			return &anthStreamAdapter{s: newAnthropicStreamer(out, model)}
		},
		chatResponseToAnthropic)
}
