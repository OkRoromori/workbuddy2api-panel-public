// probe.go 账号验活：定时向单个账号发一次**极小的真实推理请求**，用"能不能真的
// 完成一次对话"来判断这个号是不是还活着。
//
// 与既有机制的分工（这是它存在的理由）：
//   - token 保活 / 死号自检：都只看 refresh 成不成功，验证的是 **token 层**；
//   - 夜猫子：确实发真实对话，但只在 23:00–08:00 窗口跑，且目的是补任务次数；
//   - 验活：任何时刻都能跑，验证的是**端到端**——会话有效 + 模型有权限 + 上游愿意
//     为这个号出结果。token 有效但模型被收回权限，只有它能发现。
//
// 关键实现约束：**直连上游，不经过网关**。走网关会注入网关那套自定义系统提示词
// （几百 token），每次探测都白花积分；直连只发一个 "hi"。用 hy3 这类免费档模型时，
// 验活的积分成本是 0。
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/auth"
)

// probeMaxDrain 单次验活最多读取的响应字节数。
//
// 验活只需要证明"这次请求能通"，不需要完整回答。但也不能读完第一帧就掐断——
// 提前断开会让上游那边记成一次未完成的请求，反复如此可能影响账号画像。
// 1MB 对 max_tokens 很小的探测来说远超整段回答，正常都会自然读完。
const probeMaxDrain = 1 << 20

// ProbeChat 向该账号发一次真实验活请求。返回 nil 表示**通了**。
//
// model 为空回落到 "hy3"（上游的免费档）；prompt 为空回落到 "hi"。
// 调用方负责决定失败要不要处置——本函数只报告事实。
func (c *Client) ProbeChat(a *auth.Auth, model, prompt string) error {
	if model == "" {
		model = "hy3"
	}
	if prompt == "" {
		prompt = "hi"
	}
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]any{{"role": "user", "content": prompt}},
		"stream":     true,
		"max_tokens": 16,
	})
	if err != nil {
		return fmt.Errorf("构造验活请求失败: %w", err)
	}
	rc, status, respBody, err := c.ChatStreamContext(context.Background(), a, body, "", ChatMeta{})
	if rc != nil {
		defer rc.Close()
	}
	if err != nil {
		return fmt.Errorf("验活请求失败: %w", err)
	}
	if status >= 400 {
		// 把上游原文带出去：连续失败自动禁用时，reason 里要有可查的原因。
		return fmt.Errorf("验活 HTTP %d: %.200s", status, string(respBody))
	}
	// 读干流（有界）。读取过程中的错误也算失败——半路断流说明这次请求没走完。
	if _, err := io.Copy(io.Discard, io.LimitReader(rc, probeMaxDrain)); err != nil {
		return fmt.Errorf("验活流中断: %w", err)
	}
	return nil
}
