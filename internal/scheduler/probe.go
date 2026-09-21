// probe.go 验活排程：每隔 probe.interval 对池内每个可用账号发一次极小真实请求，
// 用「能不能真的完成一次推理」判断账号是否还活着；连续失败达阈值自动禁用。
//
// 与其余排程的形态差别：其它任务都是「到点跑一轮」，本任务是**固定间隔循环**
// （像余额刷新那样），因为「每隔 x 小时」不落在整点上，也没有"时点"的概念。
package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"
)

const (
	// probeFirstDelay 启动后第一次验活的等待时间。比余额刷新（5s）晚一点，
	// 错开启动瞬间的并发；也不等到满一个周期——重启后马上想知道哪些号还活着。
	probeFirstDelay = 45 * time.Second

	// probeAccountDelay 账号之间的间隔。验活是真实推理请求，同时打 11 个号
	// 既容易触发上游风控，也让失败原因变得不可分辨（分不清是自己打太快还是号真坏）。
	probeAccountDelay = 1200 * time.Millisecond
)

// RunProbeNow 立即对池内所有可用账号验活一遍（手动触发 / 首次启动）。
//
// 跳过三类账号：
//   - disabled：已经判死的号不再探（探了也不能翻身，只会刷屏）；
//   - 无 AccessToken：连凭证都没有，请求必失败，记一条失败毫无信息量；
//   - 在途已满：正被真实流量占满的号，验活去抢名额等于自己给自己添堵，
//     而且这时候它显然是"活着"的——没必要验。
func (s *Scheduler) RunProbeNow() {
	if !s.beginRun("验活") {
		return
	}
	defer s.endRun("验活")
	start := time.Now()
	total, processed, failed, disabled := 0, 0, 0, 0
	accounts := s.cfg.Pool.List()
	for i, st := range accounts {
		total++
		if st.Disabled {
			disabled++
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || !a.HasAccessToken() {
			continue
		}
		if !s.cfg.Pool.Acquire(st.UID) {
			continue
		}
		processed++
		err := s.cfg.Upstream.ProbeChat(a, s.probeModel(), s.probePrompt())
		s.cfg.Pool.Release(st.UID)
		// NoteProbe 返回 true 表示「本次因连续失败达阈值而完成了禁用」。
		if s.cfg.Pool.NoteProbe(st.UID, err == nil, errMsg(err)) {
			log.Printf("probe uid=%s: 连续 %d 次验活失败 — 已自动禁用（面板「解冻」可恢复）",
				st.UID, s.cfg.Pool.ProbeThreshold())
			failed++
			continue
		}
		if err != nil {
			failed++
			current, _ := s.cfg.Pool.Status(st.UID)
			log.Printf("probe uid=%s: 失败（第 %d 次连续）：%v", st.UID, current.ProbeFails, err)
		} else {
			log.Printf("probe uid=%s: 正常", st.UID)
		}
		if i < len(accounts)-1 {
			time.Sleep(probeAccountDelay)
		}
	}
	if disabled > 0 {
		log.Printf("probe: 跳过 %d 个已禁用账号", disabled)
	}
	logBatchDone("验活", total, processed, failed, start)
}

// ProbeOne 对单个账号验活一次（面板账号页行内「验活」按钮）。
//
// 与批量 RunProbeNow 共用同一套观测记账（pool.NoteProbe，连续失败达阈值照常自动禁用）
// 与同一套模型/提示词（probeModel/probePrompt），所以单点结论与批量结论永远一致——
// 不会出现"批量说这个号坏了、单点说它好着"这种自相矛盾。
//
// 与批量的两处差异都是"手动点一次"的语境决定的：
//   - 不跳已禁用账号：手动点就是想确认它到底还行不行（NoteProbe 对已禁用号只记观测值，
//     不会重复判死）；
//   - 在途/并发满时不静默跳过，而是明确回报原因（批量跳过是为了不刷屏）。
//
// 返回值：ok=本次验活是否通过；errText=失败原因原文（给面板显示）；autoDisabled=本次是否
// 因连续失败触发自动禁用；err=根本没跑起来的原因（账号不存在/无凭证/正忙）。
func (s *Scheduler) ProbeOne(uid string) (ok bool, errText string, autoDisabled bool, err error) {
	a := s.cfg.Pool.AuthByUID(uid)
	if a == nil {
		return false, "", false, fmt.Errorf("账号不存在（可能已被移除）")
	}
	if !a.HasAccessToken() {
		return false, "", false, fmt.Errorf("没有 AccessToken（请重新登录该账号）")
	}
	if !s.cfg.Pool.Acquire(uid) {
		return false, "", false, fmt.Errorf("该账号正忙或已达并发上限，稍后再验")
	}
	perr := s.cfg.Upstream.ProbeChat(a, s.probeModel(), s.probePrompt())
	s.cfg.Pool.Release(uid)
	if perr == nil {
		s.cfg.Pool.NoteProbe(uid, true, "")
		log.Printf("probe uid=%s: 正常（手动验活）", uid)
		return true, "", false, nil
	}
	text := perr.Error()
	disabled := s.cfg.Pool.NoteProbe(uid, false, text)
	fails := 0
	if cur, found := s.cfg.Pool.Status(uid); found {
		fails = cur.ProbeFails
	}
	log.Printf("probe uid=%s: 失败（手动验活，连续第 %d 次）：%v", uid, fails, perr)
	return false, text, disabled, nil
}

// errMsg 把错误转成给 NoteProbe 存的文本；err 为 nil 时返回空串。
func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// probeModel 当前验活用的模型（热改优先，空回落 hy3 免费档）。
func (s *Scheduler) probeModel() string {
	s.probeMu.RLock()
	defer s.probeMu.RUnlock()
	if s.probeCfg.Model == "" {
		return "hy3"
	}
	return s.probeCfg.Model
}

// probePrompt 当前验活用的提示词（空回落 "hi"）。
func (s *Scheduler) probePrompt() string {
	s.probeMu.RLock()
	defer s.probeMu.RUnlock()
	if s.probeCfg.Prompt == "" {
		return "hi"
	}
	return s.probeCfg.Prompt
}

// SetProbeConfig 热改验活参数（面板保存配置时调用）。
// 间隔变化会唤醒循环重排；模型/提示词下一次验活即生效。
func (s *Scheduler) SetProbeConfig(model, prompt string, interval time.Duration) {
	s.probeMu.Lock()
	s.probeCfg.Model = model
	s.probeCfg.Prompt = prompt
	s.probeMu.Unlock()
	s.SetProbeInterval(interval)
}

// SetProbeInterval 热改验活间隔；<=0 表示停掉循环（面板关掉该开关时）。
func (s *Scheduler) SetProbeInterval(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.probeInterval.Store(int64(d))
	poke(s.rearmProbe)
}

// StartProbe 启动验活循环。interval <= 0 时先暂停，但保留热启用能力。
func (s *Scheduler) StartProbe(ctx context.Context, interval time.Duration) {
	s.probeInterval.Store(int64(interval))
	go func() {
		var logged time.Duration
		first := true
		for {
			cur := time.Duration(s.probeInterval.Load())
			if cur != logged {
				log.Printf("scheduler: 账号验活每 %s", cur)
				logged = cur
			}
			if cur <= 0 {
				// 被热改停掉：等重排通知或退出。
				select {
				case <-ctx.Done():
					return
				case <-s.rearmProbe:
					continue
				}
			}
			wait := cur
			if first {
				wait = min(probeFirstDelay, cur)
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-s.rearmProbe:
				timer.Stop() // 间隔已变：立刻按新值重算
			case <-timer.C:
				first = false
				s.RunProbeNow()
			}
		}
	}()
}
