package metrics

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// approx 浮点比较，容忍 1e-6。
func approx(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %v，期望 %v", what, got, want)
	}
}

// TestObserveAggregatesByModelAndAccount 一次请求应同时进 totals / models / accounts 三个维度。
func TestObserveAggregatesByModelAndAccount(t *testing.T) {
	r := New(time.Now())
	r.Observe("gpt-5", "cn", "uid-a", "", true, 100, 20, 7, 300*time.Millisecond, 2*time.Second, 0)
	r.Observe("gpt-5", "cn", "uid-b", "", true, 50, 10, 0, 100*time.Millisecond, time.Second, 0)
	r.Observe("claude", "cn", "uid-a", "", false, -1, -1, 0, 0, 500*time.Millisecond, 0)

	s := r.Snapshot()

	// totals：3 个请求，2 成功 1 失败
	if s.Totals.Requests != 3 || s.Totals.OK != 2 || s.Totals.Errors != 1 {
		t.Errorf("totals 请求计数错：%+v", s.Totals)
	}
	// token 只累加有 usage 的两次
	if s.Totals.PromptTokens != 150 || s.Totals.Completion != 30 || s.Totals.TotalTokens != 180 {
		t.Errorf("totals token 错：prompt=%d completion=%d total=%d",
			s.Totals.PromptTokens, s.Totals.Completion, s.Totals.TotalTokens)
	}
	if s.Totals.CachedTokens != 7 {
		t.Errorf("cached 应为 7，实得 %d", s.Totals.CachedTokens)
	}
	// 失败请求没有 usage 不算 MissingUsage
	if s.Totals.MissingUsage != 0 {
		t.Errorf("失败请求不该计入 MissingUsage，实得 %d", s.Totals.MissingUsage)
	}

	// models：降序，gpt-5（180 token）在 claude（0）之前
	if len(s.Models) != 2 {
		t.Fatalf("模型数应为 2，实得 %d", len(s.Models))
	}
	if s.Models[0].Key != "gpt-5" {
		t.Errorf("模型应按 token 降序，首位应为 gpt-5，实得 %s", s.Models[0].Key)
	}
	if s.Models[0].Requests != 2 || s.Models[0].TotalTokens != 180 {
		t.Errorf("gpt-5 聚合错：%+v", s.Models[0])
	}

	// accounts：uid-a 有 2 次（1 成功 1 失败），uid-b 1 次
	if len(s.Accounts) != 2 {
		t.Fatalf("账号数应为 2，实得 %d", len(s.Accounts))
	}
	var a *EntryView
	for i := range s.Accounts {
		if s.Accounts[i].Key == "uid-a" {
			a = &s.Accounts[i]
		}
	}
	if a == nil {
		t.Fatal("账号 uid-a 缺失")
	}
	if a.Requests != 2 || a.PromptTokens != 100 {
		t.Errorf("uid-a 聚合错：%+v", *a)
	}
}

// TestObserveAggregatesByKey 按密钥维度独立聚合。
//
// 为什么要单独测：keys 与 accounts 是**两个不可互推的维度**——一把密钥会打到
// 多个账号，一个账号也会被多把密钥用到。面板「按密钥」表只读这一个维度，
// 如果实现里偷懒复用 accounts（比如按 uid 前缀猜密钥），数字看起来合理但完全错。
//
// 这里刻意构造"一把密钥 × 两个账号"和"一个账号 × 两把密钥"两种交叉，
// 确保聚合真的按密钥分组。
func TestObserveAggregatesByKey(t *testing.T) {
	r := New(time.Now())
	const kA, kB = "fp-key-a", "fp-key-b"
	const u1, u2 = "uid-1", "uid-2"

	// kA 打到 u1 和 u2；kB 只打到 u1（同一账号被两把密钥用到）
	r.Observe("m", "cn", u1, kA, true, 100, 20, 0, 0, time.Second, 0)
	r.Observe("m", "cn", u2, kA, true, 200, 40, 0, 0, time.Second, 0)
	r.Observe("m", "cn", u1, kB, false, -1, -1, 0, 0, time.Second, 0)

	s := r.Snapshot()
	if len(s.Keys) != 2 {
		t.Fatalf("密钥数应为 2，实得 %d：%+v", len(s.Keys), s.Keys)
	}
	// 降序：kA 有 token（360），kB 没有（失败无 usage）→ kA 在前
	if s.Keys[0].Key != kA {
		t.Fatalf("密钥应按 token 降序，首位应为 %s，实得 %s", kA, s.Keys[0].Key)
	}
	if s.Keys[0].Requests != 2 || s.Keys[0].TotalTokens != 360 {
		t.Errorf("kA 聚合错（应含两个账号的量）：%+v", s.Keys[0])
	}
	if s.Keys[1].Key != kB || s.Keys[1].Requests != 1 || s.Keys[1].Errors != 1 {
		t.Errorf("kB 聚合错：%+v", s.Keys[1])
	}

	// 关键断言：同一账号 u1 被两把密钥用到，accounts 里它是一条，keys 里是两条。
	if len(s.Accounts) != 2 {
		t.Fatalf("账号数应为 2，实得 %d", len(s.Accounts))
	}
	for _, a := range s.Accounts {
		if a.Key == u1 && a.Requests != 2 {
			t.Errorf("u1 应聚合两把密钥的请求数=2，实得 %d", a.Requests)
		}
	}
}

