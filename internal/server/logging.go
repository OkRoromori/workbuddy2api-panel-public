// logging.go 请求级表格日志：每个 /v1/chat/completions 请求结束后打印一行到 stdout。
//
// 本文件是 v1.11（远端）与本地功能线的**融合版**，两层职责并存：
//
//	v1.11 口径：chatStatsReader 抓 usage 供 recordAttempt（pool 每账号累计 + usage 记录器）；
//	本地口径：chatStat.done() 出口喂 metrics（面板仪表盘）+ 投递 audit 流水（使用日志页）。
//
// 两者数据源同一次解析、互不干扰：前者按「账号尝试」计、后者按「客户端请求」计。
package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/audit"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/logfmt"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/metrics"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/pool"
)

// chatSeq 进程级请求序号。
var chatSeq atomic.Int64

// chatLogEnabled 聊天表格日志总开关。生产恒 true；
// 测试包经 TestMain 置 false 关闭 stdout 噪音，需要断言行输出的测试用 withChatLog 临时开启（R5）。
var chatLogEnabled = true

// chatLogOut 聊天表格日志的输出目标。生产默认 os.Stdout；main 在启用管理面板时
// 经 SetChatLogOutput 注入 MultiWriter，把每行镜像进 /panel/api/logs 的环形缓冲，
// stdout 行为不变。需在开始服务前调用一次（无并发竞争窗口）。
var chatLogOut io.Writer = os.Stdout

// SetChatLogOutput 替换聊天表格日志输出目标（仅 main 启动期调用一次）。
func SetChatLogOutput(w io.Writer) { chatLogOut = w }

// Usage 一次请求的 token 用量。OK=false 表示上游没给 usage（截断/异常），
// 此时其余字段无意义，调用方不得采信。
type Usage struct {
	Prompt     int
	Completion int
	Cached     int // prompt_tokens_details.cached_tokens：命中前缀缓存、不计费的部分
	// Reasoning 推理（思维链）token，上游 usage.completion_thinking_tokens。
	// 它是 completion_tokens 的一部分，单列出来是因为思考型模型里它可能占输出的大头：
	// 并进「输出」会让人以为产出很多，其实都花在用户看不到的思考上。
	Reasoning int
	// Credit 上游 usage.credit：本次**实际扣掉的积分**，是浮点（实测可为 0.01）。
	// 这是上游算好的真数，不是拿 token × 倍率估算出来的——面板显示"本次消耗"
	// 一律用它，估不出就不显示。
	Credit float64
	OK     bool
}

// logTokens 返回表格日志里要显示的 token 数；usage 缺失时返回 -1（显示 "-"）。
func (u Usage) logTokens() int {
	if !u.OK {
		return -1
	}
	return u.Completion
}

// delta 把富 Usage 转成 pool 口径的增量（仅保留明确存在的字段）。
func (u Usage) delta() pool.TokenUsageDelta {
	if !u.OK {
		return pool.TokenUsageDelta{}
	}
	total := u.Prompt + u.Completion
	return pool.TokenUsageDelta{
		HasPromptTokens:     true,
		PromptTokens:        int64(u.Prompt),
		HasCompletionTokens: true,
		CompletionTokens:    int64(u.Completion),
		HasTotalTokens:      true,
		TotalTokens:         int64(total),
	}
}

// chatStat 单个 chat 请求的日志统计；handler 挂 defer，请求出口后落一行。
type chatStat struct {
	start  time.Time
	model  string
	realm  string // cn | global：该请求实际落地的域（分域统计用；未落号时为空）
	mode   string // "stream" | "sync"
	uid    string // 完整 uid，展示时只取前 8 位
	nick   string // 账号昵称（随选号同步），流水行经 logfmt.Label 拼成 "昵称(uid8)"
	user   string // 审计用户名（来自命中的密钥）；空 = 未启用鉴权
	keyID  string // 密钥指纹（httpauth.Fingerprint），密钥页按它统计用量
	client string // 客户端 User-Agent（已截断），仅供排查
	ttfb   time.Duration
	usage  Usage
	toks   int // <0 表示 usage 缺失 → 显示 "-"
	status int

	// reg 指标聚合器（可为 nil：测试/未启用面板时不做统计）。
	reg *metrics.Registry
	// rec 审计流水写入器（可为 nil）。请求出口投递一条明细，非阻塞。
	rec *audit.Recorder

	logged bool
}

// newChatStat 以请求进入 handler 的时刻为起点构造统计对象；toks 默认 -1（usage 缺失）。
func newChatStat(now time.Time, body []byte, stream bool) *chatStat {
	mode := "sync"
	if stream {
		mode = "stream"
	}
	return &chatStat{start: now, model: parseModelFromBody(body), mode: mode, toks: -1}
}

