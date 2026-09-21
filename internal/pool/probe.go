// probe.go 账号验活观测：记录每次验活结果，连续失败达阈值自动禁用。
//
// 与 NoteSessionDead 同一套哲学——**连续 N 次才判死**：
//
// 为什么不是"失败就禁"：验活打的是上游的真实推理接口，一次失败可能是三种完全不同的
// 原因——号真死了 / 上游抖了一下 / 探测自己撞上了限流。一次就杀号会把上游抖动变成
// 账号损失，而"晚两个小时发现坏号"的代价比"误杀一个好号"小得多。
//
// 成功只清计数、**不解除禁用**：解除是人工决定的（面板解冻 / 死号自检）。
// 让探测自己恢复会让"禁用—复活"来回抖，运维反而看不清到底怎么回事。
package pool

import (
	"fmt"
	"time"
)

// probeErrMax 验活失败原因存入状态时的最大长度。原文可能是整段上游 JSON，
// 全存进 state.json 会把文件撑大且没人看；截断保留最前面的关键信息。
const probeErrMax = 160

// defaultProbeThreshold 连续验活失败多少次才自动禁用（config 未注入时的兜底）。
const defaultProbeThreshold = 3

// NoteProbe 记录一次验活结果。ok=true 清零连续失败计数；ok=false 累计，
// 达到阈值时禁用账号并返回 true（调用方据此打"已自动禁用"日志）。
func (p *Pool) NoteProbe(uid string, ok bool, errMsg string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, found := p.byUID[uid]
	if !found {
		return false
	}
	e.probeAt = time.Now()
	e.probeOK = ok
	if ok {
		e.probeErr = ""
		e.probeFails = 0
		p.dirty.Store(true)
		return false
	}
	if len(errMsg) > probeErrMax {
		errMsg = errMsg[:probeErrMax]
	}
	e.probeErr = errMsg
	e.probeFails++
	// 已被禁用的号只更新观测值，不再重复判死（否则每轮都会"又禁用一次"）。
	if e.disabled {
		p.dirty.Store(true)
		return false
	}
	th := p.probeThreshold
	if th <= 0 {
		th = defaultProbeThreshold
	}
	if e.probeFails < th {
		p.dirty.Store(true)
		return false
	}
	e.disabled = true
	e.reason = fmt.Sprintf("验活连续 %d 次失败：%s", e.probeFails, errMsg)
	e.probeFails = 0
	p.dirty.Store(true)
	return true
}

// ProbeThreshold 连续验活失败判死的阈值（面板展示「距离判死还差几次」用）。
func (p *Pool) ProbeThreshold() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.probeThreshold <= 0 {
		return defaultProbeThreshold
	}
	return p.probeThreshold
}

// SetProbeThreshold 注入验活判死阈值（面板配置）。n<=0 回落默认值。
func (p *Pool) SetProbeThreshold(n int) {
	if n <= 0 {
		n = defaultProbeThreshold
	}
	p.mu.Lock()
	p.probeThreshold = n
	p.mu.Unlock()
}