// TestObserveKeyEmptyBucketKept 未启用鉴权时全部请求归入 ""，这张表不能是空的。
//
// 为什么不能跳过空 key：单密钥/不鉴权是最常见的家用配置。若实现照搬 uid 的
// "空值不建条目"，这些用户打开仪表盘会看到一张空表，而实际上流量跑得好好的。
func TestObserveKeyEmptyBucketKept(t *testing.T) {
	r := New(time.Now())
	r.Observe("m", "cn", "u", "", true, 10, 5, 0, 0, time.Second, 0)

	s := r.Snapshot()
	if len(s.Keys) != 1 {
		t.Fatalf("未鉴权时也应有 1 个密钥条目（空指纹），实得 %d", len(s.Keys))
	}
	if s.Keys[0].Key != "" || s.Keys[0].TotalTokens != 15 {
		t.Errorf("空指纹条目聚合错：%+v", s.Keys[0])
	}
	// 对照：uid 为空时**不**建账号条目（既有语义，不能因为这次改动被改掉）
	r2 := New(time.Now())
	r2.Observe("m", "cn", "", "", true, 10, 5, 0, 0, time.Second, 0)
	if got := len(r2.Snapshot().Accounts); got != 0 {
		t.Errorf("uid 为空不该建账号条目，实得 %d 个", got)
	}
}

// TestKeysSurviveRestart 密钥维度必须落盘。
//
// 面板显示的是"累计用量 · 重启保留"，按密钥这一维若是内存态，
// 用户每次重启都会看到它归零，而 totals/模型表还在——那种不一致
// 会被当成统计坏了，而不是"这一维没存"。
func TestKeysSurviveRestart(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "metrics.json")
	r := New(time.Now())
	r.Open(fp)
	r.Observe("gpt", "cn", "u1", "fp-a", true, 10, 4, 1, 100*time.Millisecond, time.Second, 0)
	r.Observe("gpt", "cn", "u2", "fp-b", true, 20, 6, 0, 100*time.Millisecond, time.Second, 0)
	r.Flush()

	r2 := New(time.Now())
	r2.Open(fp)
	s := r2.Snapshot()
	if len(s.Keys) != 2 {
		t.Fatalf("重启后密钥维度应恢复 2 条，实得 %d：%+v", len(s.Keys), s.Keys)
	}
	got := map[string]int64{}
	for _, k := range s.Keys {
		got[k.Key] = k.TotalTokens
	}
	if got["fp-a"] != 14 || got["fp-b"] != 26 {
		t.Errorf("重启后密钥 token 错：%+v", got)
	}
}

// TestLoadOldSnapshotWithoutKeys 旧 metrics.json（没有 keys 段）要能正常加载。
//
// 这是升级路径：用户手上的文件是上一版写的，没有 keys 字段。若解码后
// r.keys 是 nil，第一次 Observe 会往 nil map 写 → panic。
func TestLoadOldSnapshotWithoutKeys(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "metrics.json")
	// 模拟旧文件：结构完整但没有 keys
	old := `{"started":"2026-01-02T03:04:05Z","total":{"requests":7},
	         "models":{"m":{"requests":7}},"accounts":{"u":{"requests":7}},
	         "credits":{},"credit_samples":{},` +
		`"cred_used":0,"cred_tokens":0,"cred_n":0,"cred_remain":0,` +
		`"cred_used_on":0,"cred_ready":false}`
	if err := os.WriteFile(fp, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	r := New(time.Now())
	r.Open(fp)
	s := r.Snapshot()
	if s.Totals.Requests != 7 {
		t.Fatalf("旧文件应正常加载，实得 requests=%d", s.Totals.Requests)
	}
	if len(s.Keys) != 0 {
		t.Errorf("旧文件没有密钥数据，应为空，实得 %d", len(s.Keys))
	}
	// 关键：加载后必须能继续写入（nil map 会 panic）
	r.Observe("m", "cn", "u", "fp-new", true, 1, 1, 0, 0, time.Second, 0)
	if got := len(r.Snapshot().Keys); got != 1 {
		t.Errorf("旧文件加载后应能新增密钥条目，实得 %d", got)
	}
}

