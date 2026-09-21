// Package metrics 进程内用量指标：把 /v1 请求的 token 用量按模型/账号聚合，
// 并统计积分消耗速率，供管理面板「仪表盘」展示。
//
// 默认落 data/metrics.json：仪表盘数字重启后还在。空路径（测试）不落盘。
//
// 并发模型：单个 sync.Mutex 保护全部字段。写入点在每个 chat 请求出口，
// 数量级是「每秒几次」而不是「每毫秒几次」，单锁足够；用分片锁只会把
// 复杂度换来看不见的收益。
package metrics

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// bucketHours 时序窗口长度（小时）。1 小时粒度、滚动 24 小时。
//
// 为什么不是分钟：管理面板关心的是「今天什么时候在跑」，不是「哪一分钟抖动」。
// 60 根分钟柱看着热闹，但一次批量签到就能把某一根顶满、其余全空，反而看不出节奏。
const bucketHours = 24

// Counters 一组用量计数。零值可用。
type Counters struct {
	Requests   int64
	OK         int64
	Errors     int64
	Prompt     int64
	Completion int64
	Cached     int64
	// Credits 该维度累计消耗的积分（上游 usage.credit 原值求和）。
	// 按密钥维度透出后，面板能回答「哪把密钥花了多少积分」——token 量
	// 不等于积分额（不同模型倍率不同、缓存命中不计费），这个数只能实测累计。
	Credits float64
	// MissingUsage 统计「上游没给 usage」的请求数（流式被截断/上游异常）。
	// 这些请求不计入任何 token 累计，但计入 Requests——否则总数对不上。
	MissingUsage int64

	ttfbSum           time.Duration
	ttfbN             int64
	durSum            time.Duration
	durN              int64
	completionForRate int64 // 参与 tok/s 的 completion（只统计有 duration 的样本）
	durForRate        time.Duration
}

// add 把一次观测并入计数。prompt/completion/cached 为 -1 表示 usage 缺失；
// credit 为本次上游实际扣除的积分（usage 缺失时传 0，不计入）。
func (c *Counters) add(ok bool, prompt, completion, cached int, ttfb, dur time.Duration, credit float64) {
	c.Requests++
	if ok {
		c.OK++
	} else {
		c.Errors++
	}
	if prompt < 0 || completion < 0 {
		// 失败请求本来就没有 usage；只有「成功却拿不到 usage」才是上游异常信号。
		if ok {
			c.MissingUsage++
		}
		return
	}
	c.Prompt += int64(prompt)
	c.Completion += int64(completion)
	if cached > 0 {
		c.Cached += int64(cached)
	}
	if credit > 0 {
		c.Credits += credit
	}
	// TTFB 只有流式路径有；0 值（未观测到）不入样本，避免把均值拉平。
	if ttfb > 0 {
		c.ttfbSum += ttfb
		c.ttfbN++
	}
	if dur > 0 {
		c.durSum += dur
		c.durN++
		// tok/s 只按「有 duration 且有 completion」的请求算：占比加权，
		// 而不是各请求速率的算术平均（后者会被一个极短请求带偏）。
		c.completionForRate += int64(completion)
		c.durForRate += dur
	}
}

