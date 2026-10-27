package panel

import (
	"bytes"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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

// TestAppJSTopLevelSmoke app.js 顶层求值冒烟（每个落地视图各跑一遍，issue 同源
// 自上游）：用 Proxy 惰性 DOM 桩把整份 app.js 真跑一次——这类崩溃（TDZ、未定义
// 标识符、首屏渲染时的类型错误）会让面板整页白屏，而 node --check 与 Go 编译都
// 照样全绿。无 node 环境时跳过。
func TestAppJSTopLevelSmoke(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; JS smoke skipped")
	}
	harness := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const inert = new Proxy(function () {}, {
  get(t, k) { if (k === Symbol.toPrimitive) return () => ''; return inert; },
  set() { return true; },
  apply() { return inert; },
  construct() { return inert; },
  has() { return true; },
});
const sandbox = new Proxy({
  location: { hash: process.env.SMOKE_HASH || '#dashboard' },
  history: { replaceState() {} },
  localStorage: { getItem: () => null, setItem() {} },
  navigator: { clipboard: { writeText: () => Promise.resolve() } },
  document: { querySelectorAll: () => [], querySelector: () => inert, getElementById: () => inert, addEventListener() {}, documentElement: inert, head: inert, body: inert, createElement: () => inert, cookie: '' },
  fetch: () => new Promise(() => {}),
  addEventListener() {}, removeEventListener() {},
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  setInterval, clearInterval, setTimeout, clearTimeout,
  console, JSON, Math, Date, Number, String, Boolean, Object, Array, Promise, Map, Set, RegExp, Error, TypeError, isNaN, parseInt, parseFloat, encodeURIComponent, decodeURIComponent, URL, Symbol, Proxy, Reflect,
}, { get(t, k) { return t[k]; }, has() { return true; } });
sandbox.window = sandbox; sandbox.globalThis = sandbox;
vm.createContext(sandbox);
try {
  vm.runInContext(src, sandbox, { filename: 'app.js' });
  console.log('SMOKE OK');
  process.exit(0);
} catch (e) {
  console.log('SMOKE FAIL:', (e && e.stack ? e.stack : e).toString().split('\n').slice(0, 5).join('\n'));
  process.exit(1);
}
`
	hf, err := os.CreateTemp(t.TempDir(), "smoke-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hf.WriteString(harness); err != nil {
		t.Fatal(err)
	}
	hf.Close()
	for _, hash := range []string{"#dashboard", "#accounts", "#usage", "#models", "#config", "#logs", "#packages", "#keys", "#checkin", "#taskcenter"} {
		cmd := exec.Command(node, hf.Name(), "app.js")
		cmd.Dir = "."
		cmd.Env = append(os.Environ(), "SMOKE_HASH="+hash)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("app.js 顶层求值 %s 崩溃: %v\n%s", hash, err, out)
		}
		if !bytes.Contains(out, []byte("SMOKE OK")) {
			t.Fatalf("app.js smoke %s 未通过:\n%s", hash, out)
		}
	}
}

// TestAppJSCollectConfigClearable 覆盖型配置字段的清空语义：user_agent / prompt_file
// 空值必须**照发空串**（后端据此清掉覆盖），其余字段空值不下发（沿用现值）。
// 这是"改配置清不掉旧值"那类问题的回归网。
func TestAppJSCollectConfigClearable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; collectConfig test skipped")
	}
	script := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const start = src.indexOf('const CFG_MAP');
const end = src.indexOf("$('btnEye')");
if (start < 0 || end < 0 || end < start) throw new Error('collectConfig region not found');
const mk = v => ({ type: 'text', value: v });
const cfgForm = { elements: {
  listen: mk(''),
  api_key: mk('secret'),
  user_agent: mk(''),
  prompt_file: mk(''),
  checkin_hours: mk(''),
}};
const ctx = {
  Date, Number, String, Math, Map, Array, Object, isNaN, URLSearchParams, Set,
  document: { getElementById: id => (id === 'cfgForm' ? cfgForm : null) },
  $: id => (id === 'cfgForm' ? cfgForm : null),
};
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) + '\nthis.collectConfig = collectConfig;', ctx);
const out = ctx.collectConfig();
const has = (o, k) => Object.prototype.hasOwnProperty.call(o || {}, k);
process.stdout.write(JSON.stringify([
  has(out.upstream, 'user_agent'), (out.upstream || {}).user_agent,
  has(out.prompt, 'file'), (out.prompt || {}).file,
  has(out, 'listen'),
  has(out.schedule, 'checkin_hours'),
  out.api_key
]));`
	f, err := os.CreateTemp(t.TempDir(), "cfgc-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command(node, f.Name(), "app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("collectConfig node test failed: %v\n%s", err, out)
	}
	// [user_agent 已发, 其值, prompt.file 已发, 其值, listen 未发, checkin_hours 未发, api_key]
	const want = `[true,"",true,"",false,false,"secret"]`
	if strings.TrimSpace(string(out)) != want {
		t.Fatalf("collectConfig=%s want %s", strings.TrimSpace(string(out)), want)
	}
}