// TestMissingUsageCountsOnlySuccessWithoutUsage 「成功却没有 usage」才是上游异常信号。
//
// 这个语义很关键：失败请求本就没有 usage，若把它也算进 MissingUsage，
// 面板上的「缺 usage」就成了「失败数」的同义词，完全失去诊断意义。
func TestMissingUsageCountsOnlySuccessWithoutUsage(t *testing.T) {
	r := New(time.Now())
	r.Observe("m", "cn", "u", "", true, -1, -1, 0, 0, time.Second, 0)  // 200 但没 usage → 计入
	r.Observe("m", "cn", "u", "", false, -1, -1, 0, 0, time.Second, 0) // 失败无 usage → 不计入
	r.Observe("m", "cn", "u", "", true, 10, 5, 0, 0, time.Second, 0)   // 正常 → 不计入

	s := r.Snapshot()
	if s.Totals.MissingUsage != 1 {
		t.Errorf("MissingUsage 应为 1，实得 %d", s.Totals.MissingUsage)
	}
	if s.Totals.Requests != 3 {
		t.Errorf("缺 usage 的请求也要计入 Requests，实得 %d", s.Totals.Requests)
	}
}

// TestTokensPerSecIsRatioOfSums tok/s 必须是「输出 token 之和 ÷ 时长之和」，
// 而不是各请求速率的算术平均——后者会被一个极短请求带偏。
func TestTokensPerSecIsRatioOfSums(t *testing.T) {
	r := New(time.Now())
	// 请求 A：1 秒出 100 token（快）
	r.Observe("m", "cn", "", "", true, 0, 100, 0, 0, time.Second, 0)
	// 请求 B：100 秒出 100 token（慢）
	r.Observe("m", "cn", "", "", true, 0, 100, 0, 0, 100*time.Second, 0)

	s := r.Snapshot()
	// 和之比 = 200 / 101 ≈ 1.98（快照里已按 round2 保留两位）；算术平均则是 (100+1)/2 = 50.5
	approx(t, s.Totals.TokensPerSec, round2(200.0/101.0), "TokensPerSec")
	if s.Totals.TokensPerSec > 10 {
		t.Errorf("tok/s 被极端样本带偏了（%v），说明用了算术平均而不是和之比", s.Totals.TokensPerSec)
	}
}

// TestAvgTTFBOnlyStreaming 非流式传 ttfb=0 不应进入 TTFB 样本，否则均值被稀释。
func TestAvgTTFBOnlyStreaming(t *testing.T) {
	r := New(time.Now())
	r.Observe("m", "cn", "", "", true, 1, 1, 0, 400*time.Millisecond, time.Second, 0) // 流式
	r.Observe("m", "cn", "", "", true, 1, 1, 0, 0, time.Second, 0)                    // 非流式
	s := r.Snapshot()
	approx(t, s.Totals.AvgTTFBMS, 400, "AvgTTFBMS")
}

// TestCreditsRatioUsesDeltaOfSums 积分/token 比率取「Δ已用积分之和 ÷ Δtoken 之和」。
func TestCreditsRatioUsesDeltaOfSums(t *testing.T) {
	r := New(time.Now())

	// 建立基线
	r.Observe("m", "cn", "u", "", true, 0, 1000, 0, 0, time.Second, 0)
	r.NoteCredits("cn", "u", 100)

	// 再跑 4000 token，积分涨 200
	for i := 0; i < 4; i++ {
		r.Observe("m", "cn", "u", "", true, 0, 1000, 0, 0, time.Second, 0)
	}
	r.NoteCredits("cn", "u", 300)

	// 池级剩余积分
	r.SetCreditTotals("", 8000, 300, time.Now(), nil)

	s := r.Snapshot()
	if s.Credits.Samples != 1 {
		t.Fatalf("有效采样区间应为 1，实得 %d", s.Credits.Samples)
	}
	// Δused=200，Δtokens=4000 → 每百万 token 消耗 50000 积分
	approx(t, s.Credits.PerMToken, 50000, "PerMToken")
	// 8000 积分 ÷ 50000 积分/百万token = 0.16 百万 token = 160000
	if s.Credits.EstTokens != 160000 {
		t.Errorf("EstTokens 应为 160000，实得 %d", s.Credits.EstTokens)
	}
	if !s.Credits.Ready {
		t.Error("SetCreditTotals 之后 Ready 应为 true")
	}
	if s.Credits.Remain != 8000 || s.Credits.Used != 300 {
		t.Errorf("池级积分错：remain=%d used=%d", s.Credits.Remain, s.Credits.Used)
	}
}

