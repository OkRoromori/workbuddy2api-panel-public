// deadaudit.go 死号自检：定期探测被禁用的账号是否真的死了，能 refresh 成功就自动复活。
//
// 背景：disabled 会持久化到 state.json 且只能人工复活，而 12153（session dead）
// 会被网络抖动 / 上游闪断 / refresh 竞态**临时**触发——历史上多个 disabled 号
// 全部 refresh 成功，属于单次误判的受害者。
//
// 本自检给误判号一条自动恢复路径：refresh 成功 = session 没死 = 之前的 12153 判错了。
package scheduler

import (
	"log"
	"time"
)

// RunDeadAuditNow 对池内所有 disabled 账号尝试一次 token 刷新：成功即自动复活。
//
// 只清 disabled 与连续 12153 计数（Pool.ReviveDisabled），**不动**冷却/熔断——
// refresh 成功只能证明 session 活着，不能证明限流或积分状态已恢复，
// 那些交给各自的到期机制，避免一次自检把真实的限流保护也抹掉。
//
// 无刷新令牌（RefreshToken 为空）的号跳过：无法证明其存活，保持禁用更安全
// （这类号通常真的需要重新登录，面板「添加账号」重新登录会自动 Revive）。
func (s *Scheduler) RunDeadAuditNow() {
	if !s.beginRun("死号自检") {
		return
	}
	defer s.endRun("死号自检")
	start := time.Now()
	total, checked, revived := 0, 0, 0

	for _, st := range s.cfg.Pool.List() {
		if !st.Disabled {
			continue
		}
		total++
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || !a.HasRefreshToken() {
			log.Printf("dead-audit %s: 跳过（无 refresh token，需重新登录）", st.UID)
			continue
		}
		checked++
		if err := s.cfg.Upstream.RefreshToken(a); err != nil {
			log.Printf("dead-audit %s: 仍不可用（%v）", st.UID, err)
			continue
		}
		// 落盘失败**不阻止**复活：refresh 成功已经证明 session 活着，这才是复活的依据。
		// 落盘是另一回事（进程重启会回退到旧 token，可由下次保活再修），单独告警即可——
		// 若因落盘失败就不复活，日志会出现「refresh 成功但号还是灰的」，非常困惑。
		if err := a.SaveAtomic(); err != nil {
			log.Printf("dead-audit %s: 已复活，但 token 落盘失败（重启可能回退到旧 token）: %v", st.UID, err)
		}
		s.cfg.Pool.ReviveDisabled(st.UID)
		revived++
		log.Printf("dead-audit %s: refresh 成功 → 已自动复活（此前 12153 判定为误判）", st.UID)
	}

	dur := time.Since(start).Round(time.Second)
	switch {
	case total == 0:
		log.Printf("死号自检：池内无禁用账号，无需检查（耗时 %s）", dur)
	case revived > 0:
		// \a 响铃：有号被救回来值得提醒一下
		log.Printf("\a死号自检完成 ✅ 池内 %d 个禁用号（检查 %d / 自动复活 %d）耗时 %s",
			total, checked, revived, dur)
	default:
		log.Printf("死号自检完成：池内 %d 个禁用号（检查 %d）确实不可用，保持禁用（耗时 %s）",
			total, checked, dur)
	}
}
