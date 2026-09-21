package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/auth"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/livecfg"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/upstream"
)

// TestResolveModelRealmCatalog 目录校准：只有单一域有的模型必须强制那一域（硬），
// 两域都有才按 pin（硬）/prefer（软）走。
//
// 背景（实测 2026-09-20）：/v1/models 改发裸名之后，仅国际服有的模型（gpt-6-astra 等）
// 被 prefer=cn 送进国服，上游回 11102「model [...] service info not found」，客户端只见 503；
// 而显式 global: 前缀调用是 200。所以解析顺序必须是「目录事实 > 配置偏好」。
func TestResolveModelRealmCatalog(t *testing.T) {
	catBothKnown := ModelRealmCatalog{
		CN: []string{"glm-5.3", "deepseek-v4.1-flash"}, Global: []string{"glm-5.3", "gpt-6-astra"},
		CNKnown: true, GlobalKnown: true,
	}
	cases := []struct {
		name      string
		model     string
		prefer    string
		pins      map[string]string
		cat       ModelRealmCatalog
		wantRealm string
		wantSoft  bool
	}{
		{"显式前缀 → 硬指定（不受目录/偏好影响）", "global:glm-5.3", "cn", map[string]string{"glm-5.3": "cn"},
			catBothKnown, "global", false},
		{"只有国际服有 → 强制国际服（压过 prefer=cn）", "gpt-6-astra", "cn", nil, catBothKnown, "global", false},
		{"只有国际服有 → 也压过 pin=cn", "gpt-6-astra", "cn", map[string]string{"gpt-6-astra": "cn"},
			catBothKnown, "global", false},
		{"只有国服有 → 强制国服（压过 prefer=global）", "deepseek-v4.1-flash", "global", nil,
			catBothKnown, "cn", false},
		{"两域都有 + prefer=global → 软（可回落）", "glm-5.3", "global", nil, catBothKnown, "global", true},
		{"两域都有 + prefer=cn → 软", "glm-5.3", "cn", nil, catBothKnown, "cn", true},
		{"两域都有 + pin=global → 硬（用户钉过，不回落）", "glm-5.3", "cn",
			map[string]string{"glm-5.3": "global"}, catBothKnown, "global", false},
		{"目录未知 → 退回偏好（软）", "gpt-6-astra", "cn", nil, ModelRealmCatalog{}, "cn", true},
		{"一侧未知（global 未缓存）→ 不猜，仍按偏好", "gpt-6-astra", "cn", nil,
			ModelRealmCatalog{CN: []string{"glm-5.3"}, CNKnown: true}, "cn", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			realm, bare, soft := resolveModelRealm(c.model, c.prefer, c.pins, c.cat)
			if realm != c.wantRealm || soft != c.wantSoft {
				t.Errorf("resolveModelRealm(%q) = (%q, %q, soft=%v) want (%q, soft=%v)",
					c.model, realm, bare, soft, c.wantRealm, c.wantSoft)
			}
		})
	}
}

// TestOtherRealm 回落目标就是另一域。
func TestOtherRealm(t *testing.T) {
	if got := otherRealm("global"); got != "cn" {
		t.Errorf("otherRealm(global)=%q want cn", got)
	}
	if got := otherRealm("cn"); got != "global" {
		t.Errorf("otherRealm(cn)=%q want global", got)
	}
}

// TestChatRealmFallbackWhenPreferredRealmUnavailable 用户把默认域设成国际服、国际服却
// **没有可用账号**时，请求应回落到国服完成，而不是直接失败。
//
// 构造：池里只有国服账号（所以 global 域挑不到号）；目录两侧都已缓存且都含 glm-5.3
// （两域同名 → 软解析）；prefer=global。断言请求 200 且确实由国服账号完成。
func TestChatRealmFallbackWhenPreferredRealmUnavailable(t *testing.T) {
	// 清 CN 目录缓存，避免串扰
	dynamicModelsCache.Lock()
	dynamicModelsCache.ids = nil
	dynamicModelsCache.fetched = time.Time{}
	dynamicModelsCache.lastFail = time.Time{}
	dynamicModelsCache.Unlock()

	catalog := `{"code":0,"data":{"models":[
		{"id":"glm-5.3","name":"GLM-5.3","maxInputTokens":131072,"maxOutputTokens":8192}
	],"agents":[{"name":"cli","models":["glm-5.3"]}]}}`
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if strings.Contains(authz, "at-global-") {
			return 200, catalog, false // 目录探测（global 侧）走这里
		}
		return 200, sseOK, true // 正常对话
	})
	// 只预置 global 目录缓存（用一次性 global 账号触发探测）；池里不放 global 账号 → 挑不到号。
	// GlobalEnabled 必须显式打开：newFakeUpstream 直接构造 Client，零值等于关闭 global 路由，
	// 探测会被 globalOn 兜底挡掉、目录永远是空的。
	up.GlobalEnabled = true
	if names := up.FetchGlobalModels(&auth.Auth{UID: "g-probe", AccessToken: "at-global-probe", Domain: "www.workbuddy.ai"}); len(names) == 0 {
		t.Fatalf("global 目录探测应拿到 glm-5.3（fake 目录）")
	}
	p := testPoolWith(&auth.Auth{UID: "cn1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{
		Pool:     p,
		Upstream: up,
		Live: livecfg.New(livecfg.Snapshot{
			Keys:             map[string]string{"": ""},
			ModelRealmPrefer: "global", // 默认域：国际服
		}),
		GlobalEnabled: true,
	})
	body := `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("国际服无号时应回落国服、返回 200；实际 code=%d body=%s", rec.Code, rec.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resp not json: %v", err)
	}
	if _, ok := resp["choices"]; !ok {
		t.Errorf("响应缺少 choices: %v", resp)
	}
	// 硬指定（显式 global:）时**不回落**：全球域无号 → 503，这是刻意行为。
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"global:glm-5.3","messages":[{"role":"user","content":"hi"}]}`)))
	if rec2.Code == 200 {
		t.Errorf("显式 global: 指定不该静默回落国服（实际 200）")
	}
	_ = upstream.ModelInfo{}
	_ = http.StatusOK
}
