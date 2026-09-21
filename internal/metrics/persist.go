package metrics

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

var persistFlushInterval = 5 * time.Second

// countersFile 是 Counters 的落盘形态（时长用纳秒，避免 JSON 丢精度）。
type countersFile struct {
	Requests          int64   `json:"requests"`
	OK                int64   `json:"ok"`
	Errors            int64   `json:"errors"`
	Prompt            int64   `json:"prompt"`
	Completion        int64   `json:"completion"`
	Cached            int64   `json:"cached"`
	Credits           float64 `json:"credits,omitempty"` // 旧文件无此字段 → 0，自然兼容
	MissingUsage      int64   `json:"missing_usage"`
	TTFBSumNs         int64   `json:"ttfb_sum_ns"`
	TTFBN             int64   `json:"ttfb_n"`
	DurSumNs          int64   `json:"dur_sum_ns"`
	DurN              int64   `json:"dur_n"`
	CompletionForRate int64   `json:"completion_for_rate"`
	DurForRateNs      int64   `json:"dur_for_rate_ns"`
}

type creditSampleFile struct {
	Used   int64 `json:"used"`
	Tokens int64 `json:"tokens"`
}

// creditPointFile 余额采样点的落盘形态。续航估算完全依赖这段序列，
// 不落盘的话每次重启都要从零重新攒够 creditMinSpan 才有结论。
type creditPointFile struct {
	At     time.Time `json:"at"`
	Remain int64     `json:"remain"`
}

type fileSnapshot struct {
	Started    time.Time                   `json:"started"`
	Total      countersFile                `json:"total"`
	Models     map[string]countersFile     `json:"models"`
	Accounts   map[string]countersFile     `json:"accounts"`
	Keys       map[string]countersFile     `json:"keys,omitempty"`
	Buckets    []Bucket                    `json:"buckets"`
	Credits    map[string]creditSampleFile `json:"credit_samples"`
	CredUsed   int64                       `json:"cred_used"`
	CredTokens int64                       `json:"cred_tokens"`
	CredN      int64                       `json:"cred_n"`
	CredRemain int64                       `json:"cred_remain"`
	CredUsedOn int64                       `json:"cred_used_on"`
	CredAt     time.Time                   `json:"cred_at"`
	CredReady  bool                        `json:"cred_ready"`
	// CreditSeries 余额序列：续航估算的唯一依据，必须落盘。
	CreditSeries []creditPointFile `json:"credit_series,omitempty"`

	// Realms 分域计数（第 2 步）。旧文件缺这一段 → 空表：分域视图从 0 开始，
	// 面板据 RealmSince 标注"分域统计自 X 起"，不把历史全量摊进某一域。
	Realms     map[string]realmFile `json:"realms,omitempty"`
	RealmSince time.Time            `json:"realm_since,omitempty"`
}

// realmFile 单个域的落盘形态（与全量同构，少 accounts / credits 两项——见 Registry.realms 注释）。
type realmFile struct {
	Total   countersFile            `json:"total"`
	Models  map[string]countersFile `json:"models"`
	Keys    map[string]countersFile `json:"keys"`
	Buckets []Bucket                `json:"buckets"`

	// 域级积分口径（第 3 步）：不落盘则重启后域的"消耗/速率"从零攒起（域视图会突然显示
	// 「样本不足」），而池级还有历史——两边的数字会互相矛盾。
	CredRemain   int64             `json:"cred_remain"`
	CredUsedOn   int64             `json:"cred_used_on"`
	CredAt       time.Time         `json:"cred_at"`
	CredReady    bool              `json:"cred_ready"`
	CredSeries   []creditPointFile `json:"cred_series,omitempty"`
	CredExpiring []ExpiryBatch     `json:"cred_expiring,omitempty"`
	CredUsed     int64             `json:"cred_used"`
	CredTokens   int64             `json:"cred_tokens"`
	CredN        int64             `json:"cred_n"`
}

func encodeCounters(c Counters) countersFile {
	return countersFile{
		Requests: c.Requests, OK: c.OK, Errors: c.Errors,
		Prompt: c.Prompt, Completion: c.Completion, Cached: c.Cached,
		Credits:      c.Credits,
		MissingUsage: c.MissingUsage,
		TTFBSumNs:    int64(c.ttfbSum), TTFBN: c.ttfbN,
		DurSumNs: int64(c.durSum), DurN: c.durN,
		CompletionForRate: c.completionForRate, DurForRateNs: int64(c.durForRate),
	}
}

