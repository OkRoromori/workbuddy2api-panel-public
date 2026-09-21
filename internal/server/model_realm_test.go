package server

import (
	"testing"
)

// TestResolveModelWithRealm 域解析优先级：显式前缀 > 逐模型 pin > 全局 prefer > 缺省 cn。
//
// 这是「/v1/models 改发裸名」之后的核心契约：裸名的域归属完全由配置决定，
// 而客户端仍可用显式前缀强制指定某域。
func TestResolveModelWithRealm(t *testing.T) {
	cases := []struct {
		name          string
		model         string
		prefer        string
		pins          map[string]string
		wantRealm     string
		wantBare      string
	}{
		{"裸名缺省 → cn（去前缀前的历史语义）", "glm-5.3", "", nil, "cn", "glm-5.3"},
		{"裸名 prefer=global", "glm-5.3", "global", nil, "global", "glm-5.3"},
		{"pin 覆盖全局 prefer（钉 cn）", "glm-5.3", "global", map[string]string{"glm-5.3": "cn"}, "cn", "glm-5.3"},
		{"pin 覆盖全局 prefer（钉 global）", "glm-5.3", "cn", map[string]string{"glm-5.3": "global"}, "global", "glm-5.3"},
		{"显式前缀优先于 pin", "global:glm-5.3", "cn", map[string]string{"glm-5.3": "cn"}, "global", "glm-5.3"},
		{"显式前缀优先于 prefer", "cn:glm-5.3", "global", nil, "cn", "glm-5.3"},
		{"非域前缀按裸名处理（名字里带冒号）", "foo:bar", "global", nil, "global", "foo:bar"},
		{"非域前缀不参与 pin", "foo:bar", "cn", map[string]string{"bar": "global"}, "cn", "foo:bar"},
		{"prefer 非法值回落 cn", "glm-5.3", "bogus", nil, "cn", "glm-5.3"},
		{"pin 值非法视为未命中", "glm-5.3", "global", map[string]string{"glm-5.3": "bogus"}, "global", "glm-5.3"},
		{"前导冒号按裸名", ":glm", "cn", nil, "cn", ":glm"},
		{"空前缀形态 cn:", "cn:", "global", nil, "cn", ""},
		{"空模型名", "", "global", nil, "global", ""},
		{"前缀大小写敏感（CN: 不是域前缀）", "CN:glm-5.3", "cn", nil, "cn", "CN:glm-5.3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			realm, bare := resolveModelWithRealm(c.model, c.prefer, c.pins)
			if realm != c.wantRealm || bare != c.wantBare {
				t.Errorf("resolveModelWithRealm(%q, %q, %v) = (%q, %q) want (%q, %q)",
					c.model, c.prefer, c.pins, realm, bare, c.wantRealm, c.wantBare)
			}
		})
	}
}

// TestModelAllowedIgnoresRealmPrefix 白名单按裸名比对：域前缀只影响路由，不该让
// 「历史里带 cn: 的条目」与「客户端现在发的裸名」互相失配（/v1/models 改发裸名后的兼容点）。
func TestModelAllowedIgnoresRealmPrefix(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		model   string
		want    bool
	}{
		{"空白名单放行全部", nil, "glm-5.3", true},
		{"空模型名拒绝", []string{"glm-5.3"}, "  ", false},
		{"精确命中", []string{"glm-5.3"}, "glm-5.3", true},
		{"白名单带 cn: 前缀，客户端发裸名", []string{"cn:glm-5.3"}, "glm-5.3", true},
		{"白名单裸名，客户端带 cn: 前缀", []string{"glm-5.3"}, "cn:glm-5.3", true},
		{"白名单 global: 前缀，客户端带 cn: 前缀（域不参与白名单语义）", []string{"global:glm-5.3"}, "cn:glm-5.3", true},
		{"白名单条目含空白", []string{" glm-5.3 "}, "glm-5.3", true},
		{"未在白名单内拒绝", []string{"glm-5.2"}, "glm-5.3", false},
		{"非域前缀不误命中", []string{"foo:bar"}, "bar", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := modelAllowed(c.allowed, c.model); got != c.want {
				t.Errorf("modelAllowed(%v, %q) = %v want %v", c.allowed, c.model, got, c.want)
			}
		})
	}
}

