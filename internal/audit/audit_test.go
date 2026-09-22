package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// day 造一个"今天往回 n 天"的时间。
func day(n int) time.Time { return time.Now().AddDate(0, 0, -n) }

func TestWriteAndReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, DefaultRetentionDays)
	defer r.Close()

	rec := Record{
		TS: time.Now(), User: "我", Model: "deepseek-v4-flash", Account: "cb1a8f38",
		Status: 200, Mode: "stream", HasUsage: true,
		Prompt: 470767, Completion: 1070, Cached: 455000, TTFBMs: 3607, TotalMs: 8560,
		Client: "curl/8.5.0",
	}
	r.Write(rec)
	r.Close() // 等队列落盘

	got, err := r.Read("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("读回 %d 条，期望 1 条", len(got))
	}
	g := got[0]
	if g.User != "我" || g.Model != rec.Model || g.Prompt != rec.Prompt ||
		g.Cached != rec.Cached || g.TTFBMs != rec.TTFBMs || !g.HasUsage {
		t.Errorf("往返丢字段：%+v", g)
	}
}

// TestWriteOmitsEmptyFields JSONL 里零值字段应被省略（省磁盘，也更易读），
// 但 has_usage=false 必须**写出来**——它是最重要的异常信号，不能被 omitempty 吃掉。
func TestWriteOmitsEmptyFields(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, DefaultRetentionDays)
	r.Write(Record{TS: time.Now(), Status: 200, TotalMs: 12})
	r.Close()

	raw, err := os.ReadFile(filepath.Join(dir, time.Now().Format(dayLayout)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("落盘的不是合法 JSON：%v", err)
	}
	if _, ok := m["prompt"]; ok {
		t.Error("prompt=0 不该出现（omitempty 失效）")
	}
	if v, ok := m["has_usage"]; !ok || v != false {
		t.Errorf("has_usage 必须显式为 false，实际 %v (存在=%v)", v, ok)
	}
	if _, ok := m["client"]; ok {
		t.Error("空 client 不该出现")
	}
	if v := m["total_ms"]; v != float64(12) {
		t.Errorf("total_ms 应保留 0 值以外的真实值，实际 %v", v)
	}
}

// TestCloseDrainsQueue Close 必须把队列里已投递的记录落盘，否则"最后几条没了"。
func TestCloseDrainsQueue(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, DefaultRetentionDays)
	const n = 200
	for i := 0; i < n; i++ {
		r.Write(Record{TS: time.Now(), Status: 200, Completion: i})
	}
	r.Close()
	r.Close() // 幂等，不得 panic

	got, _ := r.Read("")
	if len(got) != n {
		t.Fatalf("Close 后读到 %d 条，期望 %d 条（队列未排空）", len(got), n)
	}
}

// TestWriteAfterCloseIsSafe 关闭后继续写不得 panic（quit 通道方案的核心保证）。
func TestWriteAfterCloseIsSafe(t *testing.T) {
	r := New(t.TempDir(), DefaultRetentionDays)
	r.Close()
	r.Write(Record{TS: time.Now(), Status: 200}) // 不得 panic
}

// TestNilRecorderIsSafe nil Recorder 是合法的"关闭审计"，所有方法都要安全。
func TestNilRecorderIsSafe(t *testing.T) {
	var r *Recorder
	r.Write(Record{Status: 200})
	r.Close()
	if got := r.Days(); got != nil {
		t.Errorf("nil.Days() 应为 nil，实际 %v", got)
	}
	if got, err := r.Read(""); got != nil || err != nil {
		t.Errorf("nil.Read() 应为 (nil,nil)，实际 (%v,%v)", got, err)
	}
	if r.Dropped() != 0 || r.Retention() != DefaultRetentionDays || r.Dir() != "" {
		t.Error("nil 的各种读取应回落到零值")
	}
}

// TestNewEmptyDirDisablesAudit dir 为空 → nil（不审计），而不是写到当前目录。
func TestNewEmptyDirDisablesAudit(t *testing.T) {
	if r := New("", 7); r != nil {
		t.Fatal("dir 为空应返回 nil（关闭审计）")
	}
}

