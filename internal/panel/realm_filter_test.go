package panel

import (
	"testing"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/audit"
)

// TestFilterByRealm 审计流水按域过滤（三处页面共用的口径）。
//
// 这条规则必须在审计明细、近 N 天趋势、密钥用量三处完全一致——它们显示的是同一批请求的不同
// 切面，"合计对得上、明细对不上"是最难查的一类问题，所以实现只有这一份。
func TestFilterByRealm(t *testing.T) {
	realmOf := map[string]string{"aaaaaaaa": "cn", "bbbbbbbb": "global"}
	rec := func(acct string) audit.Record { return audit.Record{Account: acct, Status: 200, HasUsage: true} }
	recs := []audit.Record{
		rec("aaaaaaaa"), // 国服号
		rec("bbbbbbbb"), // 国际服号
		rec("cccccccc"), // 已从池中移除的号（映射表里没有）
		rec(""),         // 没落到账号（例如全池冷却）
	}

	t.Run("全部：原样返回（历史不丢）", func(t *testing.T) {
		got := filterByRealm(recs, realmOf, "")
		if len(got) != 4 {
			t.Errorf("全部口径应保留 4 条，实际 %d", len(got))
		}
	})
	t.Run("国服：只有国服号那些行", func(t *testing.T) {
		got := filterByRealm(recs, realmOf, "cn")
		if len(got) != 1 || got[0].Account != "aaaaaaaa" {
			t.Errorf("国服口径 = %+v want 只有 aaaaaaaa", got)
		}
	})
	t.Run("国际服：只有国际服号那些行", func(t *testing.T) {
		got := filterByRealm(recs, realmOf, "global")
		if len(got) != 1 || got[0].Account != "bbbbbbbb" {
			t.Errorf("国际服口径 = %+v want 只有 bbbbbbbb", got)
		}
	})
	t.Run("已移除的号与未落号的行不进任何域", func(t *testing.T) {
		cn := filterByRealm(recs, realmOf, "cn")
		gl := filterByRealm(recs, realmOf, "global")
		if len(cn)+len(gl) != 2 {
			t.Errorf("两域合计 = %d want 2（已移除/未落号的不该被算进任何域）", len(cn)+len(gl))
		}
	})
	t.Run("非法域值等于不过滤", func(t *testing.T) {
		if got := filterByRealm(recs, realmOf, "bogus"); len(got) != 4 {
			t.Errorf("非法域值应原样返回 4 条，实际 %d", len(got))
		}
	})
	t.Run("空输入不 panic", func(t *testing.T) {
		if got := filterByRealm(nil, nil, "cn"); len(got) != 0 {
			t.Errorf("空输入应得空结果，实际 %+v", got)
		}
	})
}

// TestNormalizeRealm 查询参数归一：只认 cn/global，其余（含大写、空白、空）一律 "" = 全部。
func TestNormalizeRealm(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"cn", "cn"}, {"global", "global"},
		{"", ""}, {"CN", ""}, {" all ", ""}, {"bogus", ""},
	} {
		if got := normalizeRealm(c.in); got != c.want {
			t.Errorf("normalizeRealm(%q) = %q want %q", c.in, got, c.want)
		}
	}
}