// TestCreditsRatioKeepsSmallRates 小额费率不能被舍成 0。
//
// 这是真实生产事故的回归：实测环境 326 积分 / 336,245,157 token ≈ **0.97 积分/百万token**
// （旧口径 0.0011/千token）。而快照里用的是 round2 → 缓存命中率极高的环境仍可能归 0。
//
//	per_mtoken == 0  →  前端 rateKnown 为假  →  两张推算卡都显示「—」「待累积样本」
//
// 而 samples 早就有 182 个。用户等的是一个永远不会自己出现的数字。
//
// 为什么更早的测试没抓到：TestCreditsRatioUsesDeltaOfSums 用的费率是常规量级，
// 两位小数绰绰有余，正好绕过了这个精度边界。缓存命中率越高（命中部分基本不计费）
// 真实费率越小，越容易踩中。
//
// 注意 0 在这套代码里是「没有样本」的哨兵值（Ready / rateKnown 都靠它区分），
// 所以「算出来了但值很小」绝不能被舍成 0——那样的误判从外部看不出来。
func TestCreditsRatioKeepsSmallRates(t *testing.T) {
	r := New(time.Now())

	// 基线：1000 token，已用 100 积分
	r.Observe("m", "cn", "u", "", true, 0, 1000, 0, 0, time.Second, 0)
	r.NoteCredits("cn", "u", 100)

	// 再跑 3,000,000 token，积分只涨 3 → 1 积分/百万token（千分位×口径放大前的旧万分位）
	for i := 0; i < 3; i++ {
		r.Observe("m", "cn", "u", "", true, 0, 1000000, 0, 0, time.Second, 0)
	}
	r.NoteCredits("cn", "u", 103)

	r.SetCreditTotals("", 5000, 103, time.Now(), nil)
	s := r.Snapshot()

	if s.Credits.Samples != 1 {
		t.Fatalf("有效采样区间应为 1，实得 %d", s.Credits.Samples)
	}
	if s.Credits.PerMToken == 0 {
		t.Fatal("小额费率被舍成了 0——前端会把它当成「没有样本」，两张推算卡都显示「—」")
	}
	approx(t, s.Credits.PerMToken, 1, "PerMToken")
	// 5000 积分 ÷ 1 积分/百万token = 5,000 百万 token
	if s.Credits.EstTokens != 5000000000 {
		t.Errorf("EstTokens 应为 5000000000，实得 %d", s.Credits.EstTokens)
	}
	// 外推用的是未舍入的原始比率：显示层精度不该让预估值漂移。
	// 用一条更"脏"的费率再验证一次（Δused=1 / Δtokens=3,000,000 → 约 0.333 积分/百万token）。
	//
	// 断言用区间而不是精确值：这里刻意让比率除不尽，浮点尾数会让 int64 截断差个几十，
	// 锁精确值等于把测试绑在浮点实现上。真正要挡住的是"用舍入后的比率外推"——
	// 那样会得到 0/per 或量级完全不同的数，区间足以区分。
	r2 := New(time.Now())
	r2.NoteCredits("cn", "u", 1) // 此时还没有任何 token：基线 tokens=0
	r2.Observe("m", "cn", "u", "", true, 0, 3000000, 0, 0, time.Second, 0)
	r2.NoteCredits("cn", "u", 2)
	r2.SetCreditTotals("", 1000, 2, time.Now(), nil)
	s2 := r2.Snapshot()

	if s2.Credits.PerMToken == 0 {
		t.Fatal("更小的费率同样不能归零")
	}
	// 1000 积分 ÷ (1/3 积分/百万token) = 3,000 百万 token = 3e9
	if s2.Credits.EstTokens < 2999999000 || s2.Credits.EstTokens > 3000001000 {
		t.Errorf("EstTokens 应约 3e9（按未舍入比率外推），实得 %d", s2.Credits.EstTokens)
	}
}

// TestCreditsFirstSampleIsBaselineOnly 首次采样只建立基线，不产生比率样本——
// 否则「进程刚起来就被当成一次 0→N 的跳变」，比率会被污染。
func TestCreditsFirstSampleIsBaselineOnly(t *testing.T) {
	r := New(time.Now())
	r.Observe("m", "cn", "u", "", true, 0, 500, 0, 0, time.Second, 0)
	r.NoteCredits("cn", "u", 999)
	s := r.Snapshot()
	if s.Credits.Samples != 0 {
		t.Errorf("首次采样不该产生样本，实得 %d", s.Credits.Samples)
	}
	if s.Credits.PerMToken != 0 {
		t.Errorf("无样本时 PerMToken 应为 0，实得 %v", s.Credits.PerMToken)
	}
}

