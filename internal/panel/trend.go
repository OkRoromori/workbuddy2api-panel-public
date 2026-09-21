package panel

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/audit"
)

// trend 仪表盘「近 N 天用量趋势」：把审计流水按天聚合成一条时间线。
//
// 为什么独立成端点而不是塞进 stats：stats 是 5 秒一轮的内存快照，而趋势要
// 重读最多 7 个 JSONL 文件（今天可能几千行），两者量级差太远。趋势数据本身
// 变化也慢（一天一个点），没必要跟着 5 秒轮询走——前端按 60 秒节流拉取，
// 服务端再垫一层 30 秒缓存，两道闸保证磁盘 IO 不会被轮询放大。
//
// 查询参数：days=7（默认 7，钳到 2..retention——审计默认保留 14 天，
// 要更久也没有数据，宁可少给也不假装）。
//
// 数据口径与「请求审计」页完全一致（同一份 JSONL、同一个 Summarize），
// 两边数字对不上就是 bug。文件缺失的日期返回零值行而不是跳过：
// 趋势图缺一个点会出现断线，而「那天没用过」本身就是有效数据。
type trendDay struct {
	Date       string  `json:"date"` // YYYY-MM-DD（本地时区）
	Requests   int     `json:"requests"`
	OK         int     `json:"ok"`
	Failed     int     `json:"failed"`
	Prompt     int64   `json:"prompt"`
	Completion int64   `json:"completion"`
	Total      int64   `json:"total"`
	Cached     int64   `json:"cached"`
	Reasoning  int64   `json:"reasoning"`
	Credit     float64 `json:"credit"`
}

// trendTTL 聚合结果缓存时长。跨过它才真正重读文件；手动「刷新」不走缓存
// （force 参数清缓存），保证「点了刷新数字就该动」的直觉成立。
const trendTTL = 30 * time.Second

type trendResult struct {
	At    time.Time  `json:"-"` // 缓存写入时刻
	Days  []trendDay `json:"days"`
	Since string     `json:"since"` // 窗口起始日（含）
	Until string     `json:"until"` // 窗口结束日（含，通常=今天）
}

// buildTrend 真正读文件聚合。调用方需持有 cache.mu。
func (p *Panel) buildTrend(n int, realm string) *trendResult {
	// 域过滤（面板顶部的国服/国际服开关）：与请求审计页共用同一实现，口径必然一致。
	realmOf := map[string]string{}
	if realm == "cn" || realm == "global" {
		realmOf = p.realmOfAccounts()
	}
	today := time.Now()
	days := make([]trendDay, 0, n)
	for i := n - 1; i >= 0; i-- {
		d := today.AddDate(0, 0, -i)
		date := d.Format("2006-01-02")
		var row trendDay
		row.Date = date
		if p.cfg.Audit != nil {
			res, err := p.cfg.Audit.ReadDay(date)
			if err == nil {
				recs := filterByRealm(res.Records, realmOf, realm)
				sum, _, _ := audit.Summarize(recs)
				row.Requests = sum.Requests
				row.OK = sum.OK
				row.Failed = sum.Failed
				row.Prompt = int64(sum.Prompt)
				row.Completion = int64(sum.Completion)
				row.Total = int64(sum.Total)
				row.Cached = int64(sum.Cached)
				row.Reasoning = int64(sum.Reasoning)
				row.Credit = sum.Credit
			}
			// 读文件失败也按零值给这一天：趋势线缺点是看得见的（图形断掉），
			// 而 error 会让整个图表消失——前者更容易被发现和定位。
		}
		days = append(days, row)
	}
	return &trendResult{
		At:    time.Now(),
		Days:  days,
		Since: days[0].Date,
		Until: days[len(days)-1].Date,
	}
}

func (p *Panel) trend(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Audit == nil {
		writeErr(w, http.StatusNotImplemented, "审计未启用：服务启动时未注入 audit.Recorder")
		return
	}
	n := 7
	if raw := r.URL.Query().Get("days"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			n = v
		}
	}
	if n < 2 {
		n = 2
	}
	if max := p.cfg.Audit.Retention(); n > max {
		n = max
	}

	realm := normalizeRealm(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("realm"))))
	force := r.URL.Query().Get("force") == "1"
	p.trendCacheMu.Lock()
	defer p.trendCacheMu.Unlock()
	if !force && p.trendCacheData.hasData && p.trendCacheN == n && p.trendCacheRealm == realm &&
		time.Since(p.trendCacheData.res.At) < trendTTL {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "trend": p.trendCacheData.res})
		return
	}
	res := p.buildTrend(n, realm)
	p.trendCacheData = trendCacheVal{res: res, hasData: true}
	p.trendCacheN = n
	p.trendCacheRealm = realm
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "trend": res})
}