// TestAssembleModelList 双域列表合并：裸名去重、重名取优先级选中侧、CN 顺序为主。
func TestAssembleModelList(t *testing.T) {
	newEntry := func(realm, id string) map[string]any {
		return map[string]any{"id": id, "realm": realm}
	}
	build := func() (map[string]map[string]any, map[string]map[string]any, []string, []string) {
		cn := map[string]map[string]any{
			"cn-only": newEntry("cn", "cn-only"),
			"glm-5.3": newEntry("cn", "glm-5.3"),
		}
		global := map[string]map[string]any{
			"glm-5.3":   newEntry("global", "glm-5.3"),
			"gpt-5.5":   newEntry("global", "gpt-5.5"),
		}
		return cn, global, []string{"cn-only", "glm-5.3"}, []string{"glm-5.3", "gpt-5.5"}
	}
	ids := func(list []map[string]any) []string {
		out := make([]string, 0, len(list))
		for _, e := range list {
			out = append(out, e["id"].(string)+"@"+e["realm"].(string))
		}
		return out
	}

	t.Run("缺省 prefer=cn：重名取国服条目", func(t *testing.T) {
		cn, global, co, go_ := build()
		got := ids(assembleModelList(cn, global, co, go_, "cn", nil))
		want := []string{"cn-only@cn", "glm-5.3@cn", "gpt-5.5@global"}
		assertEq(t, got, want)
	})

	t.Run("prefer=global：重名取国际服条目，位置仍按 CN 顺序", func(t *testing.T) {
		cn, global, co, go_ := build()
		got := ids(assembleModelList(cn, global, co, go_, "global", nil))
		want := []string{"cn-only@cn", "glm-5.3@global", "gpt-5.5@global"}
		assertEq(t, got, want)
	})

	t.Run("pin 覆盖 prefer（只钉住重名的那个）", func(t *testing.T) {
		cn, global, co, go_ := build()
		got := ids(assembleModelList(cn, global, co, go_, "cn", map[string]string{"glm-5.3": "global"}))
		want := []string{"cn-only@cn", "glm-5.3@global", "gpt-5.5@global"}
		assertEq(t, got, want)
	})

	t.Run("同一侧元数据不混合：取哪域就是哪域的条目", func(t *testing.T) {
		cn := map[string]map[string]any{"m": {"id": "m", "realm": "cn", "context_length": 1}}
		global := map[string]map[string]any{"m": {"id": "m", "realm": "global", "context_length": 2}}
		got := assembleModelList(cn, global, []string{"m"}, []string{"m"}, "global", nil)
		if len(got) != 1 {
			t.Fatalf("重名应只出一条，实际 %d 条", len(got))
		}
		if got[0]["context_length"] != 2 || got[0]["realm"] != "global" {
			t.Errorf("应取国际服条目，实际 %v", got[0])
		}
	})

	t.Run("无 global 域时只出 CN", func(t *testing.T) {
		cn, _, co, _ := build()
		got := ids(assembleModelList(cn, map[string]map[string]any{}, co, nil, "global", nil))
		assertEq(t, got, []string{"cn-only@cn", "glm-5.3@cn"})
	})

	t.Run("空输入不 panic", func(t *testing.T) {
		if got := assembleModelList(nil, nil, nil, nil, "cn", nil); len(got) != 0 {
			t.Errorf("want empty, got %v", got)
		}
	})
}

func assertEq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("长度不符: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项: got %v want %v", i, got, want)
		}
	}
}
