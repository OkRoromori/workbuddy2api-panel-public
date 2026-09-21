package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/upstream"
)

// TestResponsesParallelToolCallsMergedOutbound Responses 的并行工具调用经「翻译 + 出站管线」
// 后，必须是上游认可的形态：**一条** assistant 带全部 tool_calls，其结果紧跟其后。
//
// 背景（2026-09-20 线上定位）：Responses 把并行调用发成多条独立 function_call item，翻译层
// 为每条 item 生成一条独立 assistant 消息，出站即
//
//	assistant tool_calls=[call_00_a] | assistant tool_calls=[call_01_b] | tool | tool
//
// 上游 deepseek 系模型要求「声明 tool_calls 的 assistant 之后必须紧跟它自己的结果」，
// 违反即 400 code=11148（tool_call_sequence_broken）并顶死整条会话（换号无效，客户端重试
// 稳定复现）。线上对照实验（同一批调用、同一模型 deepseek-v4.1-flash）：
// 上述拆分形态 → 503/11148；合并成一条 assistant → 200。
//
// 本测试钉住修复后的端到端形态，防止翻译层或出站管线的改动把它重新拆开。
func TestResponsesParallelToolCallsMergedOutbound(t *testing.T) {
	// 形状与真实 Codex Desktop 报文一致：两条并行 function_call item + 两条结果。
	req := `{
		"model":"deepseek-v4.1-flash",
		"input":[
			{"type":"message","role":"user","content":"跑两个命令"},
			{"type":"function_call","call_id":"call_00_a","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call","call_id":"call_01_b","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call_output","call_id":"call_00_a","output":"/opt"},
			{"type":"function_call_output","call_id":"call_01_b","output":"a b c"}
		],
		"tools":[{"type":"function","name":"exec_command","description":"d","parameters":{"type":"object"}}]
	}`
	chatBody, err := responsesToChat([]byte(req))
	if err != nil {
		t.Fatalf("responsesToChat: %v", err)
	}
	out := upstream.PrepareBodyOptWithEfforts(chatBody, false, nil)

	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal 出站 body: %v", err)
	}
	messages, _ := obj["messages"].([]any)

	type shape struct {
		role string
		ids  []string
	}
	var got []shape
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		role, _ := msg["role"].(string)
		var ids []string
		if role == "tool" {
			id, _ := msg["tool_call_id"].(string)
			ids = append(ids, id)
		}
		if tcs, ok := msg["tool_calls"].([]any); ok {
			for _, tci := range tcs {
				tc, _ := tci.(map[string]any)
				id, _ := tc["id"].(string)
				ids = append(ids, id)
			}
		}
		got = append(got, shape{role: role, ids: ids})
	}

	// 期望：[user, assistant(call_00_a,call_01_b), tool(call_00_a), tool(call_01_b)]
	// system 由 instructions 注入，本用例未给 instructions，故无 system。
	want := []shape{
		{"user", nil},
		{"assistant", []string{"call_00_a", "call_01_b"}},
		{"tool", []string{"call_00_a"}},
		{"tool", []string{"call_01_b"}},
	}
	if len(got) != len(want) {
		t.Fatalf("出站消息数 = %d want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].role != want[i].role || strings.Join(got[i].ids, ",") != strings.Join(want[i].ids, ",") {
			t.Errorf("消息[%d] = %s%v want %s%v", i, got[i].role, got[i].ids, want[i].role, want[i].ids)
		}
	}
	// 反向兜底：任何一条带 tool_calls 的 assistant 之后紧跟的必须是 tool 结果（上游硬校验）。
	for i, m := range got {
		if m.role != "assistant" || len(m.ids) == 0 {
			continue
		}
		for j := i + 1; j < len(got) && j <= i+len(m.ids); j++ {
			if got[j].role != "tool" {
				t.Fatalf("assistant 消息[%d] 之后跟着 %s（上游判 11148）", i, got[j].role)
			}
		}
	}
}