// CountersView 是 Counters 的可序列化投影。
type CountersView struct {
	Requests     int64 `json:"requests"`
	OK           int64 `json:"ok"`
	Errors       int64 `json:"errors"`
	PromptTokens int64 `json:"prompt_tokens"`
	Completion   int64 `json:"completion_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
	CachedTokens int64 `json:"cached_tokens"`
	// Credits 累计消耗积分。**关键用途**：按密钥维度回答「谁花了多少积分」。
	Credits      float64 `json:"credits"`
	MissingUsage int64   `json:"missing_usage"`
	AvgTTFBMS    float64 `json:"avg_ttfb_ms"`
	AvgDurMS     float64 `json:"avg_duration_ms"`
	TokensPerSec float64 `json:"tokens_per_sec"`
}

func (c *Counters) CountersView() CountersView {
	v := CountersView{
		Requests:     c.Requests,
		OK:           c.OK,
		Errors:       c.Errors,
		PromptTokens: c.Prompt,
		Completion:   c.Completion,
		TotalTokens:  c.Prompt + c.Completion,
		CachedTokens: c.Cached,
		Credits:      round2(c.Credits),
		MissingUsage: c.MissingUsage,
	}
	if c.ttfbN > 0 {
		v.AvgTTFBMS = ms(c.ttfbSum / time.Duration(c.ttfbN))
	}
	if c.durN > 0 {
		v.AvgDurMS = ms(c.durSum / time.Duration(c.durN))
	}
	if c.durForRate > 0 {
		v.TokensPerSec = round2(float64(c.completionForRate) / c.durForRate.Seconds())
	}
	return v
}

// EntryView 是带键的 CountersView（模型名 / uid）。
type EntryView struct {
	CountersView
	Key string `json:"key"`
}

// Bucket 一小时的用量。
type Bucket struct {
	Hour     int64 `json:"hour"` // Unix 小时（Unix 秒 / 3600）
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

// creditSample 单账号的积分观测点：used 为该周期已消耗积分，tokens 为
// 同时刻本进程累计消耗的 token。两次采样之差给出实测的「积分/token」。
type creditSample struct {
	used   int64
	tokens int64
}

// creditPoint 池级余额的一次采样（时间 + 剩余积分）。
//
// 为什么必须单独存这条序列：回答「积分还能用多久」只有一种可靠做法——量余额自身的净变化。
// 它天然把签到/任务发的积分算进去。反过来，用 token 速率倒推是**看不见进账的**：
// 今天耗 100、明天签到领 1000，token 速率法会算出"正在快速烧光"，而真相是余额在涨。
type creditPoint struct {
	at     time.Time
	remain int64
}

// creditWindow 余额序列的滚动窗口；creditMaxPoints 是条数上限（按 5 分钟刷新≈288 点/天）。
// 窗口取 14 天：流水图（消耗 vs 获取）要按天算净变化，窗口必须盖过图窗。
// 4400 点上限 ≈ 14 天全量采样 + 余量；透出 API 时仍降采样到 seriesMax。
const (
	creditWindow    = 14 * 24 * time.Hour
	creditMaxPoints = 4400
	// creditMinSpan 出速率结论的最短窗口。
	//
	// 为什么是 4 小时：签到每天给全池灌一次脉冲（约 1,100 积分，账号数 × 100）。
	// 窗口短于一天时，里面要么含这次脉冲、要么不含，净速率会被它带得忽大忽小。
	// 但真要求满 24 小时，刚启动的头一天就完全没数可看（用户实测窗口只有 7.7 小时）。
	// 取 4 小时折中：够滤掉"同一秒重复采样"和短时抖动，又能及时出数。
	// 不满 24 小时的窗口前端会标注「不足一天」，不假装它等于日净变化。
	creditMinSpan = 4 * time.Hour
)

// Registry 指标聚合器。并发安全；零值不可用，必须经 New 构造。
type Registry struct {
	mu      sync.Mutex
	started time.Time
	path    string
	dirty   atomic.Bool

	total    Counters
	models   map[string]*Counters
	accounts map[string]*Counters
	// keys 按密钥指纹聚合（哪把密钥在消耗）。与 accounts 是两个不可互推的维度：
	// accounts 回答「哪台号被消耗」，keys 回答「谁在调用」——一把密钥会打到多个
	// 账号，一个账号也会被多把密钥用到，谁也推不出谁。
	//
	// **accounts 必须保留**：积分/token 实测比率（NoteCredits）靠它把 token
	// 增量归因到账号上。面板不再展示账号表，但这一维度仍在算。
	keys    map[string]*Counters
	buckets map[int64]*Bucket // key = Unix 小时；读取时按窗口裁剪

	credits map[string]*creditSample // uid → 上次采样
	// 比率估计用「和之比」而不是「比之和」：sum(dUsed)/sum(dTokens)。
	// 后者（各账号各区间比率的算术平均）会让只跑了几个请求的账号和
	// 跑了上万个请求的账号等权，明显不合理。
	credUsed   int64
	credTokens int64
	credN      int64 // 有效采样区间数
	credRemain int64 // 最近一次已知的池内剩余积分合计
	credUsedOn int64 // 最近一次已知的池内已用积分合计
	credAt     time.Time
	credReady  bool // 是否至少有过一次成功的积分采样
	// credSeries 池级余额时间序列（滚动 creditWindow），用于算净消耗速率 / 续航。
	credSeries []creditPoint
	// 会过期的积分：最近失效日、那天会失效多少、以及按日期升序的完整批次。
	// 与 credRemain 的区别：credRemain 是上游口径的总额（含长期额度），这里只算会走的。
	credExpiring []ExpiryBatch

	// realms 分域计数（第 2 步）：面板顶部的国服/国际服开关切到某一域时，仪表盘要靠它出那一
	// 域的数字。**只增不改**——上面的全量字段一行不动（零回归）；历史（升级前落盘的）数据没有
	// 域维度，留在全量里、不进任何域，面板据此标注"分域统计自 X 起"，不假装它属于某域。
	//
	// 维度比全量少两项：不含 accounts（面板域视图不做账号级聚合）与积分序列（池级采样序列
	// 无法按域拆——积分余额的域归属要靠账号余额求和，而那是面板侧的事）。
	realms     map[string]*realmCounters
	realmSince time.Time
}

// realmCounters 单个域的计数（与全量同构，但只含分域之后产生的量）。
type realmCounters struct {
	total   Counters
	models  map[string]*Counters
	keys    map[string]*Counters
	buckets map[int64]*Bucket // key = Unix 小时

	// 积分口径（第 3 步）：域级余额采样与比率。算法与池级**共用同一实现**
	//（creditsViewOf），只是把范围限定在该域账号上——两套公式分开写迟早漂移。
	credRemain   int64
	credUsedOn   int64
	credAt       time.Time
	credReady    bool
	credSeries   []creditPoint
	credExpiring []ExpiryBatch
	credUsed     int64 // 比率累加器（Δ积分）
	credTokens   int64 // 比率累加器（Δtoken）
	credN        int64 // 有效采样区间数
}

// realmLocked 取（必要时创建）某域的计数桶。调用方必须已持 r.mu。
// 只认 cn/global：别的值一律返回 nil（不建桶），调用方据此跳过——宁可少记，不记错。
func (r *Registry) realmLocked(realm string) *realmCounters {
	if realm != "cn" && realm != "global" {
		return nil
	}
	rc := r.realms[realm]
	if rc == nil {
		rc = newRealmCounters()
		r.realms[realm] = rc
		if r.realmSince.IsZero() {
			r.realmSince = time.Now() // 分域统计起点（面板标注"此前数据不分域"用）
		}
	}
	return rc
}

func newRealmCounters() *realmCounters {
	return &realmCounters{
		models:  map[string]*Counters{},
		keys:    map[string]*Counters{},
		buckets: map[int64]*Bucket{},
	}
}

// RealmView 单个域的快照视图：与全量 Snapshot 的同名字段同形，前端可整块互换使用。
type RealmView struct {
	Totals  CountersView `json:"totals"`
	Models  []EntryView  `json:"models"`
	Keys    []EntryView  `json:"keys"`
	Buckets []Bucket     `json:"buckets"`
	// Credits 该域的积分口径（余额/消耗/实测比率/净速率/到期批次）：算法与池级共用同一实现
	//（creditsViewOf），只是范围限定在该域账号上。
	Credits CreditsView `json:"credits"`
}

// Started 指标起始时刻（落盘后重启仍是第一次启动的时间）。
func (r *Registry) Started() time.Time {
	if r == nil {
		return time.Time{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// New 构造 Registry。started 为进程/服务起始时刻，仅用于展示运行时长。
func New(started time.Time) *Registry {
	return &Registry{
		started:  started,
		models:   map[string]*Counters{},
		accounts: map[string]*Counters{},
		keys:     map[string]*Counters{},
		buckets:  map[int64]*Bucket{},
		credits:  map[string]*creditSample{},
		realms:   map[string]*realmCounters{},
	}
}

// Observe 记录一次 /v1 请求的用量。
//
// model 为空时归入 "-"；uid 为空时只累加全局与模型维度（不建账号条目）；
// key 为密钥指纹（httpauth.Fingerprint），为空表示未启用鉴权。
// realm 是该请求实际落地的域（cn/global；未落号传空），用于分域视图（第 2 步）。
// prompt/completion < 0 表示上游未给 usage，此时只计请求数与 MissingUsage；
// credit 为本次上游实扣积分（usage 缺失时传 0）。
// ttfb 仅流式路径有，非流式传 0（不入 TTFB 样本）。
func (r *Registry) Observe(model, realm, uid, key string, ok bool, prompt, completion, cached int, ttfb, dur time.Duration, credit float64) {
	now := time.Now()
	if model == "" {
		model = "-"
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.total.add(ok, prompt, completion, cached, ttfb, dur, credit)
	c := r.models[model]
	if c == nil {
		c = &Counters{}
		r.models[model] = c
	}
	c.add(ok, prompt, completion, cached, ttfb, dur, credit)
	if uid != "" {
		a := r.accounts[uid]
		if a == nil {
			a = &Counters{}
			r.accounts[uid] = a
		}
		a.add(ok, prompt, completion, cached, ttfb, dur, credit)
	}
	// 密钥维度不跳过空值：未启用鉴权时全部请求都归到 ""，那正是"只有一把
	// 隐式密钥"的真实情况，漏掉它这张表就会是空的。
	k := r.keys[key]
	if k == nil {
		k = &Counters{}
		r.keys[key] = k
	}
	k.add(ok, prompt, completion, cached, ttfb, dur, credit)

	// 分域计数（第 2 步）：只认 cn/global 两个域，空值（未落号/老调用）不进任何域——
	// 宁可让域视图少一点数据，也不把不知道归属的量算进某一域。
	h := now.Unix() / 3600 // 小时桶键（全量与分域共用）
	if rc := r.realmLocked(realm); rc != nil {
		rc.total.add(ok, prompt, completion, cached, ttfb, dur, credit)
		mc := rc.models[model]
		if mc == nil {
			mc = &Counters{}
			rc.models[model] = mc
		}
		mc.add(ok, prompt, completion, cached, ttfb, dur, credit)
		kc := rc.keys[key]
		if kc == nil {
			kc = &Counters{}
			rc.keys[key] = kc
		}
		kc.add(ok, prompt, completion, cached, ttfb, dur, credit)
		rb := rc.buckets[h]
		if rb == nil {
			rb = &Bucket{Hour: h}
			rc.buckets[h] = rb
		}
		rb.Requests++
		if prompt >= 0 && completion >= 0 {
			rb.Tokens += int64(prompt + completion)
		}
	}

	// 时序桶：只记 token 与请求数，用于画最近 24 小时的消耗柱。
	b := r.buckets[h]
	if b == nil {
		b = &Bucket{Hour: h}
		r.buckets[h] = b
	}
	b.Requests++
	if prompt >= 0 && completion >= 0 {
		b.Tokens += int64(prompt + completion)
	}
	if len(r.buckets) > bucketHours*2 {
		r.trimBucketsLocked(h)
	}
	r.markDirty()
}

// trimBucketsLocked 丢弃窗口外的旧桶。阈值取 window*2 才触发，避免每小时都全量扫。
func (r *Registry) trimBucketsLocked(nowHour int64) {
	cut := nowHour - bucketHours
	for k := range r.buckets {
		if k < cut {
			delete(r.buckets, k)
		}
	}
}

// NoteCredits 记录一次积分采样（余额刷新时调用）。
// remain/used 来自上游 billing 接口；used 为该周期已消耗积分。
//
// 比率估计口径：同一账号两次采样之间，
//
//	Δ积分消耗 / Δ本进程消耗token
//
// 两个 guard：
//   - Δused <= 0 → 跳过（新计费周期重置、或上游数据回退），负增量会污染比率；
//   - Δtokens <= 0 → 跳过（没有流量就没有可归因的消耗）。
//
// **已知偏差（面板需如实标注）**：签到/猫猫旅行/活跃上报等后台任务也会消耗积分，
// 但这些 token 不经 /v1 计数，因此 Δused 会偏大 → 比率偏大 → 预估可用 token 偏小。
// 方向是保守的（宁可低估），可接受。
func (r *Registry) NoteCredits(realm, uid string, used int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev := r.credits[uid]
	var tokens int64
	if a := r.accounts[uid]; a != nil {
		tokens = a.Prompt + a.Completion
	}
	r.credits[uid] = &creditSample{used: used, tokens: tokens}
	if prev == nil {
		return // 首次采样只建立基线，无法做差分
	}
	dUsed := used - prev.used
	dTok := tokens - prev.tokens
	if dUsed > 0 && dTok > 0 {
		r.credUsed += dUsed
		r.credTokens += dTok
		r.credN++
		// 域级同一份差分：账号只属于一个域，不会重复计。域桶不存在时按需建——
		// 该账号可能还没跑过流量（Observe 才会建桶），但余额刷新先到了。
		if rc := r.realmLocked(realm); rc != nil {
			rc.credUsed += dUsed
			rc.credTokens += dTok
			rc.credN++
		}
	}
	r.markDirty()
}

// ExpiryBatch 一批同日失效的积分；与 upstream.ExpiryBatch 同形，避免 metrics 依赖 upstream。
type ExpiryBatch struct {
	At     time.Time
	Remain int64
}

// SetCreditTotals 记录池级积分合计（一轮余额刷新结束后调用一次）。
// 拆成独立方法是因为 NoteCredits 是逐账号并发调用的，池级数字要等全批到齐才知道。
// expiring 为各账号「会过期的积分」批次（可空）：由调用方跨账号合并后传入，
// 传 nil 表示本轮没取到（保留上一轮的值，别把已知的到期信息抹掉）。
func (r *Registry) SetCreditTotals(realm string, remain, used int64, at time.Time, expiring []ExpiryBatch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// realm 非空 → 只写该域的积分口径，池级字段不动（池级由 realm="" 的那次调用单独写）。
	// 一条余额刷新链路里两个调用各写各的，互不干扰。
	if realm == "cn" || realm == "global" {
		if rc := r.realmLocked(realm); rc != nil {
			setCreditState(&rc.credRemain, &rc.credUsedOn, &rc.credAt, &rc.credReady, &rc.credSeries, &rc.credExpiring, remain, used, at, expiring)
		}
		r.markDirty()
		return
	}
	if remain < 0 {
		remain = 0
	}
	setCreditState(&r.credRemain, &r.credUsedOn, &r.credAt, &r.credReady, &r.credSeries, &r.credExpiring, remain, used, at, expiring)
	r.markDirty()
}

// setCreditState 写入一套积分口径（池级与域级共用同一实现）。
//
// 用指针是因为它就是在原地更新一组字段——池级那组在 Registry 上、域级那组在 realmCounters 上。
// 两处各写一遍「写合计 + 追加/滚动采样」的代码迟早漂移出两种口径（面板上「全池」与「某域」的
// 数字对不上，是最费劲的一类排查）。
func setCreditState(remainP, usedOnP *int64, atP *time.Time, readyP *bool,
	seriesP *[]creditPoint, expiringP *[]ExpiryBatch, remain, used int64, at time.Time, expiring []ExpiryBatch) {
	*remainP = remain
	*usedOnP = used
	*atP = at
	*readyP = true
	if expiring != nil {
		*expiringP = expiring
	}
	series := *seriesP
	// 追加余额采样并滚动裁剪。同一时刻重复调用（并发刷新）只保留最后一条。
	if n := len(series); n > 0 && at.Sub(series[n-1].at) < time.Second {
		series[n-1] = creditPoint{at: at, remain: remain}
	} else {
		series = append(series, creditPoint{at: at, remain: remain})
	}
	cut := at.Add(-creditWindow)
	i := 0
	for i < len(series) && series[i].at.Before(cut) {
		i++
	}
	if i > 0 {
		series = append([]creditPoint(nil), series[i:]...)
	}
	if len(series) > creditMaxPoints {
		series = append([]creditPoint(nil), series[len(series)-creditMaxPoints:]...)
	}
	*seriesP = series
}

// CreditsView 积分与预估的对外投影。
type CreditsView struct {
	Remain    int64   `json:"remain"`
	Used      int64   `json:"used"`
	Samples   int64   `json:"samples"`
	PerMToken float64 `json:"per_mtoken"` // 实测：每百万 token 消耗多少积分（原口径是每千 token，万分位太难读）
	EstTokens int64   `json:"est_tokens"` // 按实测比率估算剩余积分还能跑多少 token
	Ready     bool    `json:"ready"`      // false = 样本不足，EstTokens 无意义
	AgeSec    int64   `json:"age_sec"`    // 上次余额刷新距今秒数；-1 = 从未刷新

	// 净消耗速率与续航。**这是唯一把签到进账算进去的口径**：
	// 用相邻余额采样算净变化，余额涨了（签到）速率为负，续航自然变长。
	// BurnPerHour 单位：积分/小时。>0 = 在净消耗，<0 = 净增长（签到领得比用得多）。
	BurnPerHour float64 `json:"burn_per_hour"`
	// RunwayHours 剩余积分 ÷ 净消耗速率。仅在 BurnPerHour > 0 时有意义；
	// 净增长（<=0）时没有"用完"这回事，前端应显示"净增长"。
	RunwayHours float64 `json:"runway_hours"`
	// BurnSpanSec 参与速率计算的实测跨度秒数。为 0 = 样本不足以出速率。
	BurnSpanSec int64 `json:"burn_span_sec"`
	// BurnNet 窗口内余额净变化（负数=净消耗）。用于如实展示"这段时间到底净进还是净出"。
	BurnNet int64 `json:"burn_net"`

	// ExpiryBatchView / ExpiringBatches 描述「会过期的积分」。
	//
	// 为什么必须露出来：积分不是永久的。签到/领取发的「裂变包」一个月失效，长期包能到很远
	// 未来。而 Remain 是把两者加在一起的（这是上游余额的真实口径），所以
	// 「Remain ÷ 日消耗 = 还能用多久」会**系统性偏乐观**——用不完的部分到期直接蒸发，
	// 那笔损失在只看 Remain 时完全看不见。
	//
	// **为什么给的是列表而不是"最近一天"**：实测同一批积分分散在多个日期到期，且金额
	// 极不均（10-11 只有 1,600，10-13 却有 35,500）。只报最近一天会让人以为风险很小——
	// 一个报错的数字比没有数字更糟，因为它让人不再警惕。所以整列如实给出去。
	ExpiringBatches []ExpiryBatchView `json:"expiring_batches"`
	// ExpiringDays 为最近一个失效日距今天数（-1 = 没有会过期的批次，全是长期额度）。
	ExpiringDays int `json:"expiring_days"`

	// Series 池余额采样序列（时间升序），供面板画「余额 24h 走势线」。
	//
	// credSeries 本来就一直在存（每轮余额刷新追加一个点），此前只被用来算首尾
	// 净速率，曲线本身没透出——而「签到脉冲一下涨一截、随后缓慢回落」的形状
	// 正是余额图表里一眼能读出来的信息，标量字段表达不了。
	//
	// 点数做 stride 降采样（见 seriesMax）：这个接口前端每 5 秒轮询一次，
	// 512 点全量约 15KB/次纯属浪费；192 点画一条走势线绰绰有余。
	Series []CreditPointView `json:"series,omitempty"`

	// DailyNet 按本地自然日汇总的余额净变化（时间升序，最多 7 天）。
	// 正 = 当天净进账（签到/任务领的比花的多），负 = 净消耗。
	// 面板把它与审计流水的当日 credit 消耗相加，得到「当天获取的积分」：
	//   获取 = 净变化 + 消耗
	// 这是唯一能覆盖签到、任务、抽奖等一切进账的口径——那些进账不走 /v1，
	// 任何按 token/审计的统计都看不见它们。
	// 第一天是窗口切片（不足 24h）的净值，如实给出，前端不假装它是整天。
	DailyNet []DayNetView `json:"daily_net,omitempty"`
}

// DayNetView 一个本地自然日的余额净变化。At 为该日 00:00 的 Unix 秒（本地时区）。
type DayNetView struct {
	At   int64 `json:"at"`
	Net  int64 `json:"net"`  // 正 = 净进账；负 = 净消耗
	From int64 `json:"from"` // 该日窗口首点余额（首个没有前点的日子为 0）
	To   int64 `json:"to"`   // 该日窗口末点余额
}

// dailyNetView 把 credSeries 按本地日切窗求净变化。调用方持有锁；纯函数可测。
// 切分规则：某天的首点 = 上一天末点的后继（不重复计差），跨日边界的差值
// 归入后一天——「23:59→00:05 的变化」发生在跨越之后。
func dailyNetView(pts []creditPoint, now time.Time) []DayNetView {
	if len(pts) == 0 {
		return nil
	}
	loc := time.Local
	out := []DayNetView{}
	cur := DayNetView{At: time.Date(pts[0].at.Year(), pts[0].at.Month(), pts[0].at.Day(), 0, 0, 0, 0, loc).Unix(), From: pts[0].remain}
	cur.To = pts[0].remain
	for i := 1; i < len(pts); i++ {
		p, prev := pts[i], pts[i-1]
		day := time.Date(p.at.Year(), p.at.Month(), p.at.Day(), 0, 0, 0, 0, loc)
		if !day.Equal(time.Unix(cur.At, 0).In(loc)) {
			out = append(out, cur)
			cur = DayNetView{At: day.Unix(), From: prev.remain}
		}
		cur.To = p.remain
	}
	out = append(out, cur)
	// 只保留最近 14 天（按今天 00:00 往前数）。
	today0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).Unix()
	cut := today0 - 13*86400
	i := 0
	for i < len(out) && out[i].At < cut {
		i++
	}
	out = out[i:]
	for i := range out {
		out[i].Net = out[i].To - out[i].From
	}
	return out
}

// CreditPointView 余额序列的一个采样点。
//
// At 用 Unix 秒（前端 new Date(at*1000)），Remain 为该时刻池内剩余积分合计。
type CreditPointView struct {
	At     int64 `json:"at"`
	Remain int64 `json:"remain"`
}

// seriesMax 透出的序列点数上限；超过时按均匀 stride 抽样，首尾点必保留
// （首尾差就是净变化，丢掉任何一个 BurnPerHour 的口径就对不上了）。
const seriesMax = 192

// creditSeriesView 把内部序列转成对外投影并降采样。调用方持有锁；纯函数便于单测。
func creditSeriesView(pts []creditPoint) []CreditPointView {
	if len(pts) == 0 {
		return nil
	}
	if len(pts) <= seriesMax {
		out := make([]CreditPointView, len(pts))
		for i, p := range pts {
			out[i] = CreditPointView{At: p.at.Unix(), Remain: p.remain}
		}
		return out
	}
	out := make([]CreditPointView, 0, seriesMax)
	stride := float64(len(pts)-1) / float64(seriesMax-1)
	for i := 0; i < seriesMax; i++ {
		p := pts[int(math.Round(float64(i)*stride))]
		if len(out) > 0 && out[len(out)-1].At == p.at.Unix() {
			continue // 相邻两次取到同一点：去重，别画出重复点
		}
		out = append(out, CreditPointView{At: p.at.Unix(), Remain: p.remain})
	}
	return out
}

// ExpiryBatchView 一个失效日及其金额。
type ExpiryBatchView struct {
	At     string `json:"at"`     // 具体日期 "2026-10-13"
	Days   int    `json:"days"`   // 距今天数
	Remain int64  `json:"remain"` // 这天失效的积分合计
}

// Snapshot 输出仪表盘所需的全部数据（按当前时刻算天数类字段）。
func (r *Registry) Snapshot() Snapshot { return r.SnapshotAt(time.Now()) }

// SnapshotAt 按指定时刻取快照。
//
// 为什么把时刻做成入参：`ExpiringDays` 这类字段是「距某日还有几天」，
// 必须相对某个时刻算。原来直接在函数里调 `time.Now()`，于是测试注入的固定
// 时间根本不起作用——`TestExpiringCreditsReported` 会在**每天 12:00 UTC 之后
// 的十二个小时里稳定失败**（实测期望 26 天、实得 25：25 天 22 小时被 int() 截断）。
// 时间走参数，测试才能真正确定；生产走 Snapshot() 包装，行为一字不变。
func (r *Registry) SnapshotAt(now time.Time) Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	s := Snapshot{
		Started:   r.started,
		UptimeSec: int64(now.Sub(r.started).Seconds()),
		Totals:    r.total.CountersView(),
		Models:    make([]EntryView, 0, len(r.models)),
		Accounts:  make([]EntryView, 0, len(r.accounts)),
		Keys:      make([]EntryView, 0, len(r.keys)),
	}
	if s.UptimeSec < 0 {
		s.UptimeSec = 0
	}
	for k, c := range r.models {
		s.Models = append(s.Models, EntryView{Key: k, CountersView: c.CountersView()})
	}
	for k, c := range r.accounts {
		s.Accounts = append(s.Accounts, EntryView{Key: k, CountersView: c.CountersView()})
	}
	for k, c := range r.keys {
		s.Keys = append(s.Keys, EntryView{Key: k, CountersView: c.CountersView()})
	}
	// 降序：面板首屏就是「谁最费 token」。同 token 时按请求数、再按键稳定排序，
	// 避免 map 遍历顺序导致每次刷新表格跳动。
	sortEntryViews(s.Models)
	sortEntryViews(s.Accounts)
	sortEntryViews(s.Keys)

	// 时序桶：补全窗口内缺失的小时（前端不用自己补空洞），并裁剪窗口外。
	nowHour := now.Unix() / 3600
	s.Buckets = make([]Bucket, 0, bucketHours)
	for i := bucketHours - 1; i >= 0; i-- {
		h := nowHour - int64(i)
		if b := r.buckets[h]; b != nil {
			s.Buckets = append(s.Buckets, *b)
		} else {
			s.Buckets = append(s.Buckets, Bucket{Hour: h})
		}
	}

	// 分域视图（第 2 步）：与全量同形，供面板的域开关整块替换。只含分域之后产生的量；
	// RealmSince 让前端能如实标注"此前数据不分域"，而不是把历史全量当成某一域的数字。
	s.ByRealm = make(map[string]RealmView, len(r.realms))
	for realm, rc := range r.realms {
		rv := RealmView{
			Totals: rc.total.CountersView(),
			Models: make([]EntryView, 0, len(rc.models)),
			Keys:   make([]EntryView, 0, len(rc.keys)),
		}
		for k, c := range rc.models {
			rv.Models = append(rv.Models, EntryView{Key: k, CountersView: c.CountersView()})
		}
		for k, c := range rc.keys {
			rv.Keys = append(rv.Keys, EntryView{Key: k, CountersView: c.CountersView()})
		}
		sortEntryViews(rv.Models)
		sortEntryViews(rv.Keys)
		rv.Credits = creditsViewOf(rc.credRemain, rc.credUsedOn, rc.credN, rc.credUsed, rc.credTokens,
			rc.credAt, rc.credReady, rc.credSeries, rc.credExpiring, now)
		rv.Buckets = make([]Bucket, 0, bucketHours)
		for i := bucketHours - 1; i >= 0; i-- {
			h := nowHour - int64(i)
			if b := rc.buckets[h]; b != nil {
				rv.Buckets = append(rv.Buckets, *b)
			} else {
				rv.Buckets = append(rv.Buckets, Bucket{Hour: h})
			}
		}
		s.ByRealm[realm] = rv
	}
	s.RealmSince = r.realmSince

	s.Credits = creditsViewOf(r.credRemain, r.credUsedOn, r.credN, r.credUsed, r.credTokens, r.credAt, r.credReady, r.credSeries, r.credExpiring, now)

	return s
}

// Snapshot 是对外 JSON 结构。
type Snapshot struct {
	Started   time.Time    `json:"started"`
	UptimeSec int64        `json:"uptime_sec"`
	Totals    CountersView `json:"totals"`
	Models    []EntryView  `json:"models"`
	Accounts  []EntryView  `json:"accounts"`
	// Keys 按密钥指纹聚合；Key 是指纹，展示名由面板层映射（metrics 不认识密钥名）。
	Keys    []EntryView `json:"keys"`
	Buckets []Bucket    `json:"buckets"`
	Credits CreditsView `json:"credits"`

	// ByRealm 分域视图（第 2 步）：面板的国服/国际服开关切到某一域时整块替换上面的
	// Totals/Models/Keys/Buckets（字段同形）。缺失（后端未升级/域无流量）时前端回落全量。
	ByRealm map[string]RealmView `json:"by_realm,omitempty"`
	// RealmSince 分域统计的起点：此前的历史累计没有域维度，只在全量口径里。面板据此
	// 如实标注"分域统计自 X 起"，不把历史全量摊进某一域。
	RealmSince time.Time `json:"realm_since,omitempty"`
}

// creditsViewOf 由一套积分口径的状态构造对外投影。**池级与域级共用同一实现**：两套算法
// 分开写迟早漂移（面板上"全池比率"和"某域比率"用不同公式算，用户永远对不上）。
func creditsViewOf(remain, usedOn, credN, credUsed, credTokens int64, at time.Time, ready bool,
	series []creditPoint, expiring []ExpiryBatch, now time.Time) CreditsView {
	cv := CreditsView{Remain: remain, Used: usedOn, Samples: credN, Ready: ready, AgeSec: -1}
	if !at.IsZero() {
		cv.AgeSec = int64(now.Sub(at).Seconds())
		if cv.AgeSec < 0 {
			cv.AgeSec = 0
		}
	}
	if credTokens > 0 {
		per := float64(credUsed) / (float64(credTokens) / 1e6)
		// 保留 6 位，不能用 round2：小额费率场景这个比率仍可能落到千分位。
		//
		// 真实数值：326 积分 / 336,245,157 token ≈ 0.97 积分/百万token（旧口径 0.0011/千token）。
		// round2 在缓存命中率极高的环境仍可能把它舍成 0，前端判据 `per_mtoken > 0` 随之为假，
		// 两张推算卡都显示「—」——而 samples 早就有 182 个。用户看到的是一句「待累积样本」，
		// 实际是等多久都不会自己出数的死状态。
		cv.PerMToken = round6(per)
		if per > 0 {
			// 用未舍入的 per 外推：预估值不该因为显示层的精度而漂移。
			cv.EstTokens = int64(float64(cv.Remain) / per * 1e6)
		}
	}
	// 净消耗速率：用窗口首尾两条采样算净变化，除以真实时间跨度。
	// 这样签到发的积分会体现为负消耗（速率变小甚至为负），而不是被忽略。
	if n := len(series); n >= 2 {
		first, last := series[0], series[n-1]
		span := last.at.Sub(first.at)
		if span >= creditMinSpan {
			net := first.remain - last.remain // 正数 = 余额减少 = 净消耗
			cv.BurnSpanSec = int64(span.Seconds())
			cv.BurnNet = net
			cv.BurnPerHour = round2(float64(net) / span.Hours())
			if cv.BurnPerHour > 0 {
				cv.RunwayHours = round2(float64(cv.Remain) / cv.BurnPerHour)
			}
		}
	}
	// 到期信息：整列如实给出，每个失效日一行。
	//
	// 不再只报"最近一天"：实测同一批积分分散在多个日期到期且金额极不均
	// （10-11 仅 1,600，10-13 却有 35,500），只报第一天会让人误判风险很小。
	// 天数钳到 ≥0：已过期的包上游不会再返回，真出现就按"今天"算，不显示负数。
	cv.ExpiringDays = -1
	for i, b := range expiring {
		d := int(b.At.Sub(now).Hours() / 24)
		if d < 0 {
			d = 0
		}
		cv.ExpiringBatches = append(cv.ExpiringBatches, ExpiryBatchView{
			At:     b.At.Format("2006-01-02"),
			Days:   d,
			Remain: b.Remain,
		})
		if i == 0 {
			cv.ExpiringDays = d
		}
	}
	cv.Series = creditSeriesView(series)
	cv.DailyNet = dailyNetView(series, now)
	return cv
}

// sortEntryViews 按 token 总量降序，同量按请求数降序，再按键升序保证确定性。
func sortEntryViews(v []EntryView) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].TotalTokens != v[j].TotalTokens {
			return v[i].TotalTokens > v[j].TotalTokens
		}
		if v[i].Requests != v[j].Requests {
			return v[i].Requests > v[j].Requests
		}
		return v[i].Key < v[j].Key
	})
}

func ms(d time.Duration) float64 { return round1(float64(d.Microseconds()) / 1000) }

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// round6 给「积分/千token」这类天然落在万分位及以下的比率用。
//
// 为什么不能沿用 round2：精度不够时会把非零比率舍成 0，而 0 在这套代码里是
// 「没有样本」的哨兵值（见 CreditsView.Ready 与前端 rateKnown 判据），
// 于是「算出来了但值很小」会被误读成「还没算出来」。
func round6(f float64) float64 { return float64(int64(f*1e6+0.5)) / 1e6 }