// TestConfigFormMatchesCFGMap 配置表单与 CFG_MAP 必须一一对应，且「记录调用来源」
// 这类后端有项、面板声称可改的热生效键必须真的出现在表单里（上游 1.13.0 同款测试）。
//
// 为什么需要：这套映射断掉时没有任何编译期或运行期报错——表单多一个字段会被保存时
// 静默丢弃，CFG_MAP 多一个键则回填/保存空转，两者都只能靠人点开配置页发现。
func TestConfigFormMatchesCFGMap(t *testing.T) {
	src, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)
	htmlBytes, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)

	mapBlock := js[strings.Index(js, "const CFG_MAP = {"):]
	mapBlock = mapBlock[:strings.Index(mapBlock, "\n};")]
	// 不能按行首匹配：CFG_MAP 里多个键写在同一行，只有行首那个带换行缩进。
	inMap := func(name string) bool {
		return regexp.MustCompile(`(?:^|[\s,{])` + regexp.QuoteMeta(name) + `:\s*\[`).MatchString(mapBlock)
	}

	form := html[strings.Index(html, `<form id="cfgForm">`):]
	form = form[:strings.Index(form, "</form>")]
	names := map[string]bool{}
	for _, m := range regexp.MustCompile(`name="([a-z_0-9]+)"`).FindAllStringSubmatch(form, -1) {
		names[m[1]] = true
	}
	if len(names) == 0 {
		t.Fatal("未从配置表单解析出任何 name 字段")
	}
	for n := range names {
		if !inMap(n) {
			t.Errorf("表单字段 %q 在 CFG_MAP 里没有条目（保存时会被静默丢弃）", n)
		}
	}
	for _, m := range regexp.MustCompile(`(?:^|[\s,{])([a-z_0-9]+):\s*\[`).FindAllStringSubmatch(mapBlock, -1) {
		if !names[m[1]] {
			t.Errorf("CFG_MAP 键 %q 在配置表单里没有对应控件", m[1])
		}
	}

	// 明确断言这一个键：后端有配置项（logging.request_client_info，热生效），UI 不能少。
	if !strings.Contains(js, "request_client_info: ['logging', 'request_client_info']") {
		t.Error("CFG_MAP 缺 request_client_info 条目")
	}
	if !names["request_client_info"] {
		t.Error("配置表单缺「记录调用来源」开关（logging.request_client_info）")
	}
}

// TestAppJSCreditHistoryFormatting 积分历史区块：变动列 +N / −N（U+2212）、
// 余额千分位、空列表提示。纯格式化函数按切片做 node 侧断言。
func TestAppJSCreditHistoryFormatting(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; credit history formatting test skipped")
	}
	script := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const start = src.indexOf('function creditNum');
