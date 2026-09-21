// Package audit 请求级审计流水：每次 /v1/chat/completions 追加一行 JSON 到
// data/audit/YYYY-MM-DD.jsonl，按天切文件、到期自动清理。
//
// 与 metrics 的分工（为什么不是把字段塞进 metrics）：
//   - metrics 是**累计计数器**，回答"一共花了多少"，重启后靠快照续上，没有明细；
//   - audit 是**逐条流水**，回答"谁在什么时候用了什么模型、慢在哪、失败在哪"，
//     这正是排查故障与区分多用户所必需的信息，而累计值一旦合并就再也拆不开。
//
// 与面板运行日志（Ring）的分工：Ring 是内存里 500 行的滚动窗口，重启即空，
// 且只有 stdout 那一列文本；audit 落盘 14 天，字段结构化，可供筛选与统计。
//
// 写入是**异步尽力而为**的：请求路径只做一次非阻塞投递，队列满就丢弃并计数，
// 绝不让磁盘 IO 拖慢或阻塞转发。审计丢几条可以接受，卡住请求不可以。
package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultRetentionDays 默认保留天数（含今天，即最多 14 个文件）。
// 从 7 天放宽：仪表盘「近 14 天积分流水」要按天聚合审计流水，窗口比流水长
// 就会画出「后半段全是零」的假象——窗口不能超过数据源。
const DefaultRetentionDays = 14

// writeQueueCap 异步写入队列容量。正常情况下请求速率远低于此，
// 只有在磁盘卡死时才会积压，届时丢新记录而不是拖住请求。
const writeQueueCap = 1024

// dayLayout 单日文件名与日期参数的格式。
const dayLayout = "2006-01-02"

// Today 返回本地时区的今天（YYYY-MM-DD）。面板要把它回显给用户，
// 所以日期格式由本包统一提供，避免两处各写一份布局字符串。
func Today() string { return time.Now().Format(dayLayout) }

// ValidDay 校验 YYYY-MM-DD。
func ValidDay(day string) bool {
	_, err := time.ParseInLocation(dayLayout, day, time.Local)
	return err == nil
}

// Record 一条请求流水。
//
// 字段全部带 omitempty（除 ts/status/total_ms）：JSONL 是给人看的，也是给
// 磁盘省地方的，零值字段不必占字节。has_usage 显式保留，因为它为 false 时
// 恰恰是最重要的信号（上游没给 usage），不能被省略掉。
type Record struct {
	TS   time.Time `json:"ts"`
	User string    `json:"user,omitempty"` // 密钥对应的名字（区分你我）
	// KeyID 密钥指纹（httpauth.Fingerprint）。
	//
	// 为什么名字之外还要存它：User 是**写入当时的名字快照**，用户改了密钥名之后，
	// 按名字做的统计会把历史用量整个丢掉——表现是"改个名，这把密钥就变成没人用过"，
	// 而"还有没有人在用"恰恰是删密钥前唯一该问的问题。指纹不随改名变化。
	KeyID      string `json:"key_id,omitempty"`
	Model      string `json:"model,omitempty"`
	Account    string `json:"account,omitempty"` // uid 前 8 位
	Status     int    `json:"status"`
	Mode       string `json:"mode,omitempty"` // stream / sync
	HasUsage   bool   `json:"has_usage"`      // false = 200 但上游没给 usage（异常信号）
	Prompt     int    `json:"prompt,omitempty"`
	Completion int    `json:"completion,omitempty"`
	Cached     int    `json:"cached,omitempty"`
	// Reasoning 推理（思维链）token，上游 usage.completion_thinking_tokens。
	// 单列出来是因为思考型模型里它可能占输出的大头——并进「输出」会让人以为
	// 产出很多，实际都花在思考上、用户看不到。
	Reasoning int `json:"reasoning,omitempty"`
	// Credit 本次消耗的积分，**上游 usage.credit 原值**（浮点，实测可为 0.01）。
	// 刻意不放大成整数存放：上游给多少就记多少，"不编数字"是这个面板的底线。
	Credit  float64 `json:"credit,omitempty"`
	TTFBMs  int64   `json:"ttfb_ms,omitempty"` // 0 = 未观测到（同步请求没有首字节语义）
	TotalMs int64   `json:"total_ms"`
	Client  string  `json:"client,omitempty"` // 客户端 User-Agent，截断保存，排查用
}

