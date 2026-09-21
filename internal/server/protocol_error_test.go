// protocol_error_test.go 覆盖"上游以 SSE error 帧报错"这一族形态（6004 限流、内容拦截、
// 审核）与超时判定。这一族此前全被静默吞掉：翻译路径只认 choices，error 帧被丢弃，
// 客户端拿到一个"成功的空回复"；账号那边则在流开始前就被记成功。
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

const errFrame6004 = `{"error":{"code":6004,"msg":"model rate limit, resets at 2026-09-21 00:00:00","message":"模型限流，将在 2026-09-21 00:00:00 重置"}}`

// Anthropic 流式：error 帧必须以 error 事件原样交给客户端，且不再补 message_stop。
func TestAnthropicStreamErrorFrameSurfaced(t *testing.T) {
	var buf strings.Builder
	s := newAnthropicStreamer(&buf, "glm-5.2")
	s.Start()
	if done := s.Frame(errFrame6004); !done {
		t.Fatal("error 帧应终止流（Frame 返回 true）")
	}
	out := buf.String()
	if !strings.Contains(out, "event: error") {
		t.Fatalf("没有 error 事件：%s", out)
	}
	if !strings.Contains(out, "模型限流") {
		t.Fatalf("上游原文没透出：%s", out)
	}
	if strings.Contains(out, "message_stop") || strings.Contains(out, "message_delta") {
		t.Fatalf("error 之后不该再补收尾事件：%s", out)
	}
	// 收尾钩子不得再补一次（IsFinished 已为 true）
	s.Finish("stop")
	if strings.Contains(buf.String(), "message_stop") {
		t.Fatalf("Finish 在 error 之后仍发了 message_stop：%s", buf.String())
	}
}

// Responses 流式：error 帧收尾为 response.failed，不得谎报 completed。
func TestResponsesStreamErrorFrameEmitsFailed(t *testing.T) {
	var buf strings.Builder
	s := newRespStreamer(&buf, "glm-5.2")
	s.Start()
	if done := s.Frame(errFrame6004); !done {
		t.Fatal("error 帧应终止流")
	}
	out := buf.String()
	if !strings.Contains(out, "response.failed") {
		t.Fatalf("没有 response.failed：%s", out)
	}
	if !strings.Contains(out, `"status":"failed"`) {
		t.Fatalf("status 不是 failed：%s", out)
	}
	if strings.Contains(out, "response.completed") {
		t.Fatalf("失败流不该发 response.completed：%s", out)
	}
	if !strings.Contains(out, "模型限流") {
		t.Fatalf("上游原文没透出：%s", out)
	}
}

// 正常数据帧不得被误判成 error 帧（回归守卫）。
func TestUpstreamErrorFrameOnlyMatchesErrorFrames(t *testing.T) {
	if _, ok := upstreamErrorFrame(`{"choices":[{"delta":{"content":"hi"}}]}`); ok {
		t.Fatal("正常数据帧被误判为 error 帧")
	}
	if msg, ok := upstreamErrorFrame(errFrame6004); !ok || !strings.Contains(msg, "模型限流") {
		t.Fatalf("error 帧未识别：msg=%q ok=%v", msg, ok)
	}
	// 只有 msg 字段（另一种上游形态）
	if msg, ok := upstreamErrorFrame(`{"error":{"msg":"boom"}}`); !ok || msg != "boom" {
		t.Fatalf("msg 形态未识别：msg=%q ok=%v", msg, ok)
	}
}

type timeoutErr struct{ timeout bool }

func (e timeoutErr) Error() string   { return "stub" }
func (e timeoutErr) Timeout() bool   { return e.timeout }
func (e timeoutErr) Temporary() bool { return e.timeout }

// 超时识别：换号注定白换，必须能认出来（HANDOFF 记着"超时不换号"，此前代码里没有）。
func TestIsUpstreamTimeout(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		clientGone bool
		want       bool
	}{
		{"net.Error 超时", timeoutErr{true}, false, true},
		{"net.Error 非超时（连接被拒）", timeoutErr{false}, false, false},
		{"显式 deadline", fmt.Errorf("read: %w", context.DeadlineExceeded), false, true},
		{"客户端断连（ctx 已取消）", context.Canceled, true, false},
		{"空闲看门狗掐流（客户端仍在）", context.Canceled, false, true},
		{"普通错误", errors.New("boom"), false, false},
		{"nil", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUpstreamTimeout(c.err, c.clientGone); got != c.want {
				t.Fatalf("isUpstreamTimeout(%v, clientGone=%v) = %v, want %v", c.err, c.clientGone, got, c.want)
			}
		})
	}

	// 保证 stub 真的实现了 net.Error（否则上面的用例会静默走不到 Timeout 分支）
	var ne net.Error = timeoutErr{true}
	if !ne.Timeout() {
		t.Fatal("stub 未实现 net.Error.Timeout")
	}
}

// Responses 非流式：message item 必须排在 function_call 之前，否则客户端回放会
// 形成「工具调用 → assistant 正文 → 工具结果」被隔断的 11148 形态。
func TestChatResponseToResponsesPutsMessageBeforeToolCalls(t *testing.T) {
	chat := []byte(`{"id":"c1","model":"glm-5.2","choices":[{"message":{"content":"先说一下","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{}}`)
	out, err := chatResponseToResponses(chat, "glm-5.2")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	iMsg := strings.Index(s, `"type":"message"`)
	iFc := strings.Index(s, `"type":"function_call"`)
	if iMsg < 0 || iFc < 0 {
		t.Fatalf("缺少 item：%s", s)
	}
	if iMsg > iFc {
		t.Fatalf("message 必须排在 function_call 之前（当前 msg@%d fc@%d）：%s", iMsg, iFc, s)
	}
}
