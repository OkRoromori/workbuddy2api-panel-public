// expiring.go 积分到期批次查询：本地面板「积分到期」卡片的数据源（v1.11 基座补齐）。
//
// **字段名坑（重要，别按名字猜）**：请求参数叫 PackageEndTimeRangeBegin/End，
// 让人以为响应里有 PackageEndTime——实际没有；响应里的 ExpiredTime 也永远是空串。
// 真正的失效时刻是 DeductionEndTime（epoch 毫秒），即"这个包什么时候不能再花"。
//
// v1.11 侧用 CycleEndTime 判快过期；实测两者都有效但语义不同：
//   - CycleEndTime 是周期边界（额度按周期重置），字符串墙钟；
//   - DeductionEndTime 是可抵扣窗口结束，epoch 毫秒——这才是"用不完就没了"的那一刻。
//
// 本文件以 DeductionEndTime 为准（更准且能算出具体日期），保留 CycleEndTime 作为兜底。
package upstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/auth"
)

// ExpiryBatch 一批同日失效的积分。只统计 remain>0 的包——没余额的包到期没有任何影响。
type ExpiryBatch struct {
	At     time.Time
	Remain int64
}

// UserResourcePackages 在余额之外返回「会过期的积分」按失效日聚合的结果（按日期升序）。
//
// 为什么要它：积分不是永久的。签到/领取发的「裂变包」一个月就失效，而长期包能到很远的
// 未来。不区分的话，「剩余积分 ÷ 日消耗 = 还能用多久」会**系统性偏乐观**——用不完的部分
// 到期直接蒸发，而那笔损失在旧口径里完全看不见。
func (c *Client) UserResourcePackages(a *auth.Auth) (remain, used int64, expiring []ExpiryBatch, err error) {
	now := time.Now()
	body := map[string]any{
		"PageNumber":               1,
		"PageSize":                 100,
		"ProductCode":              "p_tcaca",
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": now.Format(packageEndLayout),
		"PackageEndTimeRangeEnd":   now.Add(365 * 101 * 24 * time.Hour).Format(packageEndLayout),
	}
	data, err := c.billingMeterJSON(a, c.billingMeterPaths(a), http.MethodPost, body)
	if err != nil {
		return 0, 0, nil, err
	}
	var resp struct {
		Response struct {
			Data struct {
				Accounts []struct {
					PackageName         string `json:"PackageName"`
					CapacitySize        int64  `json:"CapacitySize"`
					CapacityRemain      int64  `json:"CapacityRemain"`
					CapacityUsed        int64  `json:"CapacityUsed"`
					CycleCapacitySize   int64  `json:"CycleCapacitySize"`
					CycleCapacityRemain int64  `json:"CycleCapacityRemain"`
					CycleCapacityUsed   int64  `json:"CycleCapacityUsed"`
					CycleEndTime        string `json:"CycleEndTime"`     // 周期边界（墙钟），兜底判据
					DeductionEndTime    int64  `json:"DeductionEndTime"` // epoch 毫秒；真正的失效时刻
				} `json:"Accounts"`
			} `json:"Data"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, 0, nil, fmt.Errorf("resource parse: %w", err)
	}
	byDay := map[string]*ExpiryBatch{}
	for _, acct := range resp.Response.Data.Accounts {
		r, u, _ := packageRemainUsed(respAccount{
			CapacityRemain:      acct.CapacityRemain,
			CapacityUsed:        acct.CapacityUsed,
			CapacitySize:        acct.CapacitySize,
			CycleCapacityRemain: acct.CycleCapacityRemain,
			CycleCapacityUsed:   acct.CycleCapacityUsed,
			CycleCapacitySize:   acct.CycleCapacitySize,
		})
		if r < 0 {
			r = 0
		}
		if u < 0 {
			u = 0
		}
		remain += r
		used += u
		if r <= 0 {
			continue
		}
		// 判据优先 DeductionEndTime（epoch 毫秒）；缺失再退 CycleEndTime（墙钟字符串）。
		// 两者都无 → 视为长期包，不计入到期（不误报"快没了"）。
		var at time.Time
		switch {
		case acct.DeductionEndTime > 0:
			at = time.UnixMilli(acct.DeductionEndTime)
		case acct.CycleEndTime != "":
			if t, perr := time.ParseInLocation(packageEndLayout, acct.CycleEndTime, softRateResetLoc); perr == nil {
				at = t
			}
		}
		if at.IsZero() {
			continue
		}
		key := at.Format("2006-01-02")
		if b, ok := byDay[key]; ok {
			b.Remain += r
		} else {
			byDay[key] = &ExpiryBatch{At: at, Remain: r}
		}
	}
	for _, b := range byDay {
		expiring = append(expiring, *b)
	}
	sort.Slice(expiring, func(i, j int) bool { return expiring[i].At.Before(expiring[j].At) })
	return remain, used, expiring, nil
}