// withIdentity 把请求身份/客户端 UA/指标与审计出口一次性挂上（v1.11 融合接线）。
func (s *chatStat) withIdentity(id identity, client string, reg *metrics.Registry, rec *audit.Recorder) *chatStat {
	s.user, s.keyID, s.client = id.Name, id.KeyID, client
	s.reg, s.rec = reg, rec
	return s
}

// clientUA 取客户端 User-Agent（审计字段，排查"是哪个客户端在报错"用）。
func clientUA(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.UserAgent()
}

// done 幂等落一行表格日志，把用量喂给指标聚合器，并投递一条审计流水。
func (s *chatStat) done() {
	if s.logged {
		return
	}
	s.logged = true
	total := time.Since(s.start)
	s.observe(total)
	logChatRow(s.ttfb, total, s.model, s.mode, s.uid, s.nick, s.status, s.toks)
	s.auditRow(total)
}

// auditRow 投递一条审计流水。rec 为 nil 时为空操作。
//
// 字段口径与表格日志刻意保持不同：表格日志每行只有一列 token（为了对齐好读），
// 审计要的是完整用量——输入 token 与缓存 token 才是这里真正的大头（缓存命中
// 通常占输入 9 成以上），只记输出等于把成本看丢了。
func (s *chatStat) auditRow(total time.Duration) {
	if s.rec == nil {
		return
	}
	rec := audit.Record{
		TS:       s.start,
		User:     s.user,
		KeyID:    s.keyID,
		Model:    s.model,
		Account:  uidPrefix(s.uid),
		Status:   s.status,
		Mode:     s.mode,
		HasUsage: s.usage.OK,
		TTFBMs:   s.ttfb.Milliseconds(),
		TotalMs:  total.Milliseconds(),
		Client:   s.client,
	}
	if s.usage.OK {
		rec.Prompt, rec.Completion, rec.Cached = s.usage.Prompt, s.usage.Completion, s.usage.Cached
		rec.Reasoning = s.usage.Reasoning
		rec.Credit = s.usage.Credit
	}
	s.rec.Write(rec)
}

// observe 把本次请求的用量喂给指标聚合器。reg 为 nil 时为空操作。
//
// ok 只表示「客户端拿到了 200」——失败请求本就没有 usage，不该被算作
// 「成功的请求缺 usage」（后者才是上游异常信号，见 metrics.MissingUsage）。
//
// 注意：**不**在这里调 pool 的 token 累计——那条线由 v1.11 的 recordAttempt
// （按账号尝试）单独负责，两处都写会把 token 双倍计入账号。
func (s *chatStat) observe(total time.Duration) {
	if s.reg == nil {
		return
	}
	ok := s.status == http.StatusOK
	prompt, completion, cached := -1, -1, 0
	if s.usage.OK {
		prompt, completion, cached = s.usage.Prompt, s.usage.Completion, s.usage.Cached
	}
	credit := 0.0
	if s.usage.OK {
		credit = s.usage.Credit
	}
	s.reg.Observe(s.model, s.realm, s.uid, s.keyID, ok, prompt, completion, cached, s.ttfb, total, credit)
}

// chatStatsReader 在流式透传时抓取 SSE 末帧的 usage（prompt/completion/cached），
// 并记录首个 data 帧的 TTFB；原始字节原样返回给下游透传。
// 注意：不做 rune 估算，token 数一律采信上游 usage。
type chatStatsReader struct {
	br    *bufio.Reader
	start time.Time
	ttfb  time.Duration
	seen  bool // 已见过首个 data 帧（TTFB 只记一次）
	usage Usage
	// token 字段的存在性标记（区分「上游没给」与「上游给了 0」）。
	hasPrompt     bool
	hasCompletion bool
	hasTotal      bool
	hasCredit     bool
	totalTokens   int
	pend          []byte // 已读未返回的行缓存
}

// newChatStatsReaderSince 以 since 为 TTFB 计时起点（通常是请求进入 handler 的时刻）。
func newChatStatsReaderSince(r io.Reader, since time.Time) *chatStatsReader {
	return &chatStatsReader{br: bufio.NewReaderSize(r, 64*1024), start: since}
}

// TTFB 返回首个 data 帧到达耗时；无帧时为 0。
func (s *chatStatsReader) TTFB() time.Duration { return s.ttfb }

// Tokens 返回末帧 usage.completion_tokens 与是否缺失；无 usage 时 ok=false。
func (s *chatStatsReader) Tokens() (int, bool) { return s.usage.Completion, s.hasCompletion }

// Credit 返回末帧 usage.credit（本次真实扣费积分）与是否缺失。
func (s *chatStatsReader) Credit() (float64, bool) { return s.usage.Credit, s.hasCredit }

