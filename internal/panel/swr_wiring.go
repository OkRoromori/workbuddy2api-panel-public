package panel

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/audit"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/pool"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/upstream"
)

// initSlowCaches 装配三个慢接口的结果缓存（swr.go）。
//
// TTL 依据：
//   - packages 60s：余额 5 分钟才后台刷一次，包明细变化更慢；「重新查询」按钮
//     走 force 同步绕过缓存（用户显式要最新）。
//   - usage 15s：审计一条条落盘，15s 内的差异肉眼不可见；按日期分桶，切日期互不干扰。
//   - models 120s：上游目录上游侧自己缓存 1 小时，我们 2 分钟对齐一次足够。
func (p *Panel) initSlowCaches() {
	if p.cfg.Upstream != nil {
		p.pkgCache = newSWR(60*time.Second, p.fetchPackages)
		p.modelsCache = newSWR(120*time.Second, p.fetchModels)
	}
	if p.cfg.Audit != nil {
		p.usageCaches = map[string]*swrCache{}
	}
}

// ── 积分构成（原 packages handler 主体原样搬来，只包了缓存） ────────────

func (p *Panel) fetchPackages() (any, error) {
	accts := p.cfg.Pool.List()
	type row struct {
		UID      string                   `json:"uid"`
		Nickname string                   `json:"nickname"`
		Realm    string                   `json:"realm"`
		Remain   int64                    `json:"remain"`
		Size     int64                    `json:"size"`
		Packages []upstream.CreditPackage `json:"packages"`
		Error    string                   `json:"error,omitempty"`
	}
	out := make([]row, len(accts))

	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i, s := range accts {
		wg.Add(1)
		go func(i int, s pool.Status) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			it := row{UID: s.UID, Nickname: s.Nickname, Realm: s.Realm}
			a := p.cfg.Pool.AuthByUID(s.UID)
			if a == nil {
				it.Error = "account not loaded"
				out[i] = it
				return
			}
			packs, remain, size, err := p.cfg.Upstream.CreditPackages(a)
			if err != nil {
				it.Error = err.Error()
				out[i] = it
				return
			}
			it.Packages = packs
			it.Remain = remain
			it.Size = size
			out[i] = it
		}(i, s)
	}
	wg.Wait()

	sort.SliceStable(out, func(i, j int) bool { return out[i].Remain > out[j].Remain })
	return map[string]any{"accounts": out}, nil
}

// packagesCached 立即回缓存值；过期后台刷新；?force=1 同步强制刷新。
func (p *Panel) packagesCached(w http.ResponseWriter, r *http.Request) {
	if p.pkgCache == nil {
		p.packages(w, r) // 未装配上游（测试环境）走原路径
		return
	}
	data, at, cached := p.pkgCache.get(r.URL.Query().Get("force") == "1")
	if data == nil {
		writeErr(w, http.StatusBadGateway, "查询积分包失败（上游不可用），稍后自动重试")
		return
	}
	writeCachedJSON(w, data, at, cached)
}

// ── 模型目录（原 models handler 主体原样搬来，只包了缓存） ──────────────

func (p *Panel) fetchModels() (any, error) {
	out := make([]map[string]any, 0)
	var fetchErrs []string

	if uids := p.cfg.Pool.AvailableUIDsForRealm("cn"); len(uids) > 0 {
		if acct := p.cfg.Pool.AuthByUID(uids[0]); acct != nil {
			infos, err := p.cfg.Upstream.FetchModels(acct)
			if err != nil {
				fetchErrs = append(fetchErrs, "cn: "+err.Error())
			} else {
				for _, mi := range infos {
					out = append(out, panelModelEntry("cn", mi, mi.Efforts, mi.DefaultEffort, p.cfg.Upstream.HTTP))
				}
			}
		}
	}

	if p.cfg.Upstream.GlobalEnabled {
		if uids := p.cfg.Pool.AvailableUIDsForRealm("global"); len(uids) > 0 {
			if acct := p.cfg.Pool.AuthByUID(uids[0]); acct != nil {
				infos := p.cfg.Upstream.FetchGlobalModelInfos(acct)
				if len(infos) == 0 {
					fetchErrs = append(fetchErrs, "global: 上游未返回可用模型")
				} else {
					efforts, defaults := p.cfg.Upstream.GlobalEffortSnapshot()
					for _, mi := range infos {
						out = append(out, panelModelEntry("global", mi, efforts[mi.ID], defaults[mi.ID], p.cfg.Upstream.HTTP))
					}
				}
			}
		}
	}

	if len(out) == 0 {
		if len(fetchErrs) > 0 {
			return nil, &apiError{code: http.StatusBadGateway, msg: "fetch models: " + strings.Join(fetchErrs, "; ")}
		}
		return nil, &apiError{code: http.StatusServiceUnavailable, msg: "没有可用账号：请先在面板添加账号再查询"}
	}
	resp := map[string]any{"ok": true, "models": out}
	if len(fetchErrs) > 0 {
		resp["fetch_errors"] = fetchErrs // 两域有失败但另一域出数了：照常给，附带说明
	}
	return resp, nil
}