// TestCreditsSkipsBadDeltas 负增量（计费周期重置/上游回退）与零 token 增量都不采信。
func TestCreditsSkipsBadDeltas(t *testing.T) {
	r := New(time.Now())

	r.Observe("m", "cn", "u", "", true, 0, 1000, 0, 0, time.Second, 0)
	r.NoteCredits("cn", "u", 500)

	// 周期重置：used 回退 → 跳过
	r.NoteCredits("cn", "u", 10)
	// 无新流量：Δtoken==0 → 跳过
	r.NoteCredits("cn", "u", 20)

	s := r.Snapshot()
	if s.Credits.Samples != 0 {
		t.Errorf("坏增量不该产生样本，实得 %d", s.Credits.Samples)
	}
}

// TestBurnRateUsesNetBalance 续航必须按余额净变化算，签到进账要能被算进去。
//
// 这是这块最容易写错的地方：如果用 token 速率倒推，签到发的积分完全不可见，
// 会出现"余额在涨却显示正在烧光"。所以这里锁死：余额涨 → 速率为负 → 不报倒计时。
func TestBurnRateUsesNetBalance(t *testing.T) {
	base := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)

	// 纯消耗：4 小时余额从 10000 掉到 6000 → 净消耗 4000，速率 1000/时
	r := New(base)
	r.SetCreditTotals("", 10000, 0, base, nil)
	r.SetCreditTotals("", 6000, 4000, base.Add(4*time.Hour), nil)
	s := r.Snapshot()
	if s.Credits.BurnPerHour != 1000 {
		t.Errorf("净消耗速率 = %v，期望 1000", s.Credits.BurnPerHour)
	}
	if s.Credits.BurnNet != 4000 {
		t.Errorf("净变化 = %d，期望 4000（正数=净消耗）", s.Credits.BurnNet)
	}
	if s.Credits.RunwayHours != 6 {
		t.Errorf("续航 = %v 小时，期望 6（6000/1000）", s.Credits.RunwayHours)
	}

	// 签到进账大于消耗：余额从 6000 涨到 8000 → 速率为负，不给倒计时
	r2 := New(base)
	r2.SetCreditTotals("", 6000, 0, base, nil)
	r2.SetCreditTotals("", 8000, 0, base.Add(4*time.Hour), nil)
	s2 := r2.Snapshot()
	if s2.Credits.BurnPerHour >= 0 {
		t.Errorf("余额在涨时速率应为负，实得 %v（签到进账没被算进去？）", s2.Credits.BurnPerHour)
	}
	if s2.Credits.RunwayHours != 0 {
		t.Errorf("净增长时不应给续航，实得 %v", s2.Credits.RunwayHours)
	}
}

// TestBurnRateNeedsMinSpan 跨度不足 creditMinSpan（4 小时）时不出速率。
//
// 门槛定在 4 小时是为了避开签到脉冲：签到每天给全池灌一次约 1,100 积分，
// 窗口太短会随机包含/不包含这次脉冲，净速率被带得忽大忽小。
func TestBurnRateNeedsMinSpan(t *testing.T) {
	base := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	r := New(base)
	r.SetCreditTotals("", 10000, 0, base, nil)
	r.SetCreditTotals("", 9000, 1000, base.Add(2*time.Hour), nil) // 只有 2 小时，不足门槛
	s := r.Snapshot()
	if s.Credits.BurnSpanSec != 0 {
		t.Errorf("跨度不足时 BurnSpanSec 应为 0，实得 %d", s.Credits.BurnSpanSec)
	}
	if s.Credits.RunwayHours != 0 {
		t.Errorf("样本不足时不该给续航，实得 %v", s.Credits.RunwayHours)
	}
}

// TestBurnRateWorksAtThreshold 达到 creditMinSpan 就出速率。
func TestBurnRateWorksAtThreshold(t *testing.T) {
	base := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	r := New(base)
	r.SetCreditTotals("", 10000, 0, base, nil)
	r.SetCreditTotals("", 9000, 1000, base.Add(4*time.Hour), nil) // 正好 4 小时
	s := r.Snapshot()
	if s.Credits.BurnSpanSec == 0 {
		t.Fatal("满 4 小时窗口应能出速率")
	}
	// 1000 积分 / 4 小时 = 250 积分/时 = 6000 积分/天
	if s.Credits.BurnPerHour != 250 {
		t.Errorf("速率 = %v，期望 250", s.Credits.BurnPerHour)
	}
	if s.Credits.RunwayHours != 36 {
		t.Errorf("续航 = %v，期望 36（9000/250）", s.Credits.RunwayHours)
	}
}