// maxClientLen User-Agent 存入流水的最大长度。UA 可能很长且带版本噪声，
// 截断后仍足以区分"哪个客户端"。
const maxClientLen = 72

// Recorder 异步 JSONL 追加器。零值不可用，须经 New 构造。
// nil *Recorder 是合法的"不审计"，所有方法都对 nil 安全。
type Recorder struct {
	dir       string
	retention int

	ch      chan Record
	quit    chan struct{}
	done    chan struct{}
	closing sync.Once

	dropped  atomic.Int64
	writeErr atomic.Int64
}

// New 在 dir 下创建审计目录并启动后台写入协程，同时清理过期文件。
// retentionDays <= 0 回落到 DefaultRetentionDays。
// dir 为空时返回 nil（调用方据此关闭审计，而不是写进当前目录）。
func New(dir string, retentionDays int) *Recorder {
	if dir == "" {
		return nil
	}
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}
	r := &Recorder{
		dir:       dir,
		retention: retentionDays,
		ch:        make(chan Record, writeQueueCap),
		quit:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	// 目录建不出来（只读盘/权限）不是致命错误：Write 会记 writeErr 并丢弃，
	// 网关照常转发请求。审计是旁路，不该拦停主业务。
	_ = os.MkdirAll(dir, 0o750)
	r.purge()
	go r.loop()
	return r
}

// Write 投递一条记录。非阻塞：队列满或已关闭则丢弃并计数（Dropped 可见）。
func (r *Recorder) Write(rec Record) {
	if r == nil {
		return
	}
	select {
	case <-r.quit:
		return
	default:
	}
	if len(rec.Client) > maxClientLen {
		rec.Client = rec.Client[:maxClientLen]
	}
	select {
	case r.ch <- rec:
	case <-r.quit:
	default:
		r.dropped.Add(1)
	}
}

// Dropped 累计丢弃条数（队列满）。面板透出它，避免"日志缺了却不知道"。
func (r *Recorder) Dropped() int64 {
	if r == nil {
		return 0
	}
	return r.dropped.Load()
}

// WriteErrors 累计写盘失败次数。
func (r *Recorder) WriteErrors() int64 {
	if r == nil {
		return 0
	}
	return r.writeErr.Load()
}

// Retention 保留天数。
func (r *Recorder) Retention() int {
	if r == nil {
		return DefaultRetentionDays
	}
	return r.retention
}

// Dir 审计目录（面板展示用）。
func (r *Recorder) Dir() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// Close 停止后台写入并落盘队列中剩余记录；幂等。
//
// 用 quit 通道而不是 close(ch)：Write 可能与 Close 并发，向已关闭的通道发送
// 会 panic，而审计绝不该把进程打挂。
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.closing.Do(func() {
		close(r.quit)
		<-r.done
	})
}

// loop 后台写入：单协程持有文件句柄，故无需锁。
func (r *Recorder) loop() {
	defer close(r.done)
	var (
		f      *os.File
		curDay string
	)
	closeFile := func() {
		if f != nil {
			_ = f.Sync()
			_ = f.Close()
			f = nil
		}
	}
	defer closeFile()

	write := func(rec Record) {
		day := rec.TS.Local().Format(dayLayout)
		if day != curDay {
			closeFile()
			path := filepath.Join(r.dir, day+".jsonl")
			nf, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
			if err != nil {
				r.writeErr.Add(1)
				return
			}
			f, curDay = nf, day
			// 跨天时顺手清理过期文件：长期不重启的进程也能自动轮转。
			r.purge()
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			r.writeErr.Add(1)
			return
		}
		raw = append(raw, '\n')
		if _, err := f.Write(raw); err != nil {
			r.writeErr.Add(1)
			closeFile() // 句柄坏了，下一条重开
			curDay = ""
		}
	}

	for {
		select {
		case rec := <-r.ch:
			write(rec)
		case <-r.quit:
			// 排空队列再退出（尽力而为：此刻仍有并发 Write 的话，
			// 极少数几条可能留在缓冲里，属可接受的取舍）。
			for {
				select {
				case rec := <-r.ch:
					write(rec)
				default:
					return
				}
			}
		}
	}
}

// purge 删除保留期之外的日志文件。过期判定基于文件名里的日期（而非 mtime），
// 这样"把旧文件拷回来"也会被清掉，语义与保留天数一致。
func (r *Recorder) purge() {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -(r.retention - 1))
	cutoffDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.Local)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		day, err := time.ParseInLocation(dayLayout, strings.TrimSuffix(e.Name(), ".jsonl"), time.Local)
		if err != nil {
			continue // 不是本工具生成的文件名，不碰
		}
		if day.Before(cutoffDay) {
			_ = os.Remove(filepath.Join(r.dir, e.Name()))
		}
	}
}

