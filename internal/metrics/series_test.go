package metrics

import (
	"testing"
	"time"
)

// TestCreditSeriesExposed 余额采样序列必须透出到 Snapshot——面板的「余额 24h
// 走势线」就靠它。此前 credSeries 只用来算首尾净速率，曲线本身没透出。
func TestCreditSeriesExposed(t *testing.T) {
	r := New(time.Now())
	t0 := time.Unix(1_700_000_000, 0)
	r.SetCreditTotals("", 100, 0, t0, nil)
	r.SetCreditTotals("", 250, 0, t0.Add(5*time.Minute), nil) // 签到脉冲
	r.SetCreditTotals("", 240, 0, t0.Add(10*time.Minute), nil)

	s := r.SnapshotAt(t0.Add(15 * time.Minute))
	pts := s.Credits.Series
	if len(pts) != 3 {
		t.Fatalf("want 3 个采样点, got %d", len(pts))
	}
	if pts[0].Remain != 100 || pts[1].Remain != 250 || pts[2].Remain != 240 {
		t.Fatalf("余额值不对: %+v", pts)
	}
	if pts[0].At != t0.Unix() || !(pts[1].At > pts[0].At && pts[2].At > pts[1].At) {
		t.Fatalf("时间戳必须为 Unix 秒且升序: %+v", pts)
	}
}

// TestCreditSeriesDownsample 超过 seriesMax 时按均匀 stride 抽样，
// 首尾两点必须保留（首尾差 = BurnNet 的口径，丢一个就对不上了）。
func TestCreditSeriesDownsample(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	n := seriesMax*2 + 40
	pts := make([]creditPoint, 0, n)
	for i := 0; i < n; i++ {
		pts = append(pts, creditPoint{at: t0.Add(time.Duration(i) * time.Minute), remain: int64(i)})
	}
	out := creditSeriesView(pts)
	if len(out) != seriesMax {
		t.Fatalf("降采样后应恰好 %d 点, got %d", seriesMax, len(out))
	}
	if out[0].Remain != 0 {
		t.Fatalf("首点必须保留: %+v", out[0])
	}
	if out[len(out)-1].Remain != int64(n-1) {
		t.Fatalf("尾点必须保留: got %d want %d", out[len(out)-1].Remain, n-1)
	}
	for i := 1; i < len(out); i++ {
		if out[i].At <= out[i-1].At {
			t.Fatalf("输出必须严格升序且无重复点: [%d]=%d >= [%d]=%d", i, out[i].At, i-1, out[i-1].At)
		}
	}
}

// TestCreditSeriesEmpty 没有任何采样时透出空（omitempty → nil），前端显示空态。
func TestCreditSeriesEmpty(t *testing.T) {
	r := New(time.Now())
	s := r.SnapshotAt(time.Unix(1_700_000_100, 0))
	if len(s.Credits.Series) != 0 {
		t.Fatalf("无采样应无序列, got %d", len(s.Credits.Series))
	}
}

// TestKeyCreditsAggregated 按密钥的积分累计：面板「按密钥」表要回答
// 「哪把密钥花了多少积分」。积分是上游 usage.credit 原值求和，不是按倍率估算。
func TestKeyCreditsAggregated(t *testing.T) {
	r := New(time.Now())
	r.Observe("m1", "cn", "u1", "keyA", true, 100, 10, 0, 0, time.Second, 0.25)
	r.Observe("m1", "cn", "u2", "keyA", true, 100, 10, 0, 0, time.Second, 0.5)
	r.Observe("m1", "cn", "u1", "keyB", true, 100, 10, 0, 0, time.Second, 0.01)
	r.Observe("m1", "cn", "", "keyC", false, -1, -1, 0, 0, time.Second, 0) // 失败无 usage，不累计

	s := r.Snapshot()
	got := map[string]float64{}
	for _, k := range s.Keys {
		got[k.Key] = k.Credits
	}
	if got["keyA"] != 0.75 || got["keyB"] != 0.01 || got["keyC"] != 0 {
		t.Fatalf("按密钥积分累计不对: %v", got)
	}
	if s.Totals.Credits != 0.76 {
		t.Fatalf("全局积分累计不对: %v", s.Totals.Credits)
	}
}

// TestDailyNetByLocalDay daily_net 按本地自然日切窗求净变化：
// 「获取 = 净变化 + 消耗」口径的数据源。跨日差值归后一天，不重复计。
func TestDailyNetByLocalDay(t *testing.T) {
	loc := time.Local
	day := func(d int, h, m int) time.Time {
		base := time.Date(2026, 9, 15, 0, 0, 0, 0, loc)
		return base.AddDate(0, 0, d).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	pts := []creditPoint{
		{at: day(0, 10, 0), remain: 1000}, // 09/15 首点
		{at: day(0, 22, 0), remain: 900},  // 当日净 -100
		{at: day(1, 2, 0), remain: 1500},  // 跨日差 +600 归 09/16；签到脉冲
		{at: day(1, 20, 0), remain: 1200}, // 09/16 净 +200（1500-900-400? -> 见断言）
		{at: day(2, 12, 0), remain: 1100}, // 09/17 净 -100
	}
	now := day(3, 0, 1)
	got := dailyNetView(pts, now)
	if len(got) != 3 {
		t.Fatalf("want 3 天（09/15..09/17）, got %d: %+v", len(got), got)
	}
	if got[0].Net != -100 || got[0].From != 1000 || got[0].To != 900 {
		t.Fatalf("09/15 净值不对: %+v", got[0])
	}
	// 09/16 的 From 是前一天末点 900（不重报 1000），净 = 1200-900 = 300
	if got[1].From != 900 || got[1].To != 1200 || got[1].Net != 300 {
		t.Fatalf("09/16 净值不对: %+v", got[1])
	}
	if got[2].Net != 1100-1200 {
		t.Fatalf("09/17 净值不对: %+v", got[2])
	}
	// At 必须是本地日 00:00
	if got[0].At != day(0, 0, 0).Unix() {
		t.Fatalf("At 应为本地 00:00 Unix 秒: %d vs %d", got[0].At, day(0, 0, 0).Unix())
	}
}

// TestDailyNetEmpty 没采样时透出空。
func TestDailyNetEmpty(t *testing.T) {
	r := New(time.Now())
	s := r.SnapshotAt(time.Now())
	if len(s.Credits.DailyNet) != 0 {
		t.Fatalf("无采样应为空, got %d", len(s.Credits.DailyNet))
	}
}