// TestCreditSeriesTrimsWindow 余额序列按时间窗口滚动裁剪，不能无限增长。
// 窗口现在是 14 天（流水图要按天算净变化）；裁剪行为用 18 天的采样验证。
func TestCreditSeriesTrimsWindow(t *testing.T) {
	base := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	r := New(base)
	// 铺 18 天的采样（每小时一个），窗口 14 天 → 最早约 4 天被裁掉
	for i := 0; i < 24*18; i++ {
		r.SetCreditTotals("", 10000-int64(i)*10, int64(i)*10, base.Add(time.Duration(i)*time.Hour), nil)
	}
	s := r.Snapshot()
	if s.Credits.BurnSpanSec > int64(14*24*time.Hour/time.Second)+3600 {
		t.Errorf("窗口未裁剪：跨度 %d 秒超过 14 天窗口", s.Credits.BurnSpanSec)
	}
	if s.Credits.BurnSpanSec < int64(13*24*time.Hour/time.Second) {
		t.Errorf("窗口裁过头：跨度 %d 秒不足 13 天", s.Credits.BurnSpanSec)
	}
	if n := len(r.credSeries); n > creditMaxPoints {
		t.Errorf("序列点数 %d 超过上限 %d", n, creditMaxPoints)
	}
}

// TestSetCreditTotalsClampsNegativeRemain 上游给负余额（欠费/字段异常）时钳到 0，
// 否则面板会显示「-12345」，且预估可用 token 会变成负数。
func TestSetCreditTotalsClampsNegativeRemain(t *testing.T) {
	r := New(time.Now())
	r.SetCreditTotals("", -42, 100, time.Now(), nil)
	s := r.Snapshot()
	if s.Credits.Remain != 0 {
		t.Errorf("负余额应钳到 0，实得 %d", s.Credits.Remain)
	}
}

// TestCreditsAgeSec 从未刷新时 AgeSec = -1；刷新过则为距今年秒数。
func TestCreditsAgeSec(t *testing.T) {
	r := New(time.Now())
	if got := r.Snapshot().Credits.AgeSec; got != -1 {
		t.Errorf("未刷新时 AgeSec 应为 -1，实得 %d", got)
	}
	r.SetCreditTotals("", 1, 1, time.Now().Add(-90*time.Second), nil)
	got := r.Snapshot().Credits.AgeSec
	if got < 88 || got > 95 {
		t.Errorf("AgeSec 应约为 90，实得 %d", got)
	}
}

// TestBucketsFillWindowAndOrder 快照必须补齐最近 24 小时的桶（缺失的补 0），
// 且按时间升序——前端直接按下标画柱，不用自己补空洞。
func TestBucketsFillWindowAndOrder(t *testing.T) {
	r := New(time.Now())
	r.Observe("m", "cn", "", "", true, 10, 5, 0, 0, time.Second, 0)

	s := r.Snapshot()
	if len(s.Buckets) != bucketHours {
		t.Fatalf("桶数应为 %d，实得 %d", bucketHours, len(s.Buckets))
	}
	for i := 1; i < len(s.Buckets); i++ {
		if s.Buckets[i].Hour <= s.Buckets[i-1].Hour {
			t.Fatalf("桶未按时间升序：idx %d hour %d <= idx %d hour %d",
				i, s.Buckets[i].Hour, i-1, s.Buckets[i-1].Hour)
		}
	}
	// 刚发生的请求落在最后一格
	last := s.Buckets[len(s.Buckets)-1]
	if last.Requests != 1 || last.Tokens != 15 {
		t.Errorf("最新桶应含 1 请求 15 token，实得 %+v", last)
	}
	// 更早的桶都是 0
	if s.Buckets[0].Requests != 0 || s.Buckets[0].Tokens != 0 {
		t.Errorf("最早桶应为空，实得 %+v", s.Buckets[0])
	}
}

// TestObserveEmptyModelFolds 空 model 归入 "-"；空 uid 不建账号条目。
func TestObserveEmptyModelFolds(t *testing.T) {
	r := New(time.Now())
	r.Observe("", "cn", "", "", true, 1, 1, 0, 0, time.Second, 0)
	s := r.Snapshot()
	if len(s.Models) != 1 || s.Models[0].Key != "-" {
		t.Errorf("空 model 应归入 \"-\"，实得 %+v", s.Models)
	}
	if len(s.Accounts) != 0 {
		t.Errorf("空 uid 不该建账号条目，实得 %+v", s.Accounts)
	}
}

// TestUptimeNonNegative 时钟回拨也不该给出负运行时长。
func TestUptimeNonNegative(t *testing.T) {
	r := New(time.Now().Add(time.Hour)) // 起始时刻在未来
	if got := r.Snapshot().UptimeSec; got != 0 {
		t.Errorf("运行时长应钳到 0，实得 %d", got)
	}
}