const end = src.indexOf('function renderCreditHistory');
if (start < 0 || end < 0) throw new Error('credit history helpers not found');
const ctx = { Date, Number, String, Math, RegExp, isNaN, toLocaleString: undefined };
vm.createContext(ctx);
vm.runInContext(
  src.slice(start, end) +
  '\nthis.creditEntryText=creditEntryText;this.creditHistoryNote=creditHistoryNote;',
  ctx
);
const time = new Date(2026, 8, 28, 14, 5, 6).toISOString();
process.stdout.write(JSON.stringify({
  gain: ctx.creditEntryText({ time, uid: '89374120', account: '小明', delta: 100, before: 1300, after: 1400 }),
  loss: ctx.creditEntryText({ time, uid: '89374120', account: '', delta: -30, before: 1400, after: 1370 }),
  note: ctx.creditHistoryNote([{ delta: 1300 }, { delta: -60 }]),
  empty: ctx.creditHistoryNote([]),
}));`
	f, err := os.CreateTemp(t.TempDir(), "credit-history-format-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command(node, f.Name(), "app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("credit history formatting node test failed: %v\n%s", err, out)
	}
	gain := "14:05:06 | 小明 | +100 | 1,400 | 余额 1,300 → 1,400"
	loss := "14:05:06 | 89374120 | −30 | 1,370 | 余额 1,400 → 1,370"
	want := `{"gain":` + strconv.Quote(gain) + `,"loss":` + strconv.Quote(loss) +
		`,"note":"2 条 · 净 +1,240","empty":"暂无积分变动记录"}`
	if strings.TrimSpace(string(out)) != want {
		t.Fatalf("credit history formatting=%s want %s", out, want)
	}
}

// TestRegionSchedulingFormRoundTrip 按地区调度策略/账号顺序在配置表单里往返：
// 读入（数组 → 逗号串）→ 修改 → 收集（逗号串 → 数组），且键缺席时 select 回落默认。
func TestRegionSchedulingFormRoundTrip(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	script := `const fs=require('fs'), vm=require('vm'), assert=require('assert');
 const src=fs.readFileSync(process.argv[2],'utf8');
 const html=fs.readFileSync('index.html','utf8');
 const elements={};
 for(const name of ['strategy_domestic','strategy_international','domestic_account_order','international_account_order']) {
   assert(html.includes('name="'+name+'"'), 'missing form field '+name);
   elements[name]={type:name.startsWith('strategy_')?'select-one':'text',value:''};
 }
 const cfg={pool:{strategy:{domestic:'round_robin',international:'sequential_exhaust'},account_order:{domestic:['c','a'],international:['y','x']}}};
 const nodes={cfgForm:{elements},cfgPath:{},cfgNote:{}};
 const ctx={cfgLoaded:null,api:async()=>({config:cfg}),$:id=>nodes[id],markDurationFields(){},syncAllControls(){},toast(message){throw Error(message)}};
 vm.createContext(ctx);
 vm.runInContext(src.slice(src.indexOf('const CFG_MAP ='),src.indexOf("$('btnEye')")),ctx);
 (async()=>{
   await ctx.loadConfig();
   assert.equal(elements.strategy_domestic.value,'round_robin');
   assert.equal(elements.strategy_international.value,'sequential_exhaust');
   assert.equal(elements.domestic_account_order.value,'c, a');
   elements.strategy_domestic.value='weighted_random';
   elements.international_account_order.value='';
   const result=ctx.collectConfig();
   assert.equal(result.pool.strategy.domestic,'weighted_random');
   assert.equal(result.pool.strategy.international,'sequential_exhaust');
   assert.equal(JSON.stringify(result.pool.account_order.domestic),'["c","a"]');
   assert.equal(JSON.stringify(result.pool.account_order.international),'[]');
   delete cfg.pool.strategy; await ctx.loadConfig();
   assert.equal(elements.strategy_domestic.value,'weighted_random');
   assert.equal(elements.strategy_international.value,'weighted_random');
 })().catch(e=>{console.error(e);process.exitCode=1});`
	fp := filepath.Join(t.TempDir(), "region-form.cjs")
	if err = os.WriteFile(fp, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, fp, "app.js").CombinedOutput(); err != nil {
		t.Fatalf("form round trip: %v\n%s", err, out)
	}
}
