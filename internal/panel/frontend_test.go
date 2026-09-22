package panel

import (
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppJSSyntax app.js 必须能通过 JS 解析器语法校验。
//
// 为什么需要：app.js 是 go:embed 进二进制的静态资源，Go 编译器不检查其内容——
// 一次对象字面量键名未加引号（Model_chat_GLM5.2 被解析成属性访问 + 数字字面量）
// 就让整个面板白屏，而所有 Go 测试依然全绿。此测试把语法校验前移到 CI。
// 无 node 环境时跳过（不阻塞无 Node 的构建机）。
func TestAppJSSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping JS syntax check")
	}
	path, err := filepath.Abs("app.js")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "--check", path).CombinedOutput()
	if err != nil {
		t.Fatalf("app.js syntax error:\n%s", out)
	}
}

// TestIndexHTMLNoInlineScript index.html 不得含内联 <script> 块：
// 严格 CSP（script-src 'self'）会拦截内联脚本，页面将完全不可用。
// 外链形式 <script src="..."> 允许。
func TestIndexHTMLNoInlineScript(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	rest := body
	for {
		idx := strings.Index(rest, "<script")
		if idx < 0 {
			break
		}
		rest = rest[idx:]
		end := strings.Index(rest, ">")
		if end < 0 {
			break
		}
		tag := rest[:end+1]
		if !strings.Contains(tag, "src=") {
			t.Fatalf("index.html contains inline <script> (blocked by CSP): %s", tag)
		}
		rest = rest[end:]
	}
}

// TestPanelTitleHasNoBrand 钉住「标题位不出现他人品牌」这条契约。
//
// 为什么值得一条测试：WorkBuddy 是腾讯的产品名。在文档/提示语里提一句（「登录一个 WorkBuddy
// 账号」）属于指称性使用，没问题；但占在**面板标题**和**浏览器标签**的位置，读起来就像
// 「这个软件就叫 WorkBuddy」——那是品牌使用。这条很容易在同步上游前端时被无意覆盖回去，
// 所以用测试钉住：标题位只允许出现本分支自己的版本标识。
func TestPanelTitleHasNoBrand(t *testing.T) {
	html := string(indexHTML)

	// 1) <title> 里不许出现品牌名
	if i := strings.Index(html, "<title>"); i >= 0 {
		seg := html[i:]
		if j := strings.Index(seg, "</title>"); j > 0 {
			if strings.Contains(seg[:j], "WorkBuddy") {
				t.Errorf("<title> 里出现了品牌名（品牌位）：%q", seg[:j])
			}
		}
	} else {
		t.Error("index.html 里找不到 <title>")
	}

	// 2) 侧边栏大字标题里不许出现品牌名（app.js 靠 id=brandName 写显示名）
	i := strings.Index(html, `id="brandName"`)
	if i < 0 {
		t.Fatal(`侧边栏标题缺少 id="brandName"（app.js 的 applyAppName 靠它写显示名）`)
	}
	seg := html[i:]
	if j := strings.Index(seg, ">"); j >= 0 {
		seg = seg[j+1:]
		if k := strings.Index(seg, "</div>"); k >= 0 {
			if strings.Contains(seg[:k], "WorkBuddy") {
				t.Errorf("侧边栏大字标题里出现了品牌名：%q", seg[:k])
			}
		}
	}

	// 3) 显示名的单一来源（app.js 的 APP_NAME）里也不许有品牌名
	js := string(appJS)
	k := strings.Index(js, "const APP_NAME =")
	if k < 0 {
		t.Fatal("app.js 缺少 APP_NAME（面板显示名应当只有这一处定义）")
	}
	line := js[k:]
	if e := strings.Index(line, "\n"); e > 0 {
		line = line[:e]
	}
	if strings.Contains(line, "WorkBuddy") {
		t.Errorf("APP_NAME 里出现了品牌名：%q", line)
	}
}