// Days 返回可读的日期（YYYY-MM-DD），由新到旧。
func (r *Recorder) Days() []string {
	if r == nil {
		return nil
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(name, ".jsonl")
		if _, err := time.ParseInLocation(dayLayout, day, time.Local); err != nil {
			continue
		}
		out = append(out, day)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// ErrBadDay date 参数不是 YYYY-MM-DD。
var ErrBadDay = errors.New("date 格式应为 YYYY-MM-DD")

// ReadResult 读取一天的流水。
type ReadResult struct {
	// Records 解析成功的记录，按写入顺序（时间升序）。
	Records []Record
	// BadLines 无法解析的行数（截断/损坏/被别的工具改坏）。
	//
	// 为什么必须报出来：解析失败以前是静默 continue，于是一份被改坏的文件
	// 在面板上表现成"这天没人用过"——**少给数据却看不出来**是最难查的一类问题。
	// 宁可多一个"有 N 行读不出来"的提示，也不要一个看起来正常的零。
	BadLines int
}

// Read 读取某一天的流水（时间升序）。等价于 ReadDay(...).Records。
// day 为空 = 今天。文件不存在返回空切片而非错误（"今天还没人用过"不是故障）。
func (r *Recorder) Read(day string) ([]Record, error) {
	res, err := r.ReadDay(day)
	return res.Records, err
}

// ReadDay 读取某一天的流水，并附带"读不出来的行数"。
//
// 坏行（进程被 kill 时截断的最后一行）跳过而不报错——一条坏行不该让整天的
// 数据都看不到；但跳过多少条会如实计数，由面板透出。
func (r *Recorder) ReadDay(day string) (ReadResult, error) {
	if r == nil {
		return ReadResult{}, nil
	}
	if day == "" {
		day = time.Now().Format(dayLayout)
	}
	if _, err := time.ParseInLocation(dayLayout, day, time.Local); err != nil {
		return ReadResult{}, ErrBadDay
	}
	f, err := os.Open(filepath.Join(r.dir, day+".jsonl"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ReadResult{Records: []Record{}}, nil
		}
		return ReadResult{}, err
	}
	defer f.Close()

	var res ReadResult
	sc := bufio.NewScanner(f)
	// 单行可能超过默认 64KB 上限（UA 很长时），放宽到 1MB。
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			res.BadLines++
			continue
		}
		res.Records = append(res.Records, rec)
	}
	// Scanner 的错误（含超长行）不向上报：已读到的部分仍然有值；
	// 没读到的部分至少要计数，不能凭空消失。
	if err := sc.Err(); err != nil && res.BadLines == 0 {
		res.BadLines++
	}
	return res, nil
}

// Summary 一天的汇总。
type Summary struct {
	Requests   int     `json:"requests"`
	OK         int     `json:"ok"`
	Failed     int     `json:"failed"`
	Missing    int     `json:"missing"` // 200 但缺 usage
	Prompt     int     `json:"prompt"`
	Completion int     `json:"completion"`
	Cached     int     `json:"cached"`
	Reasoning  int     `json:"reasoning"`
	Total      int     `json:"total"` // prompt + completion
	Credit     float64 `json:"credit"`
	TTFBMaxMs  int64   `json:"ttfb_max_ms"`
	TTFBSumMs  int64   `json:"-"`
	TTFBN      int     `json:"-"`
}

// TTFBAvgMs 平均首字节耗时（只统计观测到的样本）；无样本返回 0。
func (s Summary) TTFBAvgMs() int64 {
	if s.TTFBN == 0 {
		return 0
	}
	return s.TTFBSumMs / int64(s.TTFBN)
}

// Bucket 按某个维度（用户/模型）聚合的一行。
type Bucket struct {
	Name       string  `json:"name"`
	Requests   int     `json:"requests"`
	Failed     int     `json:"failed"`
	Missing    int     `json:"missing"`
	Prompt     int     `json:"prompt"`
	Completion int     `json:"completion"`
	Cached     int     `json:"cached"`
	Reasoning  int     `json:"reasoning"`
	Credit     float64 `json:"credit"`
}