func (p *Panel) modelsCached(w http.ResponseWriter, r *http.Request) {
	if p.modelsCache == nil {
		p.models(w, r)
		return
	}
	data, at, cached := p.modelsCache.get(r.URL.Query().Get("force") == "1")
	if data == nil {
		// fetch 失败：缓存层没有可给的。错误语义与原 handler 一致——但具体文案在
		// apiError 里，这里读不出来就直接透传原错误格式。
		writeErr(w, http.StatusBadGateway, "模型目录查询失败（上游不可用），稍后自动重试")
		return
	}
	writeCachedJSON(w, data, at, cached)
}

// ── 请求审计（按日期分桶缓存 ReadDay 的结果；聚合/域过滤每次现算——很便宜） ──

func (p *Panel) usageCached(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Audit == nil {
		writeErr(w, http.StatusNotImplemented, "审计未启用：服务启动时未注入 audit.Recorder")
		return
	}
	day := r.URL.Query().Get("date")
	if day == "" {
		day = audit.Today()
	} else if !audit.ValidDay(day) {
		writeErr(w, http.StatusBadRequest, "date 格式应为 YYYY-MM-DD")
		return
	}

	p.usageMu.Lock()
	c, ok := p.usageCaches[day]
	if !ok {
		c = newSWR(15*time.Second, func() (any, error) {
			return p.cfg.Audit.ReadDay(day)
		})
		p.usageCaches[day] = c
	}
	p.usageMu.Unlock()

	data, at, cached := c.get(r.URL.Query().Get("force") == "1")
	if data == nil {
		writeErr(w, http.StatusInternalServerError, "read audit: 暂不可用，稍后重试")
		return
	}
	res := data.(audit.ReadResult)

	recs := res.Records
	// 域过滤与聚合每次现算（纯内存，微秒级），缓存只包住昂贵的读盘。
	realm := normalizeRealm(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("realm"))))
	recs = filterByRealm(recs, p.realmOfAccounts(), realm)
	sum, byUser, byModel := audit.Summarize(recs)

	rows := make([]audit.Record, 0, len(recs))
	for i := len(recs) - 1; i >= 0 && len(rows) < usageRowsMax; i-- {
		rows = append(rows, recs[i])
	}

	w.Header().Set("X-Data-Time", at.Format(time.RFC3339))
	w.Header().Set("X-Data-Cached", boolHeader(cached))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"date":         day,
		"days":         p.cfg.Audit.Days(),
		"retention":    p.cfg.Audit.Retention(),
		"summary":      sum,
		"ttfb_avg_ms":  sum.TTFBAvgMs(),
		"users":        byUser,
		"models":       byModel,
		"rows":         rows,
		"truncated":    len(recs) > len(rows),
		"dropped":      p.cfg.Audit.Dropped(),
		"write_errors": p.cfg.Audit.WriteErrors(),
		"bad_lines":    res.BadLines,
	})
}

// ── 小件 ──────────────────────────────────────────────────────────────

// writeCachedJSON 写数据 + 两个元信息头（数据时间 / 是否来自缓存）。
// 前端据此显示「数据时间 HH:MM」，让"看到旧的"变成明确信息而不是黑盒。
func writeCachedJSON(w http.ResponseWriter, data any, at time.Time, cached bool) {
	w.Header().Set("X-Data-Time", at.Format(time.RFC3339))
	w.Header().Set("X-Data-Cached", boolHeader(cached))
	writeJSON(w, http.StatusOK, data)
}

func boolHeader(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// apiError 让 fetch 闭包能带状态码冒泡到 handler。
type apiError struct {
	code int
	msg  string
}

func (e *apiError) Error() string { return e.msg }