// TestSnapshotIsJSONSerializable 快照是对外 JSON 契约，必须能序列化且字段名稳定。
func TestSnapshotIsJSONSerializable(t *testing.T) {
	r := New(time.Now())
	r.Observe("m", "cn", "u", "", true, 10, 5, 2, time.Second, 2*time.Second, 0)
	r.SetCreditTotals("", 1000, 50, time.Now(), nil)

	b, err := json.Marshal(r.Snapshot())
	if err != nil {
		t.Fatalf("快照无法序列化：%v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("序列化结果无法反解：%v", err)
	}
	for _, k := range []string{"uptime_sec", "totals", "models", "accounts", "buckets", "credits"} {
		if _, ok := back[k]; !ok {
			t.Errorf("JSON 缺少顶层字段 %q", k)
		}
	}
	totals := back["totals"].(map[string]any)
	for _, k := range []string{"requests", "ok", "errors", "prompt_tokens", "completion_tokens",
		"total_tokens", "cached_tokens", "missing_usage", "tokens_per_sec"} {
		if _, ok := totals[k]; !ok {
			t.Errorf("totals JSON 缺少字段 %q", k)
		}
	}
	credits := back["credits"].(map[string]any)
	for _, k := range []string{"remain", "used", "samples", "per_mtoken", "est_tokens", "ready", "age_sec",
		"burn_per_hour", "runway_hours", "burn_span_sec", "burn_net"} {
		if _, ok := credits[k]; !ok {
			t.Errorf("credits JSON 缺少字段 %q", k)
		}
	}
}

// TestConcurrentObserve 并发写入必须安全（-race 下跑）。
func TestConcurrentObserve(t *testing.T) {
	r := New(time.Now())
	var wg sync.WaitGroup
	const n = 50
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Observe("m", "cn", "u", "", true, 1, 2, 0, time.Millisecond, 2*time.Millisecond, 0)
			r.NoteCredits("cn", "u", int64(i))
		}(i)
	}
	wg.Wait()
	s := r.Snapshot()
	if s.Totals.Requests != n {
		t.Errorf("并发写入后请求数应为 %d，实得 %d", n, s.Totals.Requests)
	}
	if s.Totals.TotalTokens != int64(n*3) {
		t.Errorf("并发写入后 token 应为 %d，实得 %d", n*3, s.Totals.TotalTokens)
	}
}

func TestMetricsPersistRoundTrip(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "metrics.json")
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r := New(started)
	r.Open(fp)
	r.Observe("gpt", "cn", "u1", "", true, 10, 4, 1, 100*time.Millisecond, time.Second, 0)
	r.NoteCredits("cn", "u1", 50)
	r.NoteCredits("cn", "u1", 80)
	r.SetCreditTotals("", 900, 80, time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC), nil)
	r.Flush()

	r2 := New(time.Now())
	r2.Open(fp)
	s := r2.Snapshot()
	if s.Totals.Requests != 1 || s.Totals.TotalTokens != 14 {
		t.Fatalf("reloaded totals=%+v", s.Totals)
	}
	if !s.Started.Equal(started) {
		t.Fatalf("started not restored: %v", s.Started)
	}
	if !s.Credits.Ready || s.Credits.Remain != 900 {
		t.Fatalf("credits not restored: %+v", s.Credits)
	}
}

// TestCreditSeriesSurvivesRestart 余额序列必须落盘。
//
// 这是用户直接问过的问题：「积分概览不应该因为我的重启而数据消失吧？」
// 序列一旦只在内存里，每次重启都要从零重新攒够窗口——续航就永远出不来。
func TestCreditSeriesSurvivesRestart(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "metrics.json")
	base := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	r := New(base)
	r.Open(fp)
	r.SetCreditTotals("", 10000, 0, base, nil)
	r.SetCreditTotals("", 9000, 1000, base.Add(4*time.Hour), nil)
	if s := r.Snapshot(); s.Credits.BurnSpanSec == 0 {
		t.Fatal("前置条件：重启前应已能算出速率")
	}
	r.Flush()

	// 重启：没有发过任何 /v1 请求，只有余额序列
	r2 := New(time.Now())
	r2.Open(fp)
	s2 := r2.Snapshot()
	if s2.Credits.BurnSpanSec == 0 {
		t.Fatal("重启后余额序列丢失——续航又变回「样本不足」了")
	}
	if s2.Credits.BurnPerHour != 250 {
		t.Errorf("重启后速率 = %v，期望 250", s2.Credits.BurnPerHour)
	}
	if s2.Credits.RunwayHours <= 0 {
		t.Errorf("重启后应仍能给出续航，实得 %v", s2.Credits.RunwayHours)
	}
}