func decodeCounters(f countersFile) Counters {
	return Counters{
		Requests: f.Requests, OK: f.OK, Errors: f.Errors,
		Prompt: f.Prompt, Completion: f.Completion, Cached: f.Cached,
		Credits:      f.Credits,
		MissingUsage: f.MissingUsage,
		ttfbSum:      time.Duration(f.TTFBSumNs), ttfbN: f.TTFBN,
		durSum: time.Duration(f.DurSumNs), durN: f.DurN,
		completionForRate: f.CompletionForRate, durForRate: time.Duration(f.DurForRateNs),
	}
}

// Open 绑定落盘路径：有文件则读回，并启动后台 5s 落盘。空路径 = 不落盘（测试）。
func (r *Registry) Open(path string) {
	if path == "" {
		return
	}
	r.mu.Lock()
	r.path = path
	r.mu.Unlock()
	r.load()
	go r.flusher()
}

func (r *Registry) markDirty() { r.dirty.Store(true) }

func (r *Registry) flusher() {
	t := time.NewTicker(persistFlushInterval)
	defer t.Stop()
	for range t.C {
		r.Flush()
	}
}

// Flush 有变更才写盘。进程退出 / 重启前调用。
// 空快照不会覆盖已经有用量的文件，避免启动瞬间把历史写成 0。
func (r *Registry) Flush() {
	if !r.dirty.Swap(false) {
		return
	}
	r.mu.Lock()
	path := r.path
	if path == "" {
		r.mu.Unlock()
		return
	}
	snap := fileSnapshot{
		Started:  r.started,
		Total:    encodeCounters(r.total),
		Models:   map[string]countersFile{},
		Accounts: map[string]countersFile{},
		Keys:     map[string]countersFile{},
		Buckets:  make([]Bucket, 0, len(r.buckets)),
		Credits:  map[string]creditSampleFile{},
		CredUsed: r.credUsed, CredTokens: r.credTokens, CredN: r.credN,
		CredRemain: r.credRemain, CredUsedOn: r.credUsedOn,
		CredAt: r.credAt, CredReady: r.credReady,
	}
	for k, c := range r.models {
		snap.Models[k] = encodeCounters(*c)
	}
	for k, c := range r.accounts {
		snap.Accounts[k] = encodeCounters(*c)
	}
	for k, c := range r.keys {
		snap.Keys[k] = encodeCounters(*c)
	}
	for _, b := range r.buckets {
		snap.Buckets = append(snap.Buckets, *b)
	}
	for k, s := range r.credits {
		snap.Credits[k] = creditSampleFile{Used: s.used, Tokens: s.tokens}
	}
	// 余额序列：续航估算的唯一依据，必须落盘（否则每次重启都要重新攒样本）。
	if n := len(r.credSeries); n > 0 {
		snap.CreditSeries = make([]creditPointFile, 0, n)
		for _, p := range r.credSeries {
			snap.CreditSeries = append(snap.CreditSeries, creditPointFile{At: p.at, Remain: p.remain})
		}
	}
	// 分域计数（第 2 步）：不落盘的话重启就丢，面板的域视图会忽然变空（实测踩到）。
	if len(r.realms) > 0 {
		snap.Realms = make(map[string]realmFile, len(r.realms))
		for realm, rc := range r.realms {
			rf := realmFile{
				Total:  encodeCounters(rc.total),
				Models: map[string]countersFile{},
				Keys:   map[string]countersFile{},
			}
			for k, c := range rc.models {
				rf.Models[k] = encodeCounters(*c)
			}
			for k, c := range rc.keys {
				rf.Keys[k] = encodeCounters(*c)
			}
			for _, b := range rc.buckets {
				rf.Buckets = append(rf.Buckets, *b)
			}
			rf.CredRemain, rf.CredUsedOn = rc.credRemain, rc.credUsedOn
			rf.CredAt, rf.CredReady = rc.credAt, rc.credReady
			rf.CredUsed, rf.CredTokens, rf.CredN = rc.credUsed, rc.credTokens, rc.credN
			rf.CredExpiring = rc.credExpiring
			for _, p := range rc.credSeries {
				rf.CredSeries = append(rf.CredSeries, creditPointFile{At: p.at, Remain: p.remain})
			}
			snap.Realms[realm] = rf
		}
		snap.RealmSince = r.realmSince
	}
	r.mu.Unlock()

	// 空快照保护：启动瞬间内存里还没有任何请求统计，别拿它把上一份有数据的文件覆盖成 0。
	//
	// 但**余额序列不算「空」**：它是续航估算的唯一依据，且启动后立刻就会有采样。
	// 若把它算进"空"里，重启后只要还没发过 /v1 请求，新采的余额点就会被这条 return
	// 一直挡在内存里、永远写不进磁盘——下次重启就丢。
	if snap.Total.Requests == 0 && len(snap.Models) == 0 && len(snap.CreditSeries) == 0 {
		if old, err := os.ReadFile(path); err == nil {
			var prev fileSnapshot
			if json.Unmarshal(old, &prev) == nil && prev.Total.Requests > 0 {
				r.dirty.Store(true)
				return
			}
		}
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		r.dirty.Store(true)
		return
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("metrics: 落盘失败: %v", err)
		r.dirty.Store(true)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("metrics: 落盘改名失败: %v", err)
		r.dirty.Store(true)
	}
}

