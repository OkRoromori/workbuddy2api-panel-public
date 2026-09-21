package httpauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func req(authz string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	return r
}

func TestVerifyBearer(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		authz string
		want  bool
	}{
		{"空 key 放行（未启用鉴权）", "", "", true},
		{"空 key 也放行任意头", "", "Bearer whatever", true},
		{"正确 key", "sk-abc123", "Bearer sk-abc123", true},
		{"错误 key", "sk-abc123", "Bearer sk-wrong", false},
		{"缺 Authorization 头", "sk-abc123", "", false},
		{"缺 Bearer 前缀", "sk-abc123", "sk-abc123", false},
		{"认证 scheme 大小写不敏感", "sk-abc123", "bearer sk-abc123", true},
		{"多余空格", "sk-abc123", "Bearer  sk-abc123", false},
		{"前缀相同但内容短", "sk-abc123", "Bearer sk-abc12", false},
		{"前缀相同但内容长", "sk-abc123", "Bearer sk-abc1234", false},
		{"key 恰好是前缀", "sk-abc", "Bearer sk-abcdef", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := VerifyBearer(req(c.authz), c.key); got != c.want {
				t.Errorf("VerifyBearer(key=%q, authz=%q) = %v, want %v", c.key, c.authz, got, c.want)
			}
		})
	}
}

// TestVerifyBearerWithoutHeaderStillCompares 缺头路径不应因"提前返回"而暴露形状差异：
// 这里只验证它确实返回 false 且不 panic（常量时间的性质无法用单测断言，靠实现保证）。
func TestVerifyBearerWithoutHeaderStillCompares(t *testing.T) {
	if VerifyBearer(req(""), "any-key") {
		t.Error("missing header must not pass")
	}
}

func TestDigestIsFixedLength(t *testing.T) {
	// 不同长度输入摘要后应等长（这是常量时间比较的前提）
	if len(digest("")) != len(digest("a-much-longer-secret-value")) {
		t.Error("digest length must not depend on input length")
	}
	if len(digest("x")) != 32 {
		t.Errorf("sha256 digest length = %d, want 32", len(digest("x")))
	}
}

func TestVerifyBearerAny(t *testing.T) {
	keys := map[string]string{"sk-owner": "我", "sk-friend": "朋友"}

	cases := []struct {
		name     string
		authz    string
		wantOK   bool
		wantName string
	}{
		{"主密钥", "Bearer sk-owner", true, "我"},
		{"朋友的密钥", "Bearer sk-friend", true, "朋友"},
		{"小写认证 scheme", "bearer sk-friend", true, "朋友"},
		{"错误 key", "Bearer sk-nope", false, ""},
		{"缺 Authorization 头", "", false, ""},
		{"缺 Bearer 前缀", "sk-owner", false, ""},
		{"多余空格", "Bearer  sk-owner", false, ""},
		{"前缀匹配但不完整", "Bearer sk-owne", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name, _, ok := VerifyBearerAny(req(c.authz), keys)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if c.wantOK && c.wantName != "" && name != c.wantName {
				t.Errorf("name = %q, want %q（审计靠这个名字区分用户）", name, c.wantName)
			}
		})
	}
}

// TestVerifyBearerAnyReturnsFingerprint 指纹必须指向"命中的那把密钥"，
// 且与密钥原文无关、改名也不变——密钥页的用量统计全靠它跨改名保持稳定。
func TestVerifyBearerAnyReturnsFingerprint(t *testing.T) {
	keys := map[string]string{"sk-owner": "主号", "sk-visitor": "访客"}

	name, fp, ok := VerifyBearerAny(req("Bearer sk-visitor"), keys)
	if !ok || name != "访客" {
		t.Fatalf("name=%q ok=%v", name, ok)
	}
	if fp != Fingerprint("sk-visitor") {
		t.Errorf("指纹应指向命中的那把密钥：got %q want %q", fp, Fingerprint("sk-visitor"))
	}
	if fp == Fingerprint("sk-owner") {
		t.Error("指纹串到了另一把密钥上")
	}
	if strings.Contains(fp, "sk-visitor") {
		t.Errorf("指纹不能包含密钥原文：%q", fp)
	}

	// 改名不影响指纹——这正是它存在的理由。
	renamed := map[string]string{"sk-owner": "主号", "sk-visitor": "老王"}
	_, fp2, _ := VerifyBearerAny(req("Bearer sk-visitor"), renamed)
	if fp2 != fp {
		t.Errorf("改名后指纹必须不变：%q -> %q", fp, fp2)
	}

	// 未命中时指纹为空。
	if _, fpBad, ok := VerifyBearerAny(req("Bearer sk-nope"), keys); ok || fpBad != "" {
		t.Errorf("未命中应返回空指纹，实际 (%q,%v)", fpBad, ok)
	}
}

// TestFingerprintEmptyKeyIsEmpty "没出示密钥"必须和"出示了某把密钥"区分开。
func TestFingerprintEmptyKeyIsEmpty(t *testing.T) {
	if Fingerprint("") != "" {
		t.Error("空密钥的指纹必须是空串")
	}
	if Fingerprint("sk-a") == Fingerprint("sk-b") {
		t.Error("不同密钥的指纹不该相同")
	}
	if n := len(Fingerprint("sk-a")); n != 12 {
		t.Errorf("指纹应为 12 个十六进制字符，实际 %d", n)
	}
}

// TestVerifyBearerAnyEmptyMapDisablesAuth 空表 = 未启用鉴权，必须放行而不是全拒。
// 方向反了的话，没配密钥的用户会被面板/网关整个锁死。
func TestVerifyBearerAnyEmptyMapDisablesAuth(t *testing.T) {
	for _, keys := range []map[string]string{nil, {}} {
		name, fp, ok := VerifyBearerAny(req(""), keys)
		if !ok {
			t.Errorf("空密钥表应放行（未启用鉴权），keys=%v", keys)
		}
		if name != "" || fp != "" {
			t.Errorf("放行时名字与指纹都应为空，实际 (%q,%q)", name, fp)
		}
	}
}

// TestVerifyBearerAnyUnnamedKeyFallsBack 密钥没配名字时要回落「默认」，
// 否则审计里空名会和"老流水根本没有 user 字段"混为一谈。
func TestVerifyBearerAnyUnnamedKeyFallsBack(t *testing.T) {
	name, fp, ok := VerifyBearerAny(req("Bearer sk-x"), map[string]string{"sk-x": ""})
	if !ok || name != "默认" {
		t.Errorf("无名密钥应回落「默认」，实际 (%q,%v)", name, ok)
	}
	if fp == "" {
		t.Error("无名密钥仍应给出指纹——名字可以空，身份不能空")
	}
}
