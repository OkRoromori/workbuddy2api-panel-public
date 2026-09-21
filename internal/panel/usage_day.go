package panel

import (
	"net/http"
	"strings"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/audit"
)

// usageRowsMax 单日明细返回条数上限。
//
// 一天几千条请求全塞进 JSON 会让浏览器卡住，而表格本来也没人会翻到第 2000 行。
// 超限时截断并显式告知（truncated），而不是静默少给——"数字对不上"是最难查的。
const usageRowsMax = 2000

// shortUID 审计流水里账号列的形态：uid 前 8 位（与账号页/流水同一口径）。
// 域过滤要把流水行映射回账号所属域，两侧都得用同一个口径。
func shortUID(uid string) string {
	if len(uid) > 8 {
		return uid[:8]
	}
	return uid
}

// usage 使用日志：返回某一天的请求明细 + 汇总 + 按人/按模型分桶。
//
// 数据源是落盘的 JSONL 审计流水（保留 7 天），不是内存里的运行日志环——
// 后者重启即空、且只有一列文本，分不出谁用的、也算不出用量。
//
// 查询参数：
//
//	date=YYYY-MM-DD  留空 = 今天
//
// 明细按时间**倒序**返回（最新在前）：查故障时看的是"刚刚那几条"，
// 倒序省掉一次翻到底的操作。
func (p *Panel) usageDay(w http.ResponseWriter, r *http.Request) {
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

	res, err := p.cfg.Audit.ReadDay(day)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read audit: "+err.Error())
		return
	}
	recs := res.Records
	// 域过滤（面板顶部域开关）：汇总与明细一起过滤，二者口径必须一致——否则会出现
	// "合计 500 次、明细只有 30 条"这种对不上的画面。
	// 审计行只带 uid 前 8 位，域要从池里映射；号已从池中移除的行在域模式下不显示
	//（它已不属于任何现役域），「全部」模式照常显示（历史不丢）。
	realm := normalizeRealm(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("realm"))))
	recs = filterByRealm(recs, p.realmOfAccounts(), realm)
	sum, byUser, byModel := audit.Summarize(recs)

	rows := make([]audit.Record, 0, len(recs))
	for i := len(recs) - 1; i >= 0 && len(rows) < usageRowsMax; i-- {
		rows = append(rows, recs[i])
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"date":        day,
		"days":        p.cfg.Audit.Days(),
		"retention":   p.cfg.Audit.Retention(),
		"summary":     sum,
		"ttfb_avg_ms": sum.TTFBAvgMs(),
		"users":       byUser,
		"models":      byModel,
		"rows":        rows,
		"truncated":   len(recs) > len(rows),
		// 丢过多少条要透出来：审计是尽力而为的，静默缺失比缺失本身更糟。
		"dropped":      p.cfg.Audit.Dropped(),
		"write_errors": p.cfg.Audit.WriteErrors(),
		// 读不出来的行数：文件被改坏 / 进程被 kill 截断时，这一天的数据是**不完整**的。
		// 前端必须说出来，而不是给一个看起来正常的零。
		"bad_lines": res.BadLines,
	})
}