func (r *Registry) load() {
	r.mu.Lock()
	path := r.path
	r.mu.Unlock()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var snap fileSnapshot
	if json.Unmarshal(raw, &snap) != nil {
		log.Printf("metrics: %s 无法解析，忽略", path)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !snap.Started.IsZero() {
		r.started = snap.Started
	}
	r.total = decodeCounters(snap.Total)
	r.models = map[string]*Counters{}
	for k, c := range snap.Models {
		cc := decodeCounters(c)
		r.models[k] = &cc
	}
	r.accounts = map[string]*Counters{}
	for k, c := range snap.Accounts {
		cc := decodeCounters(c)
		r.accounts[k] = &cc
	}
	// 旧的 metrics.json 没有 keys 段（该维度后加），map 为 nil → 建成空表即可，
	// 不清空也不报错：下一次请求就会把这一维重新填起来。
	r.keys = map[string]*Counters{}
	for k, c := range snap.Keys {
		cc := decodeCounters(c)
		r.keys[k] = &cc
	}
	r.buckets = map[int64]*Bucket{}
	for i := range snap.Buckets {
		b := snap.Buckets[i]
		// 旧版本按分钟存（键名 minute），换小时粒度后解析不出 hour → 0，直接丢弃。
		// 这些桶最多只影响「最近 24 小时」那根柱，不值得写迁移。
		if b.Hour == 0 {
			continue
		}
		cp := b
		r.buckets[b.Hour] = &cp
	}
	r.credits = map[string]*creditSample{}
	for k, s := range snap.Credits {
		r.credits[k] = &creditSample{used: s.Used, tokens: s.Tokens}
	}
	r.credUsed, r.credTokens, r.credN = snap.CredUsed, snap.CredTokens, snap.CredN
	r.credRemain, r.credUsedOn = snap.CredRemain, snap.CredUsedOn
	r.credAt, r.credReady = snap.CredAt, snap.CredReady
	r.credSeries = r.credSeries[:0]
	for _, p := range snap.CreditSeries {
		if p.At.IsZero() {
			continue // 坏点丢弃：没有时间戳算不出速率
		}
		r.credSeries = append(r.credSeries, creditPoint{at: p.At, remain: p.Remain})
	}
	// 分域计数（第 2 步）：旧文件没有这一段 → 空表（分域视图从 0 开始，面板会标注
	// "分域统计自 X 起"，不把历史全量摊进某一域）。
	r.realms = map[string]*realmCounters{}
	for realm, rf := range snap.Realms {
		if realm != "cn" && realm != "global" {
			continue // 只认两个域：手改文件塞进别的域名一律忽略
		}
		rc := newRealmCounters()
		rc.total = decodeCounters(rf.Total)
		for k, c := range rf.Models {
			cc := decodeCounters(c)
			rc.models[k] = &cc
		}
		for k, c := range rf.Keys {
			cc := decodeCounters(c)
			rc.keys[k] = &cc
		}
		for i := range rf.Buckets {
			b := rf.Buckets[i]
			if b.Hour == 0 {
				continue
			}
			cp := b
			rc.buckets[b.Hour] = &cp
		}
		rc.credRemain, rc.credUsedOn = rf.CredRemain, rf.CredUsedOn
		rc.credAt, rc.credReady = rf.CredAt, rf.CredReady
		rc.credUsed, rc.credTokens, rc.credN = rf.CredUsed, rf.CredTokens, rf.CredN
		rc.credExpiring = rf.CredExpiring
		for _, p := range rf.CredSeries {
			if p.At.IsZero() {
				continue
			}
			rc.credSeries = append(rc.credSeries, creditPoint{at: p.At, remain: p.Remain})
		}
		r.realms[realm] = rc
	}
	r.realmSince = snap.RealmSince
}
