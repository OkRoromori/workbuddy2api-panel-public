package metrics

import (
	"path/filepath"
	"testing"
	"time"
)

// TestRealmBucketsAndSnapshot 分域计数（第 2 步）：按 realm 分别累计，未落号的请求**不进任何域**，
// 且全量口径一字不变（只增不改的验证）。
func TestRealmBucketsAndSnapshot(t *testing.T) {
	r := New(time.Now())
	// 国服一条
	r.Observe("m-cn", "cn", "u1", "kA", true, 100, 10, 0, 0, time.Second, 1.5)
	// 国际服一条
	r.Observe("m-gl", "global", "u2", "kA", true, 200, 20, 0, 0, time.Second, 3.0)
	// 未落号（realm 空）：只进全量，不进任何域
	r.Observe("m-x", "", "u3", "kB", true, 300, 30, 0, 0, time.Second, 9.0)
	// 国际服再来一条同模型（验证同域内累加）
	r.Observe("m-gl", "global", "u2", "kB", false, -1, -1, 0, 0, time.Second, 0)

	s := r.Snapshot()

	// —— 全量口径不受影响（含未落号那条）
	if s.Totals.Requests != 4 {
		t.Errorf("全量 requests=%d want 4", s.Totals.Requests)
	}
	if len(s.Models) != 3 {
		t.Errorf("全量模型数=%d want 3（m-cn/m-gl/m-x）", len(s.Models))
	}

	// —— 分域：cn 只有 1 条
	cn, ok := s.ByRealm["cn"]
	if !ok {
		t.Fatal("缺 by_realm[cn]")
	}
	if cn.Totals.Requests != 1 || cn.Totals.Credits != 1.5 {
		t.Errorf("cn 域 = %+v want requests=1 credits=1.5", cn.Totals)
	}
	if len(cn.Models) != 1 || cn.Models[0].Key != "m-cn" {
		t.Errorf("cn 域模型视图 = %+v want 只有 m-cn", cn.Models)
	}

	// —— 分域：global 有 2 条（一成功一失败），同模型累加
	gl, ok := s.ByRealm["global"]
	if !ok {
		t.Fatal("缺 by_realm[global]")
	}
	if gl.Totals.Requests != 2 || gl.Totals.Errors != 1 {
		t.Errorf("global 域 = %+v want requests=2 errors=1", gl.Totals)
	}
	if gl.Totals.Credits != 3.0 {
		t.Errorf("global 域积分 = %v want 3.0", gl.Totals.Credits)
	}
	if len(gl.Models) != 1 || gl.Models[0].Key != "m-gl" || gl.Models[0].Requests != 2 {
		t.Errorf("global 域模型视图 = %+v want m-gl×2", gl.Models)
	}
	// 密钥维度也分域：kA 在两域各 1 次
	if len(gl.Keys) != 2 {
		t.Errorf("global 域密钥数=%d want 2（kA/kB）", len(gl.Keys))
	}

	// —— 「未落号不进任何域」：两域请求数之和(3) < 全量(4)
	if cn.Totals.Requests+gl.Totals.Requests != 3 {
		t.Errorf("两域请求和=%d want 3（未落号那条不该被算进任何域）",
			cn.Totals.Requests+gl.Totals.Requests)
	}

	// —— 时序桶也按域出（各域桶数相同，值只含本域）
	if len(cn.Buckets) == 0 || len(gl.Buckets) == 0 {
		t.Fatalf("分域时序桶为空：cn=%d global=%d", len(cn.Buckets), len(gl.Buckets))
	}
	var cnReq, glReq int64
	for _, b := range cn.Buckets {
		cnReq += b.Requests
	}
	for _, b := range gl.Buckets {
		glReq += b.Requests
	}
	if cnReq != 1 || glReq != 2 {
		t.Errorf("分域时序桶请求数 cn=%d global=%d want 1/2", cnReq, glReq)
	}

	// —— 分域统计起点已记录（面板据此标注"此前数据不分域"）
	if s.RealmSince.IsZero() {
		t.Error("RealmSince 应已设置")
	}
}