// TestPurgeDropsExpiredFiles 保留 7 天 = 今天 + 前 6 天，第 7 天前的文件被删。
func TestPurgeDropsExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	// 预置 9 个文件：今天、前 1..8 天。
	for i := 0; i <= 8; i++ {
		p := filepath.Join(dir, day(i).Format(dayLayout)+".jsonl")
		if err := os.WriteFile(p, []byte("{}\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	// 非本工具的文件名不得被误删。
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("hi"), 0o640); err != nil {
		t.Fatal(err)
	}

	r := New(dir, 7)
	defer r.Close()

	days := r.Days()
	if len(days) != 7 {
		t.Fatalf("保留 7 天应剩 7 个文件，实际 %d 个：%v", len(days), days)
	}
	if days[0] != time.Now().Format(dayLayout) {
		t.Errorf("Days() 应由新到旧，首个是 %s", days[0])
	}
	if days[len(days)-1] != day(6).Format(dayLayout) {
		t.Errorf("最老的一天应是 %s，实际 %s", day(6).Format(dayLayout), days[len(days)-1])
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("非 .jsonl 文件被误删：%v", err)
	}
}

// TestReadBadDayRejected 非法日期格式要报错，而不是静默当成今天。
func TestReadBadDayRejected(t *testing.T) {
	r := New(t.TempDir(), 7)
	defer r.Close()
	for _, bad := range []string{"2026-9-1", "today", "../../etc/passwd", "2026-13-01"} {
		if _, err := r.Read(bad); err == nil {
			t.Errorf("Read(%q) 应报错", bad)
		}
	}
}

// TestReadMissingDayIsEmpty 「那天没人用过」不是故障：返回空切片、无错误。
func TestReadMissingDayIsEmpty(t *testing.T) {
	r := New(t.TempDir(), 7)
	defer r.Close()
	got, err := r.Read(day(3).Format(dayLayout))
	if err != nil {
		t.Fatalf("缺失的日期不该报错：%v", err)
	}
	if len(got) != 0 {
		t.Fatalf("期望空切片，实际 %d 条", len(got))
	}
}

// TestReadSkipsCorruptLines 进程被 kill 时最后一行会被截断；一条坏行不该
// 让整天的数据都看不到。
func TestReadSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, 7)
	defer r.Close()

	today := time.Now().Format(dayLayout)
	body := `{"ts":"2026-09-14T10:00:00+08:00","status":200,"total_ms":10}` + "\n" +
		`{"ts":"2026-09-14T10:00:01+08:00","status":200,"tot` + "\n" +
		`{"ts":"2026-09-14T10:00:02+08:00","status":500,"total_ms":20}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, today+".jsonl"), []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := r.Read("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("坏行应被跳过、其余保留，实际读到 %d 条", len(got))
	}
	if got[1].Status != 500 {
		t.Errorf("第二条应是 500，实际 %d", got[1].Status)
	}
}

// TestReadDayCountsBadLines 读不出来的行必须**计数**，不能静默消失。
//
// 起因是一次真实踩坑：手工造的 fixture 被工具写成了"多条记录拼在同一行"，
// 于是面板显示"这天没有记录"——文件明明有 850 字节。零值看起来完全正常，
// 这种"少给数据却看不出来"的状态比报错危险得多。
func TestReadDayCountsBadLines(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, 7)
	defer r.Close()

	ok1 := `{"ts":"2026-09-14T10:00:00+08:00","status":200,"total_ms":10}`
	ok2 := `{"ts":"2026-09-14T10:00:02+08:00","status":500,"total_ms":20}`
	body := ok1 + "\n" + "不是 JSON 的一行\n" + ok1 + " " + ok2 + "\n" + ok2 + "\n"
	if err := os.WriteFile(filepath.Join(dir, time.Now().Format(dayLayout)+".jsonl"), []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}

	res, err := r.ReadDay("")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("应解析出 2 条，实际 %d", len(res.Records))
	}
	// 两条坏行：纯文本行 + 两条记录被拼在同一行。
	if res.BadLines != 2 {
		t.Errorf("坏行应计为 2，实际 %d（静默丢弃会让面板显示成「这天没人用过」）", res.BadLines)
	}
	// Read 与 ReadDay 必须同一口径。
	recs, err := r.Read("")
	if err != nil || len(recs) != len(res.Records) {
		t.Errorf("Read 与 ReadDay 不一致：%d vs %d (err=%v)", len(recs), len(res.Records), err)
	}
}

// TestReadDayBlankLinesNotCounted 纯空白行不算坏行（文件末尾多个换行很常见）。
func TestReadDayBlankLinesNotCounted(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, 7)
	defer r.Close()
	body := "\n{\"ts\":\"2026-09-14T10:00:00+08:00\",\"status\":200,\"total_ms\":10}\n\n   \n"
	if err := os.WriteFile(filepath.Join(dir, time.Now().Format(dayLayout)+".jsonl"), []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	res, err := r.ReadDay("")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 || res.BadLines != 0 {
		t.Errorf("空白行不该算坏行：records=%d bad=%d", len(res.Records), res.BadLines)
	}
}

// TestSummarize 汇总口径：失败、缺 usage、按用户/模型分桶、TTFB 只统计观测到的样本。
func TestSummarize(t *testing.T) {
	recs := []Record{
		{User: "我", Model: "a", Status: 200, HasUsage: true, Prompt: 100, Completion: 10, Cached: 80, TTFBMs: 1000, TotalMs: 2000},
		{User: "我", Model: "a", Status: 200, HasUsage: true, Prompt: 200, Completion: 20, TTFBMs: 3000, TotalMs: 4000},
		{User: "我", Model: "b", Status: 200, HasUsage: false},                        // 200 但缺 usage
		{User: "第二台设备", Model: "b", Status: 503, TotalMs: 50},                        // 失败
		{User: "", Model: "", Status: 200, HasUsage: true, Prompt: 1, Completion: 1}, // 老流水无 user/model
	}
	sum, byUser, byModel := Summarize(recs)

	if sum.Requests != 5 || sum.OK != 4 || sum.Failed != 1 {
		t.Errorf("请求/成功/失败 = %d/%d/%d，期望 5/4/1", sum.Requests, sum.OK, sum.Failed)
	}
	if sum.Missing != 1 {
		t.Errorf("缺 usage 应为 1（只有 200 且无 usage 才算），实际 %d", sum.Missing)
	}
	if sum.Prompt != 301 || sum.Completion != 31 || sum.Total != 332 {
		t.Errorf("token 汇总 = %d/%d/%d，期望 301/31/332", sum.Prompt, sum.Completion, sum.Total)
	}
	if sum.Cached != 80 {
		t.Errorf("缓存 token 应为 80，实际 %d", sum.Cached)
	}
	if sum.TTFBMaxMs != 3000 {
		t.Errorf("TTFB 峰值应为 3000，实际 %d", sum.TTFBMaxMs)
	}
	if got := sum.TTFBAvgMs(); got != 2000 {
		t.Errorf("TTFB 均值应为 (1000+3000)/2=2000，实际 %d", got)
	}
	if got := (Summary{}).TTFBAvgMs(); got != 0 {
		t.Errorf("无样本时均值应为 0（不除零），实际 %d", got)
	}

	// 分桶：用量大的在前，回落名次按名字稳定。
	if len(byUser) != 3 {
		t.Fatalf("应有 3 个用户桶，实际 %d：%+v", len(byUser), byUser)
	}
	if byUser[0].Name != "我" || byUser[0].Requests != 3 {
		t.Errorf("用户桶首位应是「我」3 次，实际 %+v", byUser[0])
	}
	names := map[string]bool{}
	for _, b := range byUser {
		names[b.Name] = true
	}
	if !names["默认"] || !names["第二台设备"] {
		t.Errorf("空用户名应回落「默认」，实际桶名 %v", names)
	}

	if len(byModel) != 3 {
		t.Fatalf("应有 3 个模型桶，实际 %d：%+v", len(byModel), byModel)
	}
	// a: 330 tok；未知: 2 tok；b: 0 tok → 顺序 a / 未知 / b
	if byModel[0].Name != "a" || byModel[1].Name != "未知" || byModel[2].Name != "b" {
		t.Fatalf("模型桶应按 token 降序（a/未知/b），实际 %+v", byModel)
	}
	if byModel[2].Failed != 1 || byModel[2].Missing != 1 || byModel[2].Requests != 2 {
		t.Errorf("b 桶应为 2 请求 / 1 失败 / 1 缺 usage，实际 %+v", byModel[2])
	}
	if byModel[0].Missing != 0 || byModel[0].Failed != 0 {
		t.Errorf("a 桶不该有失败或缺 usage：%+v", byModel[0])
	}
}

// TestClientTruncated 超长 User-Agent 要截断，避免单行撑爆。
func TestClientTruncated(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, DefaultRetentionDays)
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	r.Write(Record{TS: time.Now(), Status: 200, Client: string(long)})
	r.Close()

	got, _ := r.Read("")
	if len(got) != 1 {
		t.Fatalf("应读到 1 条，实际 %d", len(got))
	}
	if len(got[0].Client) != maxClientLen {
		t.Errorf("client 应截断到 %d，实际 %d", maxClientLen, len(got[0].Client))
	}
}