// TestExpiringCreditsReported 到期信息要**整列**如实透出，且不能因为某轮取不到就被抹掉。
//
// 为什么断言"整列"：实测同一批积分分散在多个日期到期且金额极不均
// （10-11 仅 1,600，10-13 却有 35,500）。只报最近一天会让人误判风险很小——
// 一个报错的数字比没有数字更糟。这条测试锁住"必须全给"。
func TestExpiringCreditsReported(t *testing.T) {
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	r := New(base)

	// 从没设过 → 没有会过期的批次（全是长期额度）
	// 用 SnapshotAt(base) 而不是 Snapshot()：后者内部取 time.Now()，
	// 注入的 base 就不起作用，测试会在当天 12:00 UTC 之后稳定失败。
	if s := r.SnapshotAt(base); s.Credits.ExpiringDays != -1 || len(s.Credits.ExpiringBatches) != 0 {
		t.Errorf("未设到期信息时应为 -1 且无批次，实得 %d / %d 条",
			s.Credits.ExpiringDays, len(s.Credits.ExpiringBatches))
	}

	// 仿真实分布：四天分散到期，金额集中在第三天
	d11 := base.Add(26 * 24 * time.Hour)
	d12 := base.Add(27 * 24 * time.Hour)
	d13 := base.Add(28 * 24 * time.Hour)
	d14 := base.Add(29 * 24 * time.Hour)
	r.SetCreditTotals("", 45159, 100, base, []ExpiryBatch{
		{At: d11, Remain: 1600},
		{At: d12, Remain: 100},
		{At: d13, Remain: 35500},
		{At: d14, Remain: 2860},
	})
	s := r.SnapshotAt(base)
	if s.Credits.ExpiringDays != 26 {
		t.Errorf("最近失效日应为 26 天后，实得 %d", s.Credits.ExpiringDays)
	}
	if len(s.Credits.ExpiringBatches) != 4 {
		t.Fatalf("必须整列给出 4 个失效日，实得 %d 条", len(s.Credits.ExpiringBatches))
	}
	// 升序：前端直接按序渲染，不用自己排。
	for i, want := range []struct {
		at     string
		days   int
		remain int64
	}{
		{d11.Format("2006-01-02"), 26, 1600},
		{d12.Format("2006-01-02"), 27, 100},
		{d13.Format("2006-01-02"), 28, 35500},
		{d14.Format("2006-01-02"), 29, 2860},
	} {
		got := s.Credits.ExpiringBatches[i]
		if got.At != want.at || got.Days != want.days || got.Remain != want.remain {
			t.Errorf("第 %d 条 = %+v，期望 %s/%d/%d", i, got, want.at, want.days, want.remain)
		}
	}
	// 总额含长期额度，所以一定 ≥ 会失效部分——不满足就是口径搞反了。
	var sum int64
	for _, b := range s.Credits.ExpiringBatches {
		sum += b.Remain
	}
	if s.Credits.Remain < sum {
		t.Errorf("剩余总额 %d 不该小于会失效的 %d", s.Credits.Remain, sum)
	}

	// 传 nil = 本轮没取到 → 保留上一轮的值，不能把已知的到期信息抹成"没有"
	r.SetCreditTotals("", 45000, 200, base.Add(time.Minute), nil)
	s2 := r.SnapshotAt(base.Add(time.Minute))
	if len(s2.Credits.ExpiringBatches) != 4 {
		t.Errorf("nil 不该抹掉到期信息，实得 %d 条", len(s2.Credits.ExpiringBatches))
	}
}

// TestExpiringDaysNeverNegative 上游万一返回已过期的批次，天数钳到 0，不显示负数。
func TestExpiringDaysNeverNegative(t *testing.T) {
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	r := New(base)
	r.SetCreditTotals("", 100, 0, base, []ExpiryBatch{{At: base.Add(-48 * time.Hour), Remain: 100}})
	if got := r.SnapshotAt(base).Credits.ExpiringDays; got != 0 {
		t.Errorf("已过期批次的天数应钳到 0，实得 %d", got)
	}
}

func TestMetricsFlushDoesNotClobberHistory(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "metrics.json")
	r := New(time.Now())
	r.Open(fp)
	r.Observe("gpt", "cn", "u1", "", true, 10, 4, 0, 0, time.Second, 0)
	r.Flush()

	empty := New(time.Now())
	empty.Open(fp) // 读回 1 次请求
	empty.dirty.Store(true)
	empty.total = Counters{} // 模拟空进程误写
	empty.models = map[string]*Counters{}
	empty.Flush()

	r2 := New(time.Now())
	r2.Open(fp)
	if r2.Snapshot().Totals.Requests != 1 {
		t.Fatalf("empty flush must not wipe history, got %+v", r2.Snapshot().Totals)
	}
}