// TestRealmPersistRoundTrip 分域计数要能落盘并加载回来（否则重启就丢，域视图会忽然变空）。
func TestRealmPersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	r := New(time.Now())
	r.Observe("m-gl", "global", "u1", "kA", true, 500, 50, 0, 0, time.Second, 4.25)
	r.Open(path)
	r.Flush() // 同步落盘（不起后台 goroutine；面板层的 Save 就是它的包装）

	r2 := New(time.Now())
	r2.Open(path) // Open 内部会 load 已有文件
	s := r2.Snapshot()
	gl, ok := s.ByRealm["global"]
	if !ok {
		t.Fatalf("重启后分域数据丢失：%+v", s.ByRealm)
	}
	if gl.Totals.Requests != 1 || gl.Totals.Credits != 4.25 || gl.Totals.PromptTokens != 500 {
		t.Errorf("重启后 global 域 = %+v want requests=1 credits=4.25 prompt=500", gl.Totals)
	}
	if len(gl.Models) != 1 || gl.Models[0].Key != "m-gl" {
		t.Errorf("重启后 global 域模型视图 = %+v", gl.Models)
	}
	if s.RealmSince.IsZero() {
		t.Error("重启后 RealmSince 丢失")
	}
}

// TestRealmCredits 域级积分口径（第 3 步）：比率按域分别差分（跨域混算得出的是没有意义的
// "平均倍率"），且**不影响池级口径**——两条线各写各的。
func TestRealmCredits(t *testing.T) {
	r := New(time.Now())
	// 两域各一个账号先跑一轮流量（建立 token 基线）
	r.Observe("m", "cn", "u-cn", "k", true, 1000, 0, 0, 0, time.Second, 0)
	r.Observe("m", "global", "u-gl", "k", true, 500, 0, 0, 0, time.Second, 0)
	// 第一次采样：只建基线，不出比率
	r.NoteCredits("cn", "u-cn", 10)
	r.NoteCredits("global", "u-gl", 100)
	// 再跑一轮（制造 Δtoken），然后第二次采样（Δused > 0）→ 出比率
	r.Observe("m", "cn", "u-cn", "k", true, 1000, 0, 0, 0, time.Second, 0)
	r.Observe("m", "global", "u-gl", "k", true, 500, 0, 0, 0, time.Second, 0)
	r.NoteCredits("cn", "u-cn", 12)      // Δused=2 / Δtok=1000 → 2000 积分/百万 token
	r.NoteCredits("global", "u-gl", 110) // Δused=10 / Δtok=500 → 20000 积分/百万 token

	r.SetCreditTotals("cn", 500, 12, time.Now(), nil)
	r.SetCreditTotals("global", 1000, 110, time.Now(), nil)

	s := r.Snapshot()
	cn, gl := s.ByRealm["cn"], s.ByRealm["global"]
	if cn.Credits.Remain != 500 || !cn.Credits.Ready {
		t.Errorf("cn 域积分口径 = %+v want remain=500 ready=true", cn.Credits)
	}
	if cn.Credits.PerMToken != 2000 {
		t.Errorf("cn 域实测比率 = %v want 2000 积分/百万token", cn.Credits.PerMToken)
	}
	if gl.Credits.PerMToken != 20000 {
		t.Errorf("global 域实测比率 = %v want 20000（必须与国服分别算）", gl.Credits.PerMToken)
	}
	// 池级口径这一轮没被写过（只调了域级）→ 仍是未就绪：两条线互不干扰
	if s.Credits.Ready {
		t.Errorf("只写域级时池级不该就绪：%+v", s.Credits)
	}
}

// TestRealmCreditsPersist 域级积分口径也要能落盘回读（否则重启后域视图的"消耗/速率"
// 会从零攒起，与池级历史互相矛盾）。
func TestRealmCreditsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	r := New(time.Now())
	r.SetCreditTotals("global", 888, 77, time.Now(), []ExpiryBatch{{At: time.Now().Add(48 * time.Hour), Remain: 123}})
	r.Open(path)
	r.Flush()

	r2 := New(time.Now())
	r2.Open(path)
	gl := r2.Snapshot().ByRealm["global"]
	if gl.Credits.Remain != 888 || gl.Credits.Used != 77 || !gl.Credits.Ready {
		t.Errorf("重启后域级积分口径 = %+v want remain=888 used=77 ready=true", gl.Credits)
	}
	if len(gl.Credits.ExpiringBatches) != 1 || gl.Credits.ExpiringBatches[0].Remain != 123 {
		t.Errorf("重启后域级到期批次 = %+v want 1 条 123", gl.Credits.ExpiringBatches)
	}
}
