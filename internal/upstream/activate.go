// activate.go 国际服账号「一键激活」：补地区 → 注册激活 → 领 trial → 读余额。
//
// 为什么单独抽这一步：面板的「添加国际服」已经会在登录后自动跑这套链路，但**没有可见的
// 结果**——用户只看到积分是 0，不知道是"没激活"、"trial 没领到"还是"只是余额还没入账"。
// 这里把它做成可重跑的显式动作（面板账号页「激活」/「激活全部国际服」），逐步返回结果，
// 让"卡在哪一步"一目了然。
//
// 实测的**入账延迟**（2026-09-20，账号 1545595f）：
//
//	trial 调用 17:35:12  →  上游积分包 CreateTime 17:35:13
//
// 即领取是异步记账的，紧跟着读余额会读到 0（面板因此显示 0 积分，直到下一次余额刷新，
// 默认 60 分钟）。所以新领成功后若读到 0，这里等一小会儿重读一次——这一条是"登录后显示
// 0 积分"的直接修复。
package upstream

import (
	"fmt"
	"time"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/auth"
)

// ActivationStep 激活链路的一步结果（面板逐条展示）。
type ActivationStep struct {
	Name   string `json:"name"`              // 步骤名：注册激活 / trial 加油包 / 读取余额
	OK     bool   `json:"ok"`                // 该步是否达到预期
	Detail string `json:"detail,omitempty"`  // 人话说明（成功口径 / 失败原因）
}

// 领到积分后等待重读的间隔与次数：实测入账延迟在 1 秒量级，给 2 次机会（共约 3 秒），
// 再读不到就如实返回 0（不假装成功）。
const (
	activateGrantWait    = 1500 * time.Millisecond
	activateGrantRetries = 2
)

// ActivateGlobal 跑一遍国际服激活链路，返回逐步结果与最终余额。
//
// 语义：注册激活/trial 的失败**不阻断**后续步骤，也都不作为函数级错误返回（它们属于
// 「这个号当前能领到多少」的信息，面板要逐条显示）；只有"不是国际服账号"与"余额读不出来"
// 才返回 err——前者是调用方用错了号，后者让调用方知道余额数字不可信。
func (c *Client) ActivateGlobal(a *auth.Auth) (steps []ActivationStep, remain, total int64, err error) {
	if a == nil || a.Realm() != "global" {
		return nil, 0, 0, fmt.Errorf("activate: only global accounts")
	}

	// 1) 注册激活（幂等；region required 时自动补地区后重试）。
	if activated, aerr := c.GlobalCompleteRegistration(a); aerr != nil {
		steps = append(steps, ActivationStep{Name: "注册激活", OK: false, Detail: aerr.Error()})
	} else if activated {
		steps = append(steps, ActivationStep{Name: "注册激活", OK: true, Detail: "已激活"})
	}

	// 2) trial 加油包（幂等码 14051 = 已领过，非错误）。
	claimed := false
	if got, terr := c.ClaimTrial(a); terr != nil {
		steps = append(steps, ActivationStep{Name: "trial 加油包", OK: false, Detail: terr.Error()})
	} else if got {
		claimed = true
		steps = append(steps, ActivationStep{Name: "trial 加油包", OK: true, Detail: "新领成功"})
	} else {
		steps = append(steps, ActivationStep{Name: "trial 加油包", OK: true, Detail: "已领过（幂等）"})
	}

	// 3) 读余额；刚领到积分时可能还没入账，等一小会儿重读。
	remain, total, rerr := c.UserResource(a)
	for i := 0; claimed && rerr == nil && remain == 0 && i < activateGrantRetries; i++ {
		time.Sleep(activateGrantWait)
		remain, total, rerr = c.UserResource(a)
	}
	if rerr != nil {
		steps = append(steps, ActivationStep{Name: "读取余额", OK: false, Detail: rerr.Error()})
		return steps, 0, 0, fmt.Errorf("activate: read balance: %w", rerr)
	}
	detail := fmt.Sprintf("剩余 %d / 共 %d", remain, total)
	if claimed && remain > 0 {
		detail += "（含刚到的 trial 积分）"
	}
	steps = append(steps, ActivationStep{Name: "读取余额", OK: true, Detail: detail})
	return steps, remain, total, nil
}

// StepsSummary 把步骤结果压成一行（登录/激活的返回字段用）。
func StepsSummary(steps []ActivationStep) string {
	out := ""
	for _, s := range steps {
		seg := s.Name + ":"
		if s.Detail != "" {
			seg += s.Detail
		} else if s.OK {
			seg += "完成"
		} else {
			seg += "失败"
		}
		if out != "" {
			out += "；"
		}
		out += seg
	}
	return out
}
