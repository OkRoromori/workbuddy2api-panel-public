package upstream

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/auth"
)

// TestCreditPackagesEndTimePrecedence 钉住「积分构成」逐包的到期时间字段口径。
//
// 背景（用户实测反馈「到期列恒为 —，只看得见发放时间」）：上游响应里 ExpiredTime
// 恒为空串、PackageEndTime 不存在——请求参数名 PackageEndTimeRangeBegin/End 是误导
// （详见 expiring.go 头注释）。真正的到期时刻是 DeductionEndTime（epoch 毫秒）>
// CycleEndTime（墙钟串）> 旧字段兜底。旧实现只读 ExpiredTime/PackageEndTime，
// 故 EndTime 恒空。
func TestCreditPackagesEndTimePrecedence(t *testing.T) {
	ded := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"msg":"OK","data":{"Response":{"Data":{"Accounts":[` +
			// 1) DeductionEndTime 与 CycleEndTime 同时有值 → 取 DeductionEndTime
			`{"PackageName":"A","CapacitySize":100,"CapacityRemain":100,"CapacityUsed":0,` +
			`"DeductionEndTime":` + strconv.FormatInt(ded.UnixMilli(), 10) +
			`,"CycleEndTime":"2026-10-01 00:00:00"},` +
			// 2) 只有 CycleEndTime → 原样透出（墙钟串）
			`{"PackageName":"B","CapacitySize":100,"CapacityRemain":100,"CapacityUsed":0,` +
			`"CycleEndTime":"2026-11-05 12:00:00"},` +
			// 3) 只有旧字段 ExpiredTime（实测恒空，防御性保留最后兜底）
			`{"PackageName":"C","CapacitySize":100,"CapacityRemain":100,"CapacityUsed":0,` +
			`"ExpiredTime":"2026-12-01"},` +
			// 4) 全空 = 长期包 → EndTime 留空（前端显示 —），不伪造日期
			`{"PackageName":"D","CapacitySize":100,"CapacityRemain":100,"CapacityUsed":0}` +
			`]}}}}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BillingBaseCN: srv.URL}
	packs, _, _, err := c.CreditPackages(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	if err != nil {
		t.Fatalf("CreditPackages: %v", err)
	}
	if len(packs) != 4 {
		t.Fatalf("packs=%d want 4", len(packs))
	}
	// 1) DeductionEndTime 优先：按时刻比较（实现按本机时区渲染 RFC3339，日期口径
	// 与面板 CreatedAt 一致，测试不绑死时区）。
	if got, perr := time.Parse(time.RFC3339, packs[0].EndTime); perr != nil || !got.Equal(ded) {
		t.Errorf("packs[0](%s).EndTime=%q want instant %s (err=%v)", packs[0].Name, packs[0].EndTime, ded, perr)
	}
	// 2) 只有 CycleEndTime：同样按时刻渲染 RFC3339（UTC+8 墙钟口径）。
	wantB := time.Date(2026, 11, 5, 12, 0, 0, 0, softRateResetLoc)
	if got, perr := time.Parse(time.RFC3339, packs[1].EndTime); perr != nil || !got.Equal(wantB) {
		t.Errorf("packs[1](%s).EndTime=%q want instant %s (err=%v)", packs[1].Name, packs[1].EndTime, wantB, perr)
	}
	// 3) 只有 ExpiredTime 且形态不是 packageEndLayout（缺时分秒）→ 解析失败，保留原串兜底。
	if packs[2].EndTime != "2026-12-01" {
		t.Errorf("packs[2](%s).EndTime=%q want %q", packs[2].Name, packs[2].EndTime, "2026-12-01")
	}
	// 4) 全空 = 长期包 → 留空（前端显示 —），不伪造日期。
	if packs[3].EndTime != "" {
		t.Errorf("packs[3](%s).EndTime=%q want empty", packs[3].Name, packs[3].EndTime)
	}
}