// TotalTokens 返回末帧 usage.total_tokens 与是否缺失。
func (s *chatStatsReader) TotalTokens() (int, bool) { return s.totalTokens, s.hasTotal }

// FullUsage 返回末帧解析出的完整用量（prompt/completion/cached/reasoning/credit）。
func (s *chatStatsReader) FullUsage() Usage { return s.usage }

// Usage 返回流式响应中已收到的 token usage 字段（pool 口径，供 recordAttempt）。
func (s *chatStatsReader) Usage() pool.TokenUsageDelta {
	return pool.TokenUsageDelta{
		HasPromptTokens:     s.hasPrompt,
		PromptTokens:        int64(s.usage.Prompt),
		HasCompletionTokens: s.hasCompletion,
		CompletionTokens:    int64(s.usage.Completion),
		HasTotalTokens:      s.hasTotal,
		TotalTokens:         int64(s.totalTokens),
	}
}

// parseSSELine 解析一行 "data: {...}"：首帧记 TTFB，见 usage 即采信整份用量。
// 后到的 usage 覆盖先到的（上游通常只在末帧给，但重复给时以最后一帧为准）。
func (s *chatStatsReader) parseSSELine(line string) {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data: ") {
		return
	}
	payload := strings.TrimPrefix(line, "data: ")
	if payload == "[DONE]" {
		return
	}
	if !s.seen {
		s.seen = true
		s.ttfb = time.Since(s.start)
	}
	var chunk struct {
		Usage *struct {
			PromptTokens     *int     `json:"prompt_tokens"`
			CompletionTokens *int     `json:"completion_tokens"`
			TotalTokens      *int     `json:"total_tokens"`
			ThinkingTokens   *int     `json:"completion_thinking_tokens"`
			Credit           *float64 `json:"credit"`
			Details          *struct {
				CachedTokens    int `json:"cached_tokens"`
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(payload), &chunk) != nil || chunk.Usage == nil {
		return
	}
	u := s.usage
	u.OK = true
	if v := chunk.Usage.PromptTokens; v != nil {
		s.hasPrompt, u.Prompt = true, *v
	}
	if v := chunk.Usage.CompletionTokens; v != nil {
		s.hasCompletion, u.Completion = true, *v
	}
	if v := chunk.Usage.TotalTokens; v != nil {
		s.hasTotal, s.totalTokens = true, *v
	}
	if v := chunk.Usage.ThinkingTokens; v != nil {
		u.Reasoning = *v
	} else if d := chunk.Usage.Details; d != nil && d.ReasoningTokens > 0 {
		u.Reasoning = d.ReasoningTokens
	}
	if v := chunk.Usage.Credit; v != nil {
		s.hasCredit, u.Credit = true, *v
	}
	if d := chunk.Usage.Details; d != nil {
		u.Cached = d.CachedTokens
	}
	s.usage = u
}

// Read 返回原始数据，同时解析统计 TTFB/token。
func (s *chatStatsReader) Read(p []byte) (int, error) {
	if len(s.pend) > 0 {
		n := copy(p, s.pend)
		s.pend = s.pend[n:]
		return n, nil
	}
	line, err := s.br.ReadString('\n')
	if line != "" {
		s.parseSSELine(line)
		s.pend = []byte(line)
		n := copy(p, s.pend)
		s.pend = s.pend[n:]
		return n, nil
	}
	return 0, err
}

// rewriteModel 把 outbound chat body 的 model 字段替换为 bare（保留其余字段原样）。
// 仅当 bare != 原 model 时由 chatCompletions 调用；body 不可解析时原样返回（不二次错误化）。
func rewriteModel(body []byte, bare string) []byte {
	if len(body) == 0 || bare == "" {
		return body
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	if cur, ok := obj["model"].(string); !ok || cur == bare {
		return body
	}
	obj["model"] = bare
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// parseModelFromBody 从请求 JSON 取 model 字段，缺省标 "-"。
func parseModelFromBody(body []byte) string {
	var obj struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &obj); err != nil || obj.Model == "" {
		return "-"
	}
	return obj.Model
}

// usageDeltaFromResponse 从非流式聚合响应中提取明确存在的 token 字段（pool 口径）。
func usageDeltaFromResponse(resp map[string]any) pool.TokenUsageDelta {
	return usageFrom(resp).delta()
}

// usageFrom 从 Aggregate 返回的响应中提取完整 usage；无 usage 或缺 completion_tokens 时
// 返回 OK=false（视为上游未提供，不采信 0）。
func usageFrom(resp map[string]any) Usage {
	u, ok := resp["usage"].(map[string]any)
	if !ok {
		return Usage{}
	}
	num := func(k string) (int, bool) {
		switch v := u[k].(type) {
		case float64:
			return int(v), true
		case json.Number:
			i, err := v.Int64()
			return int(i), err == nil
		default:
			return 0, false
		}
	}
	comp, okc := num("completion_tokens")
	if !okc {
		return Usage{}
	}
	prompt, _ := num("prompt_tokens")
	out := Usage{Prompt: prompt, Completion: comp, OK: true}
	// credit 只在放行路径上取；Aggregate 出来的 map 里上游给了就是浮点。
	if c, ok := u["credit"].(float64); ok {
		out.Credit = c
	}
	// 推理 token：上游给在 usage.completion_thinking_tokens；缺了再退到
	// completion_tokens_details.reasoning_tokens（两个字段实测同时存在）。
	if rt, ok := num("completion_thinking_tokens"); ok {
		out.Reasoning = rt
	} else if d, ok := u["completion_tokens_details"].(map[string]any); ok {
		if rt, ok := d["reasoning_tokens"].(float64); ok {
			out.Reasoning = int(rt)
		}
	}
	if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
		if c, ok := d["cached_tokens"].(float64); ok {
			out.Cached = int(c)
		}
	}
	return out
}

// completionTokens 从 Aggregate 返回的响应中提取 usage.completion_tokens；缺失返回 -1。
func completionTokens(resp map[string]any) int {
	return usageFrom(resp).logTokens()
}

// uidPrefix 只显示 uid 前 8 位；空 uid 显示 "-"。
//
// 实现委托 logfmt.UID8，避免 "截 8 位" 的规则在 server 与 logfmt 两处各写一份而走样。
func uidPrefix(uid string) string {
	return logfmt.UID8(uid)
}

// 请求流水行的固定列宽（显示列宽，非字节）。取固定宽度而不是让内容自然长度撑开，
// 是为了让 stdout 里成百上千行能竖着扫——否则模型名长短不一、中文昵称按字节补空格
// 错位，根本没法用肉眼对齐着一列列看（这正是上一版 11 字节硬截断要解决的问题）。
const (
	// chatModelWidth 覆盖 realm 前缀 + 最长模型名："global:" (7) + "deepseek-v4.1-flash" (19) = 26。
	// 旧的 11 字节截断会把 "cn:deepseek-v4-flash" 切成 "cn:deepseek"，让人误以为是另一个模型。
	chatModelWidth = 26
	// chatAcctWidth 容纳 "昵称(uid8)"：中文昵称按 2 列/字算，5 字中文 + "(xxxxxxxx)" = 20 列。
	chatAcctWidth = 22
	chatTTFBWidth = 8
	chatTokWidth  = 6
	chatRateWidth = 11 // 形如 "183.6tok/s"
)

// logChatRow 打印一行请求级表格日志（输出 chatLogOut，无 log 时间戳前缀）。
//
// 参数：
//   - model：模型名（含 realm 前缀），超 chatModelWidth 截断（模型名是 ASCII，字节截即列宽）；
//   - uid/nick：完整 uid 与账号昵称，经 logfmt.Label 拼成 "昵称(uid8)" 展示——只有
//     uid8 时人眼无法判断是哪个号，要辨认必须再查 auths/，排障多一跳；
//   - toks<0 表示 usage 缺失，显示 "-"。
func logChatRow(ttfb, total time.Duration, model, mode, uid, nick string, status int, toks int) {
	if !chatLogEnabled {
		return
	}
	seq := chatSeq.Add(1)
	model = logfmt.Pad(logfmt.Truncate(model, chatModelWidth), chatModelWidth)
	// 账号标签只补不截：超宽时宁可让该行变宽，也不丢昵称信息（昵称是排查的主线索）。
	acct := logfmt.Pad(logfmt.Label(uid, nick), chatAcctWidth)
	tokField := "-"
	tokpsField := "-"
	if toks >= 0 {
		tokField = fmt.Sprintf("%d", toks)
		if total > 0 {
			tokpsField = fmt.Sprintf("%.1ftok/s", float64(toks)/total.Seconds())
		} else {
			tokpsField = "0.0tok/s"
		}
	}
	ttfbMS := "-"
	if ttfb > 0 {
		ttfbMS = fmt.Sprintf("%dms", ttfb.Milliseconds())
	}
	fmt.Fprintf(chatLogOut, "| #%03d | %s | %s | %s | %d | %s | TTFB=%s | tok=%s | %s | total=%.1fs |\n",
		seq,
		time.Now().Format("15:04:05"),
		model,
		mode,
		status,
		acct,
		logfmt.Pad(ttfbMS, chatTTFBWidth),
		logfmt.Pad(tokField, chatTokWidth),
		logfmt.Pad(tokpsField, chatRateWidth),
		total.Seconds(),
	)
}