// Summarize 汇总一天的流水，并给出按用户、按模型两个维度的分桶。
//
// 分桶顺序：按 token 总量降序（用量大的排前面），总量相同再按名字，保证
// 同一份数据每次渲染顺序稳定——否则前端每次刷新行序都在跳。
func Summarize(recs []Record) (Summary, []Bucket, []Bucket) {
	var sum Summary
	byUser := map[string]*Bucket{}
	byModel := map[string]*Bucket{}
	for _, r := range recs {
		sum.Requests++
		if r.Status == 200 {
			sum.OK++
		} else {
			sum.Failed++
		}
		if r.Status == 200 && !r.HasUsage {
			sum.Missing++
		}
		sum.Prompt += r.Prompt
		sum.Completion += r.Completion
		sum.Cached += r.Cached
		sum.Reasoning += r.Reasoning
		sum.Credit += r.Credit
		if r.TTFBMs > 0 {
			sum.TTFBSumMs += r.TTFBMs
			sum.TTFBN++
			if r.TTFBMs > sum.TTFBMaxMs {
				sum.TTFBMaxMs = r.TTFBMs
			}
		}
		addBucket(byUser, userLabel(r.User), r)
		addBucket(byModel, modelLabel(r.Model), r)
	}
	sum.Total = sum.Prompt + sum.Completion
	return sum, sortBuckets(byUser), sortBuckets(byModel)
}

// userLabel 用户名的展示回落：老的流水（多密钥之前）没有 user 字段。
func userLabel(u string) string {
	if strings.TrimSpace(u) == "" {
		return "默认"
	}
	return u
}

// modelLabel 模型名的展示回落。
func modelLabel(m string) string {
	if strings.TrimSpace(m) == "" || m == "-" {
		return "未知"
	}
	return m
}

func addBucket(m map[string]*Bucket, name string, r Record) {
	b := m[name]
	if b == nil {
		b = &Bucket{Name: name}
		m[name] = b
	}
	b.Requests++
	if r.Status == 200 {
		if !r.HasUsage {
			b.Missing++
		}
	} else {
		b.Failed++
	}
	b.Prompt += r.Prompt
	b.Completion += r.Completion
	b.Cached += r.Cached
	b.Reasoning += r.Reasoning
	b.Credit += r.Credit
}

// GroupBy 按 keyOf 返回的键把流水分桶；keyOf 返回空串的记录被跳过。
//
// 与 Summarize 的区别：Summarize 的分桶键写死成"用户名/模型名"（给人看的页面用），
// GroupBy 让调用方自己决定按什么归并——密钥页要按**密钥指纹**归并，
// 这样改名不会把历史用量丢掉。
func GroupBy(recs []Record, keyOf func(Record) string) map[string]Bucket {
	m := map[string]*Bucket{}
	for _, r := range recs {
		k := keyOf(r)
		if k == "" {
			continue
		}
		b := m[k]
		if b == nil {
			b = &Bucket{Name: k}
			m[k] = b
		}
		b.Requests++
		if r.Status == 200 {
			if !r.HasUsage {
				b.Missing++
			}
		} else {
			b.Failed++
		}
		b.Prompt += r.Prompt
		b.Completion += r.Completion
		b.Cached += r.Cached
		b.Reasoning += r.Reasoning
		b.Credit += r.Credit
	}
	out := make(map[string]Bucket, len(m))
	for k, b := range m {
		out[k] = *b
	}
	return out
}

// MergeBuckets 把 src 累加进 dst（原地），返回 dst。
// 用来把"按指纹统计的新流水"和"按名字统计的老流水"合成一个数。
func MergeBuckets(dst, src Bucket) Bucket {
	dst.Requests += src.Requests
	dst.Failed += src.Failed
	dst.Missing += src.Missing
	dst.Prompt += src.Prompt
	dst.Completion += src.Completion
	dst.Cached += src.Cached
	dst.Reasoning += src.Reasoning
	dst.Credit += src.Credit
	return dst
}

func sortBuckets(m map[string]*Bucket) []Bucket {	out := make([]Bucket, 0, len(m))
	for _, b := range m {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		ti := out[i].Prompt + out[i].Completion
		tj := out[j].Prompt + out[j].Completion
		if ti != tj {
			return ti > tj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// String 便于日志/调试。
func (r Record) String() string {
	return fmt.Sprintf("%s %s %s %d", r.TS.Format(time.RFC3339), r.User, r.Model, r.Status)
}
