'use strict';
/*
 * app.js 行为回归测试（Node + 假 DOM）。
 *
 * 为什么需要：app.js 是 go:embed 的静态资源，Go 编译器只管塞进二进制、不解析内容。
 * `node --check`（见 frontend_test.go）只能证明「语法能解析」，证明不了「逻辑对」——
 * 健康度算错、进度条永远 0% 这种 bug 语法完全合法，而页面看起来还"正常"。
 * 这里把 app.js 真跑起来，用最小 DOM 桩喂真实日志样本，断言渲染结果。
 *
 * 无浏览器环境（本机 agent-browser 不支持 Windows）时的替代验证手段。
 * 用法：node internal/panel/testdata/app_harness.js
 */

const fs = require('fs');
const path = require('path');
const vm = require('vm');

/* ── 最小 DOM 桩 ───────────────────────────────────────────────────── */
// 注意 innerHTML / textContent 的**联动**：真实浏览器里给 textContent 赋值会清空
// innerHTML（两者是同一个 DOM 的两种视图），而普通对象字段不会。
// 之前用普通字段，导致「el.textContent = '' 之后断言 innerHTML === ''」假失败——
// 桩与真实行为不符会产假阳性/假阴性，所以这里用 getter/setter 模拟真实语义。
const els = Object.create(null);
function mkEl(id) {
  let _html = '';
  const e = {
    id,
    get innerHTML() { return _html; },
    set innerHTML(v) { _html = String(v == null ? '' : v); },
    get textContent() { return _html; },
    set textContent(v) { _html = String(v == null ? '' : v); },
    title: '', value: '', checked: false,
    hidden: false, disabled: false,
    dataset: {}, style: {},
    scrollTop: 0, scrollHeight: 0, clientHeight: 0,
    classList: {
      _s: new Set(),
      add(...c) { c.forEach(x => this._s.add(x)); },
      remove(...c) { c.forEach(x => this._s.delete(x)); },
      contains(c) { return this._s.has(c); },
      toggle(c, on) { if (on === undefined) on = !this._s.has(c); on ? this._s.add(c) : this._s.delete(c); return on; },
    },
    appendChild() {}, remove() {}, addEventListener() {}, focus() {},
    // closest：真 DOM 会沿祖先链找，桩只判断"是不是自己"。
    // 支持 `[attr]` 形式的选择器（app.js 里全是这种），不支持标签/类选择器——
    // 需要更复杂的选择器时再扩，当前够用。
    closest(sel) {
      const attrs = String(sel || '').match(/\[[A-Za-z0-9_-]+\]/g);
      if (!attrs) return null;
      for (const a of attrs) {
        if (this.hasAttribute(a.slice(1, -1))) return this;
      }
      return null;
    },
    querySelectorAll() { return []; },    // 属性读写：真实 DOM 有，桩以前没有——aria-expanded / title 这类状态就存在属性里，
    // 缺了它「无障碍属性有没有同步」「title 有没有被摘掉」根本没法断言（一调就 TypeError）。
    _attrs: {},
    setAttribute(k, v) { this._attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k) ? this._attrs[k] : null; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k); },
    removeAttribute(k) { delete this._attrs[k]; },
    // contains：真实 DOM 用来判断「移到的元素是不是自己的后代」。
    // 桩里只按"是不是自己"判断——够用，因为测试只验证"移到自身不算离开"。
    contains(child) { return child === this; },
  };
  return e;
}
function getEl(id) {
  if (!els[id]) els[id] = mkEl(id);
  return els[id];
}

// mkBtn 造一个按钮桩：tagName 必须是 BUTTON，否则「按钮不弹提示」那条规则测不到。
// 文字用 textContent 设（app.js 就是读它判断"有没有文字"的）。
function mkBtn(text, tip) {
  const b = mkEl('btn-' + text + '-' + tip);
  b.tagName = 'BUTTON';
  b.textContent = text;
  if (tip) b.setAttribute('title', tip);
  return b;
}

// mkBtn 造一个按钮桩：tagName 必须是 BUTTON，否则「按钮不弹提示」那条规则测不到。
// 文字用 textContent 设（app.js 就是读它判断"有没有文字"的）。
function mkBtn(text, tip) {
  const b = mkEl('btn-' + text + '-' + tip);
  b.tagName = 'BUTTON';
  b.textContent = text;
  if (tip) b.setAttribute('title', tip);
  return b;
}

// mkSel 造一个 select 桩。自定义下拉要读 options / selectedIndex / value，
// 并支持把选择写回去——没有这些就没法验证「外观层有没有写回原生」。
function mkSel(id, labels, selectedLabel) {
  const s = mkEl(id);
  s.tagName = 'SELECT';
  s.options = labels.map(l => ({ textContent: l, value: l }));
  s.selectedIndex = Math.max(0, labels.indexOf(selectedLabel));
  Object.defineProperty(s, 'value', {
    get() { return s.options[s.selectedIndex] ? s.options[s.selectedIndex].value : ''; },
    set(v) {
      const i = s.options.findIndex(o => o.value === v);
      if (i >= 0) s.selectedIndex = i;
    },
  });
  // 自定义控件会往 select 上插 DOM（parentNode.insertBefore）并加 class
  s._attrs = {};
  s.classList = {
    _s: new Set(),
    add(c) { this._s.add(c); },
    remove(c) { this._s.delete(c); },
    contains(c) { return this._s.has(c); },
    toggle(c, on) { on ? this._s.add(c) : this._s.delete(c); },
  };
  return s;
}

// mkCb 造一个 checkbox 桩。
function mkCb(checked) {
  const c = mkEl('cb-' + Math.random());
  c.tagName = 'INPUT';
  c.checked = !!checked;
  c.classList = {
    _s: new Set(),
    add(x) { this._s.add(x); },
    remove(x) { this._s.delete(x); },
    contains(x) { return this._s.has(x); },
    toggle(x, on) { on ? this._s.add(x) : this._s.delete(x); },
  };
  return c;
}

const notifications = [];
// timerLog 记录 setInterval 的间隔与 clearInterval 的 id（见上面 sandbox 的说明）。
const timerLog = { set: [], clear: [] };
let notifyPerm = 'granted';

// docHandlers 记录挂在 document 上的事件处理器（提示浮层要靠它被调到）。
// 假 DOM 不派发事件，所以把回调存下来供 trailer 直接调用。
const docHandlers = { over: [], out: [], move: [] };
// body.appendChild 会把元素按 className 记进 els，供测试按名字取回。
// 提示浮层是 app.js 动态创建的，没有 id——用 className 当键才能找到它。
function bodyAppend(el) {
  if (el && el.className) els['__' + String(el.className).split(' ').pop()] = el;
}

const sandbox = {
  console,
  // 定时器不真跑：这些回调（通知自动关闭、resize debounce）与本测试断言无关，
  // 真调度只会让 node 为了等一个 8s 定时器白挂 8 秒才退出。
  // 但 setInterval 要返回**真值 id** 并记录 clearInterval：队列轮询在切到国际服时
  // 会被清掉，那条契约只有在 queueTimer 是假值时才断言得了（恒返回 0 就永远为假）。
  setTimeout: () => 0, clearTimeout: () => {},
  setInterval: (fn, ms) => { timerLog.set.push(ms); return timerLog.set.length; },
  clearInterval: id => { timerLog.clear.push(id); },
  Date, Math, JSON, Object, Array, String, Number, Boolean, RegExp, Error, Promise, Set, Map,
  encodeURIComponent, decodeURIComponent, parseInt, parseFloat, isNaN,
  document: {
    getElementById: getEl,
    createElement: () => mkEl('new'),
    querySelectorAll: () => [],
    documentElement: { dataset: {} },
    body: Object.assign(mkEl('body'), { appendChild: bodyAppend }),
    contains: node => !!node && node !== null,
    addEventListener(type, fn) {
      if (type === 'mouseover') docHandlers.over.push(fn);
      else if (type === 'mouseout') docHandlers.out.push(fn);
      else if (type === 'mousemove') docHandlers.move.push(fn);
    },
  },
  localStorage: {
    _m: Object.create(null),
    getItem(k) { return k in this._m ? this._m[k] : null; },
    setItem(k, v) { this._m[k] = String(v); },
    removeItem(k) { delete this._m[k]; },
  },
  location: { hash: '', href: 'http://localhost:8787/panel/', origin: 'http://localhost:8787', protocol: 'http:', host: 'localhost:8787' },
  history: { replaceState() {} },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  addEventListener() {},
  ResizeObserver: class { observe() {} disconnect() {} },
  Notification: function (title, opts) {
    notifications.push({ title, body: opts && opts.body });
    this.close = () => {};
  },
  fetch: () => Promise.reject(new Error('fetch not stubbed')),
  confirm: () => true,
  alert: () => {},
  toast: null,
};
sandbox.Notification.permission = notifyPerm;
sandbox.Notification.requestPermission = () => Promise.resolve(notifyPerm);
sandbox.window = sandbox;
sandbox.globalThis = sandbox;
sandbox.self = sandbox;
// 把桩的内部状态也放进沙箱：trailer 是在沙箱里跑的，看不见 harness 作用域的变量。
// __els 按 id/className 存了所有元素（提示浮层是动态创建的，只能按 className 找回）。
sandbox.__els = els;
// Event：app.js 的写回函数要 dispatchEvent(new Event('change'))。
// 沙箱里没有浏览器全局，给个最小实现——测试只关心"有没有派发、带什么类型"。
sandbox.Event = class Event {
  constructor(type, opts) { this.type = type; this.bubbles = !!(opts && opts.bubbles); }
};
sandbox.__docHandlers = docHandlers;
sandbox.__timerLog = timerLog;
// 通知开关默认打开，方便断言通知真的被触发
sandbox.localStorage.setItem('wb2api.notify', '1');

/* ── 载入 app.js ───────────────────────────────────────────────────── */
const appPath = path.join(__dirname, '..', 'app.js');
const src = fs.readFileSync(appPath, 'utf8');

// 追加导出尾：app.js 是顶层 const/let，脚本作用域内可见；这样能在同一个闭包里取出
// 被测函数，又不必给生产代码加任何测试钩子。
const trailer = `
;globalThis.__t = {
  healthOf, updateTaskProgress, renderTaskProgress, TP,
  fmtLogLine, esc, renderAccounts, renderDashboard, flipModels,
  fmtInt, fmtTok, fmtRate, fmtMs, pctOf, uptime, hhmm, hourLabel,
  uidName,
  applyStatus, loadStatus, loadStats, refreshVisible,
  accKind, visibleAccounts, visibleLogs, logCatOf, paintLogs,
  renderCheckin, visibleCheckins, taskKind, visibleTasks,
  setTaskFilter: v => { taskFilter = v; },
  setCkFilter: v => { ckFilter = v; },
  setCkQuery: v => { ckQuery = v; },
  setOverview: v => { overviewData = v; },
  setAccFilter: v => { accFilter = v; },
  setAccQuery: v => { accQuery = v; },
  setAccSort: v => { accSort = v; },
  setLogFilter: v => { logFilter = v; },
  setLogQuery: v => { logQuery = v; },
  setLogLines: v => { logLines = v; },
  accState: () => ({ accFilter, accQuery, accSort, accList }),
  ask, closeAsk: v => _closeAsk(v),
  openExpiryDetail, _closeExpiryDetail,
  // 使用日志（请求审计流水）
  useRows, useAgg, useRowHTML, useCSV, fmtCredit, useSlow, useUserName, useModelName,
  renderUsage,
  setTrendDays: v => { trendData = { days: v }; },
  setDailyNet: v => { _lastDailyNet = v; },
  renderCreditBurn,
  setUseData: v => { useData = v; },
  setUseUser: v => { useUser = v; },
  setUseModel: v => { useModel = v; },
  setUseStatus: v => { useStatus = v; },
  setUseMode: v => { useMode = v; },
  setUseAccount: v => { useAccount = v; },
  useActiveFilters, useHasAnyFilter, useStatusLabel, useStatusMatch,
  fltOptions, renderFilterMenu, openFilterMenu, closeFilterMenu, filterMenuOpen, clearUseFilters,
  setUseQuery: v => { useQuery = v; },
  // 密钥页
  keyRowHTML, keyUseCell, renderKeys,
  setKeysData: v => { keysData = v; },
  // 密钥指纹 → 名字的映射（仪表盘「按密钥」表用）。平时由 loadStats 从
  // /panel/api/stats 的 key_names 灌入；测试直接注入，免得真发请求。
  setKeyNames: v => { keyNames = v; },
  // 统一提示浮层：把三个挂到 document 上的处理器录下来，供测试直接调。
  // 假 DOM 不派发真实事件，所以直接调处理函数——测的是**处理逻辑**
  //（摘 title / 装回去 / 收放 / 移到子元素不算离开），不是浏览器的事件派发本身。
  tipOver: ev => (__docHandlers.over || []).forEach(h => h(ev)),
  tipOut: ev => (__docHandlers.out || []).forEach(h => h(ev)),
  tipAttr: el => (el ? el.getAttribute('title') : null),
  // 自定义控件的内部动作：假 DOM 不派发真实点击，直接把处理器拆出来调。
  // 测的是"点了之后有没有写回原生元素"这个契约，不是浏览器的事件派发本身。
  pickDropdownOption, toggleCheckbox, dropdownLabel, syncSelect,
  dropdownPaint: sel => {
    const m = sel.parentNode.querySelector('.dd-menu');
    if (m) m._paintHooks = null;
  },
  dropdownPick: (sel, i) => {
    const menu = sel.parentNode.querySelector('.dd-menu');
    // 触发 menu 上的 click 委托：构造一个能 closest 到选项的 target
    const o = menu._opts && menu._opts[i];
    if (o) (menu._clickHandlers || []).forEach(h => h({ target: o }));
  },
  switchToggle: cb => {
    const sw = cb.nextElementSibling;
    if (sw && sw._clickHandlers) sw._clickHandlers.forEach(h => h({ preventDefault() {} }));
  },
  tipState: () => {
    const el = __els['__tip'];
    return { on: !!(el && el.classList.contains('on')), text: el ? el.textContent : '' };
  },
  niceMax,
  // 积分构成卡片：标题取昵称（不可见字符要剥掉）否则 uid8；消耗条宽度按已用占比
  pkNick, pkTitle, renderPackages,
  // 添加账号：双域选择（cn/global）。0b2103b 引入、融合时前端入口整段丢失，
  // 这里钉住「选域 → 带 realm 的 login/start」这条契约。
  // 选哪个 radio 由 harness 侧 __setAddRealm 控制（querySelector 桩）。
  openAdd, startAddLogin, pollLogin,
  // 面板切换优化（缓存优先 / 域开关原地更新 / 轮询收敛）：
  renderIfChanged, markDirty, paintView, paintNow,
  renderRealmSwitch, setPanelRealm, applyPanelRealm, applyCnOnlyViews,
  realmBtn: k => (realmBtns || []).filter(b => b.k === k)[0],
  realmInd: () => realmInd,
  viewDirtyState: v => !!viewDirty[v],
  setView: v => { view = v; },
  getView: () => view,
  setQueueTimer: v => { queueTimer = v; },
  queueTimerValue: () => queueTimer,
  pollMs: POLL_MS,
  accViewSig, ckViewSig, logViewSig, mdViewSig, useViewSig, keyViewSig,
  // 图表悬停
};
`;

vm.createContext(sandbox);
vm.runInContext(src + trailer, sandbox, { filename: 'app.js' });
const T = sandbox.__t;

/* ── 断言工具 ─────────────────────────────────────────────────────── */
let fails = 0, passes = 0;
// eq 严格比较，但把「数字」与「同一数字的字符串」视为相等。
//
// 为什么需要这条：DOM 的 textContent 永远是字符串，而断言常写 eq(el.textContent, 3)。
// 以前的桩把 textContent 当普通字段、返回数字，这些断言才侥幸通过；桩一旦按真实语义
// 返回字符串，它们就成片失败。放宽这一条比逐个改断言更稳，也仍然抓得住真正的值错误
// （想比较数值字符串与数字之外的东西，JSON 严格比较照旧生效）。
function eqNumish(a, b) {
  if (typeof a === 'number' || typeof b === 'number') {
    const na = Number(a), nb = Number(b);
    if (!Number.isNaN(na) && !Number.isNaN(nb) && String(a).trim() !== '' && String(b).trim() !== '') {
      return na === nb;
    }
  }
  return null;
}
function eq(actual, want, msg) {
  const a = JSON.stringify(actual), w = JSON.stringify(want);
  if (a === w) { passes++; return; }
  const numish = eqNumish(actual, want);
  if (numish === true) { passes++; return; }
  fails++;
  console.error(`  ✗ ${msg}\n      得到 ${a}\n      期望 ${w}`);
}
function ok(cond, msg) {
  if (cond) { passes++; return; }
  fails++;
  console.error(`  ✗ ${msg}`);
}

/* ── 1. healthOf：健康度评分 ──────────────────────────────────────── */
console.log('healthOf');
T.setOverview({ session_dead_threshold: 3, accounts: [{ uid: 'u1', alias: '主号' }] });

eq(T.healthOf({ success_count: 100, err_total: 0 }).score, 100, '健康号满分');
eq(T.healthOf({ success_count: 1, err_total: 1 }).score, 100, '样本不足（<5）不算错误率');
eq(T.healthOf({ disabled: true, disabled_reason: '12153 session dead' }).score, 0, '禁用号 0 分');
ok(T.healthOf({ disabled: true, disabled_reason: '12153 session dead' })
  .reasons.join('；').includes('12153'), '禁用原因带进 reasons');

const cooling = T.healthOf({ success_count: 5, err_total: 0, cool_remaining_sec: 60, cool_kind: 'soft' });
eq(cooling.score, 70, '软冷却 -30');
ok(cooling.reasons.join('；').includes('限流冷却'), '软冷却原因文案');

const hard = T.healthOf({ success_count: 5, err_total: 0, cool_remaining_sec: 60, cool_kind: 'hard_credit' });
eq(hard.score, 70, '积分冷却 -30');
ok(hard.reasons.join('；').includes('积分耗尽'), '积分冷却原因文案');

const brk = T.healthOf({ success_count: 5, err_total: 0, breaker_until: new Date(Date.now() + 60000).toISOString() });
eq(brk.score, 55, '熔断 -45（优先于冷却，不叠加）');
ok(brk.reasons.join('；').includes('熔断中'), '熔断原因文案');

// 连续 12153：2/3 → -24（每 -12），这是唯一能提前干预的窗口
const sd = T.healthOf({ success_count: 5, err_total: 0, session_dead_fails: 2 });
eq(sd.score, 76, '连续 12153 2/3 → -24');
ok(sd.reasons.join('；').includes('2/3'), '12153 展示 n/阈值（阈值取 overview）');

eq(T.healthOf({ success_count: 5, err_total: 0, session_dead_fails: 9 }).score, 70, '12153 扣分封顶 -30');

// 错误率：样本够（>=5）才计入，50% → -25（封顶）
eq(T.healthOf({ success_count: 5, err_total: 5 }).score, 75, '错误率 50% → -25（封顶）');
eq(T.healthOf({ success_count: 9, err_total: 1 }).score, 94, '错误率 10% → -6');

eq(T.healthOf({ success_count: 5, err_total: 0, retry_count: 2 }).score, 90, '累计熔断 2 次 → -10');
eq(T.healthOf({ success_count: 5, err_total: 0, retry_count: 9 }).score, 85, '累计熔断扣分封顶 -15');
eq(T.healthOf({ success_count: 5, err_total: 0, soft_streak: 3 }).score, 90, '连续限流 >=3 → -10');
eq(T.healthOf({ success_count: 5, err_total: 0 }).reasons[0], '无异常信号', '健康号 reasons 兜底文案');

/* ── 2. updateTaskProgress：进度条 ────────────────────────────────── */
console.log('updateTaskProgress');
const L = (t, s) => `2026/09/13 21:0${t}:00 panel: ${s}`;
const lines = [
  '2026/09/13 21:00:00 workbuddy2api listening on :8787',
  L(0, '一键完成 uid=u1 开始，共 18 项（含批量接受）；单项含真实对话与异步计分等待，请耐心等待逐项日志'),
  L(1, '一键完成 uid=u1 [1/18] >> (批量接受) 拉取任务列表…'),
  L(2, '一键完成 uid=u1 [1/18] [OK] (批量接受) 已接受 3 个任务'),
  L(3, '一键完成 uid=u1 [2/18] [SKIP] task_daily 该账号无此任务'),
  L(4, '一键完成 uid=u1 [3/18] [FAIL] task_chat 查询失败: 401 ...'),
];
T.updateTaskProgress(lines);

eq(T.TP.total, 18, '总数从「开始」行解析');
eq(T.TP.idx, 3, '进度取最大序号（同一 idx 的 >> 与 [OK] 只算一次）');
eq(T.TP.done, false, '未结束 → done=false');
eq(getEl('taskProgress').hidden, false, '有进度 → 显示进度条');
eq(getEl('tpFill').style.width, '17%', '宽度 3/18 ≈ 17%');
ok(getEl('tpText').textContent.includes('主号 · 第 3/18 项 · 17%'), '文案含别名/序号/百分比：' + getEl('tpText').textContent);
ok(/预计剩余/.test(getEl('tpText').textContent), '有 >=2 个时间戳样本 → 给出剩余估计');

// 结束行：满格 + 标 done + 弹通知
T.updateTaskProgress(lines.concat([
  L(5, '一键完成 uid=u1 全部结束 ✅ 共 18 项（12 完成 / 5 跳过 / 1 失败）耗时 1m3s'),
]));
eq(T.TP.done, true, '结束行 → done=true');
eq(getEl('tpFill').style.width, '100%', '结束不置满格是 bug，必须 100%');
ok(getEl('taskProgress').classList.contains('done'), '结束 → 容器加 .done（灰条）');
ok(getEl('tpText').textContent.includes('已完成'), '结束文案：' + getEl('tpText').textContent);
eq(notifications.length, 1, '触发 1 次桌面通知');
ok(notifications[0].body.includes('主号'), '通知标题用别名而非 uid：' + notifications[0].body);
ok(notifications[0].body.includes('12'), '通知带完成项数：' + notifications[0].body);

// 幂等：1.5s 轮询会反复看到同一行结束日志，不能反复弹通知
T.updateTaskProgress(lines.concat([
  L(5, '一键完成 uid=u1 全部结束 ✅ 共 18 项（12 完成 / 5 跳过 / 1 失败）耗时 1m3s'),
]));
eq(notifications.length, 1, '同一批结束日志不重复通知（轮询幂等）');

// 新批次：重置为进行中并重新计数
T.updateTaskProgress(lines.concat([
  L(5, '一键完成 uid=u1 全部结束 ✅ 共 18 项（12 完成 / 5 跳过 / 1 失败）耗时 1m3s'),
  L(6, '一键完成 uid=u2 开始，共 4 项（含批量接受）；单项含真实对话与异步计分等待，请耐心等待逐项日志'),
  L(7, '一键完成 uid=u2 [1/4] [OK] task_a 完成'),
]));
eq(T.TP.uid, 'u2', '新批次接管进度条');
eq(T.TP.idx, 1, '新批次进度重置');
eq(T.TP.total, 4, '新批次总数');
eq(T.TP.done, false, '新批次回到进行中');

// 日志里没有一键完成 → 进度条收起，不占地方
T.updateTaskProgress(['2026/09/13 21:00:00 workbuddy2api listening on :8787']);
eq(T.TP.total, 0, '无批次 → 清空');
eq(getEl('taskProgress').hidden, true, '无批次 → 隐藏进度条');

/* ── 3. fmtLogLine：不回归（uid 别名 + 分类 + 状态 badge） ────────── */
console.log('fmtLogLine');
T.setOverview({ session_dead_threshold: 3, accounts: [{ uid: '11111111-2222-3333-4444-555555555555', alias: '备用号' }] });
const html = T.fmtLogLine('2026/09/13 21:01:00 panel: 一键完成 uid=11111111-2222-3333-4444-555555555555 [3/18] [FAIL] task_chat boom');
ok(html.includes('备用号'), 'uid 换成别名');
// 原始 uid 只允许出现在 title="…" 里（hover 提示），可见文本中不得再有——
// 一条日志 17 项 × 满屏 uuid 正是用户抱怨「太乱」的根因。
const visible = html.replace(/title="[^"]*"/g, '');
ok(!visible.includes('555555555555'), '可见文本里不再出现原始 uid：' + visible);
ok(html.includes('ln-tag fail'), '[FAIL] 渲染成状态 badge');
ok(html.includes('[21:01:00]'), '时间戳抽出为 [HH:MM:SS]');
ok(html.includes('ln-cat'), '每行都有分类标签');

// workbuddy2api 服务名不得被误判成「旅行」（历史 bug）
ok(T.fmtLogLine('2026/09/13 21:01:00 workbuddy2api listening on :8787').includes('c-sys'),
  '服务名 workbuddy2api 归「系统」而非「旅行」');

/* ── 3b. 日志筛选 / 搜索 ─────────────────────────────────────────── */
console.log('logFilter');
const sampleLogs = [
  '2026/09/13 21:01:00 workbuddy2api listening on :8787',
  '2026/09/13 21:02:00 panel: 一键完成 uid=11111111-2222-3333-4444-555555555555 [3/18] [FAIL] task_chat boom',
  '2026/09/13 21:03:00 checkin 签到完成',
];
eq(T.logCatOf(sampleLogs[0]).label, '系统', 'listening 归系统');
eq(T.logCatOf(sampleLogs[1]).label, '任务', '一键完成归任务');
eq(T.logCatOf(sampleLogs[2]).label, '签到', '签到行归签到');
T.setLogLines(sampleLogs);
T.setLogFilter('任务'); T.setLogQuery('');
eq(T.visibleLogs(sampleLogs).length, 1, '按分类筛只留任务行');
T.setLogFilter('all'); T.setLogQuery('备用号');
eq(T.visibleLogs(sampleLogs).length, 1, '按别名搜索命中 uid 行');
T.setLogQuery('');
T.paintLogs();
ok(getEl('logChips').innerHTML.includes('任务'), '日志芯片含任务分类');
ok(getEl('logBox').innerHTML.includes('ln-cat'), '筛选后仍按终端样式渲染');

/* ── 4. renderAccounts：列数必须与表头一致 ────────────────────────── */
// 回归案例：加了健康度单元格，但 index.html 的 <thead> 没加 <th>——表体 9 格 /
// 表头 8 格，整张表右移一列，健康度分数跑到「积分」表头底下。
// 这里把行内 <td> 数出来，assets_test.go 把表头 <th> 数出来，两边必须相等。
console.log('renderAccounts');
T.setOverview({ session_dead_threshold: 3, accounts: [{ uid: 'u1', alias: '主号' }] });
T.renderAccounts([{
  uid: 'u1', alias: '主号', nickname: 'nick', credits: 3949,
  success_count: 10, err_total: 0, in_flight: 0,
}]);
const rowHtml = getEl('accBody').innerHTML;
const tdCount = (rowHtml.match(/<td\b/g) || []).length;
eq(tdCount, 10, '单个账号行渲染 10 个 <td>（表头也必须是 10 个 <th>，见 assets_test.go）');
ok(rowHtml.includes('class="hp"'), '健康度单元格在位');
ok(/<td class="hp"[\s\S]*?width:100%/.test(rowHtml), '健康号健康度条满格');
ok(rowHtml.includes('>3949<'), '积分仍在自己的单元格里（列序：账号/状态/健康度/积分）');
ok(rowHtml.includes('未验活'), '还没验活过的号显示「未验活」而不是空白');
// 行上不许挂 title：挂在 <tr> 上会让鼠标停在行内**任何地方**都弹提示。
// 账号行原本挂的是「uid: …」，而 uid 就在账号格第二行显示着——纯重复的噪音。
// 判断原则：提示只在"揭示了行内看不到的信息"时才加（健康度低的原因、任务描述/
// 跳转链接都属于这类），重复行内已有信息的一律去掉。
ok(!/<tr[^>]*title=/.test(rowHtml), '账号行不挂 title（挂了会让整行任何位置都弹提示）');
ok(!rowHtml.includes('最近使用：'), '并发格不再重复弹「最近使用」——同一信息就在它下面那行');
ok(rowHtml.includes('点击修改显示名称'), '但账号名的「点击改名」提示保留（那是操作引导，不是重复信息）');

/* ── 4a-1b. 账号池的国服/国际服徽标 ──────────────────────────────── */
// 规则（8a4b45f 起，见 renderAccounts 内注释）：**两个域都挂徽标**，位置在状态标签
// 之后——账号池混着两个域的号，冷却/禁用行也要能一眼看出归属。
// 本块原先钉的是「只有 global 显示、文案叫国际版」的旧规则（05:36 的 a8c775e），
// 当天 18:12 的实现把它改成了两域都显示、文案统一成「国服/国际服」；
// 此处按**现行实现**钉住，别再照旧规则改回去。
{
  T.renderAccounts([{ uid: 'g1', nickname: '国际号', realm: 'global', credits: 100, success_count: 1, err_total: 0, in_flight: 0 }]);
  const gh = getEl('accBody').innerHTML;
  ok(gh.includes('realm-tag global') && gh.includes('国际服'), 'global 账号挂「国际服」紫标');

  T.renderAccounts([{ uid: 'c1', nickname: '国内号', realm: 'cn', credits: 100, success_count: 1, err_total: 0, in_flight: 0 }]);
  const ch = getEl('accBody').innerHTML;
  ok(ch.includes('realm-tag') && ch.includes('国服'), 'CN 账号挂「国服」蓝标');
  ok(!ch.includes('realm-tag global'), 'CN 标不带 global 类（配色不串到紫色）');

  // realm 字段缺失（旧 state / 尚未迁移）也不能炸：按 CN 兜底显示
  T.renderAccounts([{ uid: 'x1', nickname: '无域号', credits: 100, success_count: 1, err_total: 0, in_flight: 0 }]);
  const xh = getEl('accBody').innerHTML;
  ok(xh.includes('realm-tag') && xh.includes('国服'), 'realm 缺失按国服兜底（旧 state 兼容）');
}

/* ── 4a-2. 验活列的三种状态 ───────────────────────────────────────── */
// 这一列是本轮新增，最容易出的错是「失败次数」和「距禁用还差几次」算反——
// 那会让人以为某个号还很安全，其实下一轮就要被禁用了。
{
  const base = { uid: 'p1', alias: 'A', credits: 100, success_count: 1, err_total: 0, in_flight: 0 };
  T.setOverview({ session_dead_threshold: 3, probe_threshold: 3, accounts: [] });

  T.renderAccounts([Object.assign({}, base, { probe_at: '2026-09-14T17:00:00+08:00', probe_ok: true, probe_fails: 0 })]);
  ok(getEl('accBody').innerHTML.includes('通过'), '验活通过显示「通过」');

  T.renderAccounts([Object.assign({}, base, {
    probe_at: '2026-09-14T17:00:00+08:00', probe_ok: false, probe_fails: 1, probe_err: 'HTTP 401',
  })]);
  const failHtml = getEl('accBody').innerHTML;
  ok(failHtml.includes('失败 1/3'), '失败显示「连续 n/阈值」');
  ok(failHtml.includes('sdot warn'), '距阈值还远时用黄点');
  ok(failHtml.includes('HTTP 401'), '悬停里带上失败原因');

  T.renderAccounts([Object.assign({}, base, {
    probe_at: '2026-09-14T17:00:00+08:00', probe_ok: false, probe_fails: 2, probe_err: 'HTTP 401',
  })]);
  ok(getEl('accBody').innerHTML.includes('sdot bad'), '再失败一次就禁用时用红点（提前预警）');

  // 阈值可由后端调整：前端必须读 overview 的值，不能硬编码 3。
  T.setOverview({ session_dead_threshold: 3, probe_threshold: 5, accounts: [] });
  T.renderAccounts([Object.assign({}, base, {
    probe_at: '2026-09-14T17:00:00+08:00', probe_ok: false, probe_fails: 2,
  })]);
  ok(getEl('accBody').innerHTML.includes('失败 2/5'), '阈值跟着后端走（不硬编码 3）');
  ok(getEl('accBody').innerHTML.includes('sdot warn'), '阈值改成 5 后，2 次失败仍是黄点');
  T.setOverview({ session_dead_threshold: 3, probe_threshold: 3, accounts: [] });
}

/* ── 4b. 账号筛选 / 搜索 / 排序 ───────────────────────────────────── */
console.log('accountFilter');
const pool = [
  { uid: 'u-ok', alias: '主力号', nickname: 'nick-ok', credits: 100, success_count: 10, err_total: 0 },
  { uid: 'u-cool', alias: '冷却号', credits: 800, success_count: 5, err_total: 0, cool_remaining_sec: 60, cool_kind: 'soft' },
  { uid: 'u-off', alias: '禁用号', credits: 50, disabled: true, disabled_reason: 'manual' },
];
eq(T.accKind(pool[0]), 'ok', '健康号归「可用」');
eq(T.accKind(pool[1]), 'cool', '冷却号归「冷却」');
eq(T.accKind(pool[2]), 'off', '禁用号归「禁用」');
T.setAccFilter('ok'); T.setAccQuery(''); T.setAccSort('health');
eq(T.visibleAccounts(pool).map(s => s.uid), ['u-ok'], '筛选可用只留健康号');
T.setAccFilter('all'); T.setAccQuery('冷却');
eq(T.visibleAccounts(pool).map(s => s.uid), ['u-cool'], '搜索别名命中冷却号');
T.setAccQuery(''); T.setAccSort('credits');
eq(T.visibleAccounts(pool).map(s => s.uid), ['u-cool', 'u-ok', 'u-off'], '按积分降序');
T.setAccSort('health');
T.renderAccounts(pool);
ok(getEl('accChips').innerHTML.includes('data-f="ok"'), '筛选芯片渲染在位');
ok(getEl('accCount').textContent.includes('3'), '计数显示 3 个账号');
T.setAccFilter('ok');
T.renderAccounts();
eq((getEl('accBody').innerHTML.match(/<tr\b/g) || []).length, 1, '筛选后只渲染可用行');
ok(getEl('accCount').textContent.includes('1 / 3'), '筛选后计数 1 / 3');
T.setAccFilter('all');
T.renderAccounts();
ok(!getEl('accBody').innerHTML.includes('签到'), '账号表不再放签到按钮');
ok(!getEl('accBody').innerHTML.includes('任务'), '账号表不再放任务按钮');

/* ── 4c. 签到积分页 ───────────────────────────────────────────────── */
console.log('checkinPage');
const ckPool = [
  { uid: 'u-ok', alias: '主力号', credits: 630, today_checked: true, today_credit: 100, month_credit: 200, last_checkin: '2026-09-13T10:00:00Z' },
  { uid: 'u-wait', alias: '未签号', credits: 50, today_checked: false, today_credit: 0, month_credit: 100 },
];
T.renderCheckin(ckPool);
eq(getEl('ckSigned').textContent, '1 / 2', '今日已签 1/2');
eq(getEl('ckPending').textContent, 1, '未签 1');
eq(getEl('ckTodaySum').textContent, '+100', '今日积分合计');
eq((getEl('ckBody').innerHTML.match(/<td\b/g) || []).length / 2, 8, '签到行 8 列');
ok(getEl('ckBody').innerHTML.includes('已签'), '已签胶囊在位');
ok(getEl('ckBody').innerHTML.includes('data-a="checkin"'), '签到按钮在签到页');
eq(T.visibleCheckins(ckPool).length, 2, '默认全部');
T.setCkFilter('signed');
eq(T.visibleCheckins(ckPool).map(s => s.uid), ['u-ok'], '筛选已签');
T.setCkFilter('pending');
eq(T.visibleCheckins(ckPool).map(s => s.uid), ['u-wait'], '筛选未签');
T.setCkFilter('all');

/* ── 4d. 任务弹窗筛选 ─────────────────────────────────────────────── */
console.log('taskFilter');
eq(T.taskKind({ claimed: true }), 'done', '已领取');
eq(T.taskKind({ claimable: true }), 'claim', '可领取优先于可自动');
eq(T.taskKind({ auto: true }), 'auto', '官方列表里能自动完成的');
eq(T.taskKind({ needs_adaptation: true }), 'new', '官方新任务标为待适配');
eq(T.taskKind({ title: '手工' }), 'manual', '其余需手动');
const taskPool = [
  { task_code: 'a', claimed: true },
  { task_code: 'b', claimable: true },
  { task_code: 'c', auto: true },
  { task_code: 'd' },
  { task_code: 'e', needs_adaptation: true }
];
T.setTaskFilter('claim');
eq(T.visibleTasks(taskPool).map(t => t.task_code), ['b'], '筛选可领取');
T.setTaskFilter('auto');
eq(T.visibleTasks(taskPool).map(t => t.task_code), ['c'], '筛选可自动');
T.setTaskFilter('new');
eq(T.visibleTasks(taskPool).map(t => t.task_code), ['e'], '筛选官方新任务');
T.setTaskFilter('all');
eq(T.visibleTasks(taskPool).length, 5, '筛选全部');

/* ── 5. 仪表盘：格式化助手 + 渲染 ──────────────────────────────────── */
console.log('dashboard');

// 格式化边界：null 与 0 必须区分（没数据 ≠ 数据是 0）。
eq(T.fmtInt(null), '-', 'fmtInt(null) 是「-」而不是 0');
eq(T.fmtInt(0), '0', 'fmtInt(0) 是「0」');
eq(T.fmtInt(1234567), '1,234,567', 'fmtInt 千分位');

eq(T.fmtTok(0), '0', 'fmtTok(0)');
eq(T.fmtTok(999), '999', 'fmtTok 三位数不缩写');
eq(T.fmtTok(1000), '1.0k', 'fmtTok 千位保留一位');
eq(T.fmtTok(9999), '10.0k', 'fmtTok 四位数仍保留一位');
eq(T.fmtTok(10000), '10k', 'fmtTok 五位数起取整');
eq(T.fmtTok(1234567), '123.5万', 'fmtTok 百万级用中文「万」');
eq(T.fmtTok(2500000000), '25.00亿', 'fmtTok 十亿级用中文「亿」');
// 十亿以上不再用 B：中文界面里 B 看不出是十亿还是亿，且 63.00B 这种把量级信息也吞了。
ok(!T.fmtTok(42138357788).includes('B'), 'fmtTok 不再输出 B（改用中文单位）');
eq(T.fmtTok(42138357788), '421.38亿', 'fmtTok 百亿级');
eq(T.fmtTok(60350000000), '603.50亿', 'fmtTok 六百亿级');

// 单价必须按量级给精度：千分位以下被 toFixed(2) 显示成 "0.00" 时，
// 数字是对的但看起来像没数据——比显示「—」更误导。（口径：积分/百万token）
eq(T.fmtRate(0), '—', 'fmtRate(0) 是「—」');
eq(T.fmtRate(0.0011), '0.0011', 'fmtRate 千分位以下保留 4 位（不能显示成 0.00）');
eq(T.fmtRate(0.97), '0.97', 'fmtRate 常规量级两位小数');
eq(T.fmtRate(310), '310.00', 'fmtRate 百 token 口径的常规件两位小数');

// 除零必须回 0：buckets/prompt 都可能为 0，NaN/Infinity 会污染整列显示。
eq(T.pctOf(1, 0), 0, 'pctOf 分母 0 → 0（不是 NaN/Infinity）');
eq(T.pctOf(3, 4), 75, 'pctOf 正常');

eq(T.fmtMs(0), '—', 'fmtMs(0) 表示未观测到');
eq(T.fmtMs(412.5), '413ms', 'fmtMs 亚秒');
eq(T.fmtMs(1500), '1.50s', 'fmtMs 秒级');

eq(T.uptime(3661), '1时01分', 'uptime 不足一天复用 dur');
eq(T.uptime(90000), '1天1时', 'uptime 跨天折成 N天H时');

// 渲染：喂一份完整快照，断言各块的可见结果。
T.setOverview({
  session_dead_threshold: 3,
  accounts: [{ uid: 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee', alias: '主号' }],
});
const nowHour = Math.floor(Date.now() / 3600000);
const buckets = [];
for (let i = 23; i >= 0; i--) {
  buckets.push({ hour: nowHour - i, requests: i === 0 ? 2 : 0, tokens: i === 0 ? 9000 : 0 });
}
const snap = {
  started: '2026-09-13T21:00:00+08:00',
  uptime_sec: 3661,
  totals: {
    requests: 4, ok: 3, errors: 1, prompt_tokens: 12000, completion_tokens: 8000,
    total_tokens: 20000, cached_tokens: 3000, missing_usage: 1,
    avg_ttfb_ms: 412.5, avg_duration_ms: 4000, tokens_per_sec: 2.5,
  },
  models: [
    {
      key: 'glm-5.2', requests: 3, ok: 2, errors: 1, prompt_tokens: 9000,
      completion_tokens: 6000, total_tokens: 15000, cached_tokens: 3000,
      credits: 4650, // 4650 积分 / 0.015M tok = 310000 积分/百万token（fmtRate 两位小数）
      missing_usage: 0, avg_ttfb_ms: 400, avg_duration_ms: 4000, tokens_per_sec: 2.5,
    },
    {
      key: 'deepseek-v4', requests: 1, ok: 1, errors: 0, prompt_tokens: 3000,
      completion_tokens: 2000, total_tokens: 5000, cached_tokens: 0,
      missing_usage: 1, avg_ttfb_ms: 0, avg_duration_ms: 0, tokens_per_sec: 0,
    },
  ],
  // accounts 保留：metrics 仍在算这一维（积分比率靠它归因），只是面板不再渲染。
  // 留着正好验证"快照多给一个字段不会让前端出错"。
  accounts: [{
    key: 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee', requests: 3, ok: 2, errors: 1,
    prompt_tokens: 9000, completion_tokens: 6000, total_tokens: 15000,
    cached_tokens: 3000, missing_usage: 0, avg_ttfb_ms: 400,
    avg_duration_ms: 4000, tokens_per_sec: 2.5,
  }],
  // 按密钥：面板现在展示这一维。第三把的名字**故意不给**，
  // 用来验证映射缺失时回落短指纹（而不是渲染成空白行）。
  keys: [
    {
      key: '9f2c4a1b7e08d3f5a6c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d',
      requests: 3, ok: 3, errors: 0, prompt_tokens: 1500,
      completion_tokens: 500, total_tokens: 2000, cached_tokens: 0,
      missing_usage: 0, avg_ttfb_ms: 400, avg_duration_ms: 4000, tokens_per_sec: 2.5,
    },
    {
      key: '3a5b7c9d1e2f30415263748596a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f',
      requests: 1, ok: 0, errors: 1, prompt_tokens: 0, completion_tokens: 0,
      total_tokens: 0, cached_tokens: 0, missing_usage: 0,
      avg_ttfb_ms: 0, avg_duration_ms: 0, tokens_per_sec: 0,
    },
    {
      key: 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
      requests: 2, ok: 2, errors: 0, prompt_tokens: 600,
      completion_tokens: 200, total_tokens: 800, cached_tokens: 0,
      missing_usage: 0, avg_ttfb_ms: 0, avg_duration_ms: 0, tokens_per_sec: 0,
    },
  ],
  buckets,
  credits: {
    remain: 4049, used: 1234, samples: 5,
    per_mtoken: 310, est_tokens: 13061290, ready: true, age_sec: 120,
    // 净消耗 9.5 积分/时 → 4049/9.5 ≈ 426 小时 ≈ 17.8 天（跨度 2 小时，未满一天）
    burn_per_hour: 9.5, runway_hours: 426.2, burn_span_sec: 7200, burn_net: 19,
    // 会过期的积分：真实场景里签到发的裂变包分散在几个日期到期，金额极不均
    expiring_days: 26,
    expiring_batches: [
      { at: '2026-10-11', days: 26, remain: 1600 },
      { at: '2026-10-12', days: 27, remain: 100 },
      { at: '2026-10-13', days: 28, remain: 1500 },
      { at: '2026-10-14', days: 29, remain: 441 },
    ],
  },
};
// 密钥表（仪表盘现在展示这一维）。名字由 keyNames 映射提供。
// 必须在 renderDashboard 之前注入——渲染时读的就是它。
T.setKeyNames({
  '9f2c4a1b7e08d3f5a6c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d': '我的主力密钥',
  '3a5b7c9d1e2f30415263748596a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f': '室友共用',
});
T.renderDashboard(snap);

const cards = getEl('dashCards').innerHTML;
eq((cards.match(/<div class="stat/g) || []).length, 4, '首行 4 个英雄卡（运行时长在顶栏，不再重复成卡）');
ok(cards.includes('>4<'), 'KPI 显示请求总数 4');
ok(cards.includes('75.0%'), 'KPI 成功率 3/4 = 75.0%');
ok(cards.includes('20k'), 'KPI token 合计缩写');
ok(cards.includes('4,049'), 'KPI 剩余积分');
ok(!cards.includes('1时01分'), '运行时长只在顶栏 meta，不重复成卡');
ok(!cards.includes('输入 token') && !cards.includes('输出 token'),
  '首行不再重复输入/输出（构成条已有拆分）');

const credit = getEl('dashCredit').innerHTML;
eq((credit.match(/class="mi"/g) || []).length, 2, '次级指标带第一行 2 项（不再是卡片）');
ok(credit.includes('积分/百万token'), '单价口径为积分/百万token');
ok(credit.includes('>310.00<'), '实测单价 310 积分/百万token');
// 主数字给出量级（1306万），口径说明进 title。
ok(credit.includes('1306万'), '预估可用 token 主数字用中文单位');
ok(!credit.includes('>13.06M<'), '预估可用 token 不再用 M 缩写');
ok(cards.includes('2 分钟前'), '积分数据新鲜度提示（跟在剩余积分卡上）');

const quality = getEl('dashQuality').innerHTML;
eq((quality.match(/class="mi"/g) || []).length, 5, '次级指标带第二行 5 项');
ok(quality.includes('413ms'), '平均 TTFB');
ok(quality.includes('>2<'), '覆盖模型数 2');
ok(quality.includes('>1<'), '缺 usage 数 1');

// 列数契约：行内 <td> 必须等于 index.html 里该表的 <th> 数
// （assets_test.go 数表头，这里数表体，两边必须相等）。
const mRows = getEl('dashModelBody').innerHTML;
eq((mRows.match(/<tr>/g) || []).length, 2, '模型表两行');
eq((mRows.match(/<td\b/g) || []).length / 2, 13, '模型表每行 13 个 <td>（含积分/Mtok 列）');
ok(mRows.includes('glm-5.2'), '模型名在表里');
ok(mRows.includes('15k'), '模型合计 token');
ok(mRows.includes('75.0%'), '占比=占全池合计的百分比（15000/20000），不再是相对最大模型');
ok(!mRows.includes('100.0%'), '最大模型不再恒显 100.0%（旧口径是相对最大者归一）');
ok(mRows.includes('>3.0k<'), '缓存命中单列显示绝对量');
ok(mRows.includes('33.3%'), '缓存率单列显示百分比（模型内 3000/9000）');
ok(!mRows.includes('class="share"') && !mRows.includes('<div class="bar"'), '占比/缓存率是纯文字，不再画蓝色进度条');
ok(mRows.includes('>310000.00<'), '模型单价 4650 积分/0.015M tok = 310000 积分/百万token');
ok(mRows.includes('>—<'), '无积分记录的模型单价显示破折号');

// 密钥表（仪表盘现在展示这一维）。名字由 keyNames 映射提供，
// 最后一把**故意不给名字**，用来验证"映射缺失时回落短指纹"。
const kRows = getEl('dashKeyBody').innerHTML;
eq((kRows.match(/<td\b/g) || []).length / 3, 8, '密钥表每行 8 个 <td>（含积分列）');
ok(kRows.includes('我的主力密钥'), '密钥表显示密钥名而不是指纹');
ok(!kRows.includes('9f2c4a1b7e08d3f5'), '有名字时不重复打印完整指纹');
ok(kRows.includes('3a5b7c9d'), '映射缺失的密钥回落显示短指纹（不能留白）');
ok(kRows.includes('2.0k'), '密钥表合计 token 缩写');
ok(getEl('dashKeyNote').textContent === '3 把密钥有流量（重启后继续累计）', '密钥表附注');



// 轴上限取整到 1/2/5×10^k：否则刻度会是 287764 这种读不出来的数。
eq(T.niceMax(287764), 500000, '轴上限取整到 5×10^5');
eq(T.niceMax(1), 1, '轴上限至少 1（全零时不除零）');
eq(T.niceMax(0), 1, '零值不产生 0 或 NaN');
eq(T.niceMax(950), 1000, '接近整十的取到整十');
eq(T.niceMax(1200), 2000, '超过 1 倍进位到 2 倍档');

/* ── 4a-3. 出口列的显示名 ─────────────────────────────────────────── */
// 审计流水里存的 account 是 `uid 前 8 位`（为省磁盘截断过），而别名表按**完整 uid** 索引。
// 拿缩写去等值查永远查不到，于是「出口」列一直显示 5c805214 这种十六进制——
// 别名形同虚设。这条盯的就是「缩写也要能还原成别名」。
{
  T.setOverview({
    session_dead_threshold: 3, probe_threshold: 3,
    accounts: [{ uid: '5c805214-90a5-46f7-9e7f-30065160fba1', alias: '备用号' }],
  });
  eq(T.uidName('5c805214-90a5-46f7-9e7f-30065160fba1'), '备用号', '完整 uid → 别名');
  eq(T.uidName('5c805214'), '备用号', '8 位缩写也要能还原成别名（审计里存的就是这个）');

  // 没设别名时回落昵称，再回落 uid 前 8 位。
  T.setOverview({
    session_dead_threshold: 3, probe_threshold: 3,
    accounts: [{ uid: 'aaaaaaaa-1111-2222-3333-444444444444', nickname: '小王' }],
  });
  eq(T.uidName('aaaaaaaa'), '小王', '没别名时回落昵称');

  T.setOverview({ session_dead_threshold: 3, probe_threshold: 3, accounts: [] });
  eq(T.uidName('bbbbbbbb'), 'bbbbbbbb', '账号已移除 / overview 没拉到 → 回落前 8 位');
  eq(T.uidName(''), '', '空 uid 不炸');

  // 端到端：出口格显示**别名在上、手机号在下**，而不是那串十六进制。
  // WorkBuddy 的 nickname 字段就是手机号，那才是人能认出来的标识。
  T.setOverview({
    session_dead_threshold: 3, probe_threshold: 3,
    accounts: [{ uid: '5c805214-90a5-46f7-9e7f-30065160fba1', alias: '备用号', nickname: '18922115042' }],
  });
  const acctRow = T.useRowHTML({ ts: '2026-09-14T10:00:00+08:00', account: '5c805214', status: 200, has_usage: true });
  ok(acctRow.includes('备用号'), '出口列第一行是别名');
  ok(acctRow.includes('18922115042'), '出口列第二行是手机号');
  ok(acctRow.indexOf('备用号') < acctRow.indexOf('18922115042'), '别名在上、手机号在下');
  ok(!/<div class="nm"[^>]*>5c805214</.test(acctRow), '出口列不再把 uid 缩写当主标识');
  ok(acctRow.includes('5c805214-90a5-46f7-9e7f-30065160fba1'), '完整 uid 退到悬停提示里（核对时仍拿得到）');

  // 没设别名时，手机号直接顶上来，不留空行。
  T.setOverview({
    session_dead_threshold: 3, probe_threshold: 3,
    accounts: [{ uid: '5c805214-90a5-46f7-9e7f-30065160fba1', nickname: '18922115042' }],
  });
  const noAlias = T.useRowHTML({ ts: '2026-09-14T10:00:00+08:00', account: '5c805214', status: 200, has_usage: true });
  ok(/<div class="nm"[^>]*>18922115042</.test(noAlias), '没别名时手机号就在第一行');

  // 筛选项也得显示别名——显示 uid 缩写的话用户不知道该点哪个。
  T.setOverview({
    session_dead_threshold: 3, probe_threshold: 3,
    accounts: [{ uid: '5c805214-90a5-46f7-9e7f-30065160fba1', alias: '备用号', nickname: '18922115042' }],
  });
  T.setUseData({
    date: '2026-09-14', days: ['2026-09-14'], retention: 7, truncated: false, dropped: 0, write_errors: 0,
    summary: { requests: 2, ok: 2, failed: 0 }, users: [], models: [],
    rows: [
      { ts: '2026-09-14T10:00:00+08:00', account: '5c805214', status: 200, has_usage: true },
      { ts: '2026-09-14T10:00:01+08:00', account: '5c805214', status: 200, has_usage: true },
    ],
  });
  const acctOpts = T.fltOptions('account');
  ok(acctOpts.some(o => o.label === '备用号'), '账号筛选项显示别名');
  ok(acctOpts.some(o => o.v === '5c805214'), '筛选项的 value 仍是原始 uid（否则筛不中）');
  T.setOverview({ session_dead_threshold: 3, probe_threshold: 3, accounts: [] });
}

/* ── 4a-4. 积分构成卡片：标题回退 + 消耗条 ───────────────────────── */
// 上游 auth 文件里的 nickname 并非手机号那么干净：实测有账号的昵称是
// 4 个 U+007F(DEL)——`trim()` 去不掉控制字符、垃圾值正则也匹配不上，
// 卡片标题就渲染成一片空白（副标题反倒挂着 uid8，主次颠倒）。
// 这组断言钉住：不可见字符先剥掉，剥完为空就回退 uid8。
{
  eq(T.pkNick('\u007f\u007f\u007f\u007f'), '', 'U+007F 控制字符 → 剥完为空');
  eq(T.pkNick(' 18922115042 '), '18922115042', '正常昵称只 trim 不伤内容');
  eq(T.pkNick('ok'), 'ok', 'pkNick 只管剥字符，不判垃圾值（那是 pkTitle 的事）');

  const uidFull = 'd79a2025-e2ea-4ac8-9eea-bca7416010b3';
  eq(T.pkTitle({ uid: uidFull, nickname: '\u007f\u007f\u007f\u007f' }), { main: 'd79a2025', sub: '' },
    '控制字符昵称 → 主标题回退 uid8、副标题留空');
  eq(T.pkTitle({ uid: uidFull, nickname: '' }), { main: 'd79a2025', sub: '' }, '空昵称 → uid8');
  eq(T.pkTitle({ uid: uidFull, nickname: 'ok' }), { main: 'd79a2025', sub: '' }, '垃圾值 ok → uid8');
  eq(T.pkTitle({ uid: uidFull, nickname: '18922115042' }), { main: '18922115042', sub: 'd79a2025' },
    '正常昵称 → 昵称在上、uid8 在下供核对');
  eq(T.pkTitle({}), { main: '', sub: '' }, 'uid 缺失不炸');

  // 端到端：渲染两块账号，消耗条宽度 = 已用/总额，卡片按剩余降序。
  T.renderPackages({
    accounts: [
      {
        uid: 'aaaa0001-1111', nickname: '\u007f\u007f', realm: 'cn',
        remain: 4768, size: 4911,
        packages: [
          { name: 'CodeBuddy个人体验版', package_code: 'TCACA_code_008', size: 500, remain: 357, used: 143, created_at: '2026-09-11T17:02:00+08:00' },
          { name: 'CodeBuddy个人版国内运营裂变包', package_code: 'TCACA_code_007', size: 4411, remain: 4411, used: 0, created_at: '2026-09-11T17:02:02+08:00' },
        ],
      },
      {
        uid: 'bbbb0002-2222', nickname: '18922115042', realm: 'cn',
        remain: 100, size: 5000,
        packages: [{ name: 'CodeBuddy个人体验版', package_code: 'TCACA_code_008', size: 5000, remain: 100, used: 4900, created_at: '2026-09-12T10:00:00+08:00' }],
      },
      { uid: 'cccc0003-3333', nickname: '查询失败号', error: 'timeout' },
    ],
  });
  const html = getEl('pkSummary').innerHTML;
  ok(html.includes('#1') && html.includes('#2'), '卡片带排名角标');
  ok(html.indexOf('aaaa0001') < html.indexOf('bbbb0002'), '按剩余降序：4.8k 的号排在 100 的号前面');
  ok(!/\u007f/.test(html), '控制字符不进 HTML');
  ok(/<span class="nm">aaaa0001<\/span>/.test(html), '控制字符昵称 → 主标题是 uid8');
  ok(/<span class="nm">18922115042<\/span>/.test(html), '正常昵称 → 主标题是昵称');
  // 消耗条：aaaa 已用 143/4911=2.91%，bbbb 已用 4900/5000=98.00%
  ok(html.includes('class="used" style="width:2.91%"'), '低消耗号：已用条 2.91% 宽');
  ok(html.includes('class="used" style="width:98.00%"'), '高消耗号：已用条 98.00% 宽');
  ok(html.includes('class="left" style="width:97.09%"'), '剩余段补足 100%（2.91 + 97.09）');
  ok(html.includes('已用 <b>143</b> / 4.9k'), '消耗文案给出绝对值');
  ok(html.includes('查询失败：timeout'), '查不到的账号单独一张错误卡，不参与排名');
  ok(!/查询失败号[^<]*<\/span><\/div><div class="hero"/.test(html), '错误卡不渲染消耗条');

  // 明细区表头也用同一套标题规则（原来裸用 nickname，控制字符同样会开花）
  const detail = getEl('pkDetail').innerHTML;
  ok(/<h3>aaaa0001 · cn<\/h3>/.test(detail), '明细表头：控制字符昵称同样回退 uid8');
  ok(detail.includes('18922115042 · cn'), '明细表头：正常昵称照常用');
}

// 4a-5（添加账号选域）放在文件末尾：它依赖 statusPoll 一节里装的 fetch 桩。
// 背景：0b2103b 加了「选域 → 获取授权链接」两步流程（国际版走 workbuddy.ai、
// 落盘 auth.realm=global），但 ab7fd7c 换基座时前端整段被远端版本覆盖掉了，
// 后端 realm 分支还留着、UI 却没了——用户点「添加账号」只能拿到 CN 链接。

// 可视化：构成条三段各占其位，水位环百分比按剩余/(剩余+已用)
//
// 三段必须**互不重叠**且相加 = 100%。缓存命中是输入的一部分，不能和完整的输入并排：
// 那样三段相加会到 115%（真实数据上到过 197%），flex 再把它们按比例压扁，
// 画面就成了无意义的"各占一半"。这里直接把"加起来等于 100"钉成断言。
const viz = getEl('dashCreditViz').innerHTML;
// 存量两个大数字已去重（首行英雄卡 + 下面 bar-x 各有一份），卡片直接从额度条讲起。
ok(!viz.includes('剩余积分'), '积分卡不再重复「剩余积分」大字（英雄卡已有）');
ok(viz.includes('已耗 <b>1,234</b>') && viz.includes('剩余 <b>4,049</b>'), '已耗/剩余绝对值在额度条下方');
// 额度条：已耗 1234 / 总量 (4049+1234=5283) = 23.4%，剩余 76.6%
ok(viz.includes('class="burn"'), '额度条回归');
ok(viz.includes('i class="used"') && viz.includes('i class="free"'),
  '额度条分「已耗 / 剩余」两段（不同纹理，不只靠颜色）');
ok(viz.includes('23.4%') && viz.includes('76.6%'), '两段都标百分比');
// 快照里 burn_per_hour=9.5 → 4049/9.5 ≈ 426 小时 ≈ 17.8 天
ok(viz.includes('17.8') && viz.includes('<span class="unit"> 天</span>'),
  '剩余可用：数值与单位分开，单位走小字号');
ok(viz.includes('剩余可用'), '有「剩余可用」字段，不用含糊的"平均消耗"');
ok(viz.includes('−228<span class="unit"> 积分/天</span>'), '净消耗速率按天显示（9.5/时 × 24 = 228/天）');
ok(viz.includes('不足一天'), '样本不足 24 小时时明确标注「不足一天」');

// 无限循环建议：burn=9.5 积分/时、1 个能签到的号
//   日净消耗 = 9.5×24 = 228；228÷100 = 2.28 → 补 3 个；共 1+3 = 4 个ok(viz.includes('账号数建议'), '给出账号数建议');
ok(viz.includes('4 个'), '建议号数 = 现有 1 + ceil(228/100) = 4');
ok(viz.includes('再补 3 个'), '一句话说清还要补几个');
// 口径说明放进问号气泡：常驻会变噪音，需要时才看
ok(viz.includes('class="q"'), '账号数建议旁边有圈问号');
ok(viz.includes('data-tip='), '问号里带提示文本');
ok(viz.includes('惯例估值'), '气泡里说明了 100 是估值不是实测');
ok(!viz.includes('惯例值</div>'), '口径说明不再常驻占版面');

// 到期提醒已搬到顶栏（#dashExpiry），**只显示最快到期的一条**，点开看完整清单。
// 26 天 → 绿档（充裕）
const chip = getEl('dashExpiry');
ok(chip.className.includes('ok'), '26 天到期走绿档（>10 天充裕）');
ok(!chip.hidden, '有临期积分时卡片显示');
ok(chip.innerHTML.includes('充裕'), '绿档标记"充裕"');
// 只报最上面那条（1,600 / 26 天），不是把所有日期都铺开
ok(chip.innerHTML.includes('1,600'), '显示最快到期那一条的金额');
ok(chip.innerHTML.includes('26 天后到期'), '显示最快到期那一条的天数');
ok(!chip.innerHTML.includes('2026-10-13'), '卡片上不铺开其它到期日（避免撑高顶栏）');
// 但必须写出"还有几笔 + 合计"，否则只看 1,600 会低估风险
ok(chip.innerHTML.includes('等 4 笔'), '写出还有几笔可展开');
ok(chip.innerHTML.includes('共 3,641'), '写出待到期合计（不是只给第一条）');
// 合计 1600+100+1500+441 = 3641，占余额 3641/4049 = 90%
ok(!viz.includes('class="expiry'), '积分卡里不再重复渲染到期块');

// 点击卡片 → 打开明细弹窗，逐日列出
T.openExpiryDetail();
ok(getEl('expVeil').classList.contains('on'), '点击卡片打开明细弹窗');
const expBody = getEl('expBody').innerHTML;
eq((expBody.match(/<tr>/g) || []).length, 4, '弹窗里逐日列出全部 4 个到期日');
ok(expBody.includes('2026-10-11'), '弹窗含第 1 个到期日');
ok(expBody.includes('2026-10-12'), '弹窗含第 2 个到期日');
ok(expBody.includes('2026-10-13'), '弹窗含第 3 个到期日');
ok(expBody.includes('2026-10-14'), '弹窗含第 4 个到期日');
ok(getEl('expSummary').textContent.includes('4 个到期日'), '弹窗摘要给出笔数');
ok(getEl('expSummary').textContent.includes('3,641'), '弹窗摘要给出合计');
ok(getEl('expSummary').textContent.includes('90%'), '弹窗摘要给出占余额比');
T._closeExpiryDetail();
ok(!getEl('expVeil').classList.contains('on'), '关闭后弹窗消失');

// 三档边界：11 天绿、10 天黄、3 天红（档位取**最近一批**的天数）
function chipAt(days) {
  T.renderDashboard({
    uptime_sec: 90000, totals: {}, models: [], accounts: [], buckets: [],
    credits: {
      remain: 10000, used: 100, samples: 9, per_mtoken: 310, est_tokens: 0, ready: true,
      age_sec: 60, burn_per_hour: 50, runway_hours: 200, burn_span_sec: 90000, burn_net: 1000,
      expiring_days: days,
      expiring_batches: [{ at: '2026-10-13', days: days, remain: 9000 }],
    },
  });
  return getEl('dashExpiry');
}
ok(chipAt(11).className.includes('ok'), '11 天 → 绿（边界上界）');
ok(chipAt(11).innerHTML.includes('充裕'), '11 天文案"充裕"');
ok(chipAt(10).className.includes('warn'), '10 天 → 黄（含边界）');
ok(chipAt(10).innerHTML.includes('尽快用'), '10 天文案"尽快用"');
ok(chipAt(4).className.includes('warn'), '4 天 → 黄');
ok(chipAt(3).className.includes('bad'), '3 天 → 红（含边界）');
ok(chipAt(3).innerHTML.includes('快过期'), '3 天文案"快过期"');
ok(chipAt(0).className.includes('bad'), '今天到期 → 红');
ok(chipAt(0).innerHTML.includes('今天'), '0 天显示"今天"而不是"0 天"');

// 空批次列表（有 days 但没数据）也要隐藏，避免出现空壳卡片
T.renderDashboard({
  uptime_sec: 90000, totals: {}, models: [], accounts: [], buckets: [],
  credits: {
    remain: 10000, used: 100, samples: 9, per_mtoken: 310, est_tokens: 0, ready: true,
    age_sec: 60, burn_per_hour: 50, runway_hours: 200, burn_span_sec: 90000, burn_net: 1000,
    expiring_days: 5, expiring_batches: [],
  },
});
ok(chip.hidden, '批次为空时隐藏（不能只凭 expiring_days 判断）');

// 没有会过期的批次 → 整块隐藏
T.renderDashboard({
  uptime_sec: 90000, totals: {}, models: [], accounts: [], buckets: [],
  credits: {
    remain: 10000, used: 100, samples: 9, per_mtoken: 310, est_tokens: 0, ready: true,
    age_sec: 60, burn_per_hour: 50, runway_hours: 200, burn_span_sec: 90000, burn_net: 1000,
    expiring_days: -1, expiring_batches: [],
  },
});
ok(chip.hidden, '全是长期额度时卡片隐藏');
ok(chip.textContent === '' && chip.innerHTML === '', '隐藏时不留残渣');

// 净增长时不建议加号，而是说已达成循环
T.renderDashboard({
  uptime_sec: 3600, totals: {}, models: [], accounts: [], buckets: [],
  credits: {
    remain: 45200, used: 100, samples: 9, per_mtoken: 310, est_tokens: 0, ready: true,
    age_sec: 60, burn_per_hour: -120, runway_hours: 0, burn_span_sec: 7200, burn_net: -240,
  },
});
const gainViz = getEl('dashCreditViz').innerHTML;
ok(gainViz.includes('净增长'), '余额在涨时显示"净增长"而不是倒计时');
ok(!gainViz.includes('预计还能用'), '净增长时不报"还能用多久"（那是错的）');
ok(gainViz.includes('+2,880<span class="unit"> 积分/天</span>'), '净增长按天显示为正速率（120/时 × 24）');
ok(gainViz.includes('无限循环'), '净增长时直接说已达成无限循环，不建议加号');
ok(!gainViz.includes('再补'), '已达成循环时不该再让人补号');

// 只采到一个点：明确说"下一次刷新就能出速率"，不能编数字、也不能说"攒够 1 小时"
// （门槛已从 1 小时降到 1 分钟，文案必须跟着改，否则误导用户以为要等很久）
T.renderDashboard({
  uptime_sec: 600, totals: {}, models: [], accounts: [], buckets: [],
  credits: {
    remain: 45000, used: 10, samples: 1, per_mtoken: 300, est_tokens: 0, ready: true,
    age_sec: 30, burn_per_hour: 0, runway_hours: 0, burn_span_sec: 0, burn_net: 0,
  },
});
const onePt = getEl('dashCreditViz').innerHTML;
ok(onePt.includes('才采到 1 个点'), '只有一个采样点时说明下一次就能出速率');
ok(!onePt.includes('攒够 1 小时'), '不再声称要等 1 小时');

// 满一天（25 小时跨度）：去掉「不足一天」的琥珀标注
T.renderDashboard({
  uptime_sec: 90000, totals: {}, models: [], accounts: [], buckets: [],
  credits: {
    remain: 45000, used: 1000, samples: 300, per_mtoken: 300, est_tokens: 0, ready: true,
    age_sec: 20, burn_per_hour: 100, runway_hours: 450, burn_span_sec: 90000, burn_net: 2500,
  },
});
const fullDayViz = getEl('dashCreditViz').innerHTML;
ok(fullDayViz.includes('实测 1.0 天'), '满一天后标注实测天数');
ok(!fullDayViz.includes('不足一天'), '满一天后不再标「不足一天」');

// 未满一天（5 小时）：出数，但必须标注「不足一天」+ 气泡里补充口径
T.renderDashboard({
  uptime_sec: 18000, totals: {}, models: [], accounts: [], buckets: [],
  credits: {
    remain: 45000, used: 1000, samples: 60, per_mtoken: 300, est_tokens: 0, ready: true,
    age_sec: 20, burn_per_hour: 250, runway_hours: 180, burn_span_sec: 18000, burn_net: 1250,
  },
});
const shortSpan = getEl('dashCreditViz').innerHTML;
ok(shortSpan.includes('样本 5.0 小时'), '5 小时窗口如实标注实际样本时长');
ok(shortSpan.includes('不足一天'), '未满一天时标注「不足一天」（签到脉冲还没被平均掉）');
ok(shortSpan.includes('−6,000<span class="unit"> 积分/天</span>'), '日速率 = 250/时 × 24');
ok(shortSpan.includes('7.5<span class="unit"> 天</span>'), '续航 180 小时 → 7.5 天');
ok(!shortSpan.includes('才采到 1 个点'), '有速率时不再说样本不足');
ok(shortSpan.includes('签到脉冲还没被平均掉'),
  '未满一天时，账号数建议的气泡里也说明结论更粗');

// 空状态：没数据时不能留空表，也不能报错
T.renderDashboard({
  uptime_sec: 5, totals: {}, models: [], accounts: [], keys: [], buckets: [],
  credits: { remain: 0, used: 0, samples: 0, per_mtoken: 0, est_tokens: 0, ready: false, age_sec: -1 },
});
ok(getEl('dashModelBody').innerHTML.includes('还没有模型用量'), '空模型表给提示');
ok(getEl('dashKeyBody').innerHTML.includes('还没有密钥用量'), '空密钥表给提示');
ok(getEl('dashModelBody').innerHTML.includes('colspan="13"'), '空模型行 colspan=13（含积分/Mtok 列）');
ok(getEl('dashKeyBody').innerHTML.includes('colspan="8"'), '空密钥行 colspan=8（含积分列）');
ok(getEl('dashCredit').innerHTML.includes('—'), '无样本时预估显示破折号而不是 0');
ok(getEl('dashCards').innerHTML.includes('—'), '零请求时成功率显示破折号');
ok(getEl('dashCreditViz').innerHTML.includes('还没有积分数据'), '无积分时给提示');

/* ── 5b. 小额费率：两张推算卡必须照常出数 ──────────────────────────
   判据曾经写成 `per_ktoken > 0`（千token 口径，数值万分位）。缓存命中率越高
   该值越小，一旦后端把它舍成 0，这个判据就把「算出来了」误判成「样本不够」
   ——两张卡双双显示「—」「待累积样本」，而 samples 早就有 182 个。用户等的
   是一个永远不会自己出现的数字。口径现为积分/百万token（per_mtoken），
   但千分位以下仍可能被极端命中率打出，回归断言保留。
   放在这里（仪表盘断言之后）：它要重渲染整份快照，会覆盖上面用的 snap。 */
T.renderDashboard({
  uptime_sec: 90000, totals: {}, models: [], accounts: [], buckets: [],
  credits: {
    remain: 46842, used: 1061, samples: 182,
    per_mtoken: 1.1, est_tokens: 42138357788, ready: true, age_sec: 240,
    burn_per_hour: -29.68, runway_hours: 0, burn_span_sec: 82813, burn_net: -683,
  },
});
const smallRate = getEl('dashCredit').innerHTML;
ok(smallRate.includes('积分/百万token'), '单价卡标题为「积分/百万token」口径');
ok(smallRate.includes('>1.10<'), '百万token 口径单价两位小数显示（1.1 → 1.10）');
ok(!smallRate.includes('>0.00<'), '单价不能被显示成 0.00（那看着像没数据）');
ok(smallRate.includes('421.38亿'), '小额费率下预估可用 token 照常外推');
ok(smallRate.includes('42,138,357,788 token'), '小额费率下也给出完整数值');
ok(!smallRate.includes('待累积样本'), '样本够时不许再提示「待累积样本」');
ok(!smallRate.includes('样本不足'), 'samples>0 时不许提示「样本不足」');
// 密钥表积分列：credits 来自 metrics 按密钥累计（上游 usage.credit 求和）。
// 主快照的 keys 无 credits 字段 → 显示「—」。
ok(kRows.includes('>—<'), '无积分数据时显示破折号');
T.renderDashboard({
  uptime_sec: 3660, totals: {}, accounts: [], buckets: [], models: [],
  credits: { remain: 0, used: 0, samples: 0, age_sec: -1 },
  keys: [
    { key: '9f2c4a1b7e08d3f5a6c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d', requests: 10, total_tokens: 5000, prompt_tokens: 4000, completion_tokens: 1000, credits: 12.5 },
    { key: '3a5b7c9d1e2f30415263748596a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f', requests: 2, total_tokens: 900, prompt_tokens: 800, completion_tokens: 100 },
  ],
});
const kRows2 = getEl('dashKeyBody').innerHTML;
ok(kRows2.includes('>12.50<'), '密钥积分列显示上游实扣合计（最多两位小数）');
ok(kRows2.includes('>—<'), '没有积分记录的密钥显示破折号而不是 0');

// 模型表折叠：>3 个模型默认只显示前三 + 一行「展开」
const manyModels = ['alpha', 'beta', 'gamma', 'delta', 'epsilon', 'zeta'].map((n, i) => ({
  key: n, requests: 10 - i, total_tokens: 10000 - i * 1000,
  prompt_tokens: 9000 - i * 900, completion_tokens: 1000, cached_tokens: 0, errors: 0,
}));
T.renderDashboard({ uptime_sec: 3660, totals: {}, accounts: [], buckets: [], keys: [], credits: { remain: 0, used: 0, samples: 0, age_sec: -1 }, models: manyModels });
const mRows2 = getEl('dashModelBody').innerHTML;
eq((mRows2.match(/<tr\b/g) || []).length, 4, '6 个模型默认 3 行数据 + 1 行折叠行');
ok(mRows2.includes('展开其余 3 个模型'), '折叠行写明还剩几个');
ok(mRows2.includes('alpha') && mRows2.includes('gamma') && !mRows2.includes('delta'), '默认只展示前三名');
// 点「展开」→ 全量显示，按钮变「收起」
T.flipModels();
const mRows3 = getEl('dashModelBody').innerHTML;
eq((mRows3.match(/<tr\b/g) || []).length, 7, '展开后 6 行数据 + 1 行收起行');
ok(mRows3.includes('收起'), '展开后按钮变「收起」');
ok(mRows3.includes('delta'), '展开后能看到第 4 名以后的模型');
T.flipModels();
ok(getEl('dashModelBody').innerHTML.includes('展开其余 3 个模型'), '再点一次回到折叠态');

// 积分流水图：消耗（审计逐日 credit）+ 获取（余额净变化 + 消耗）双序列。
// dailyNet 今天没有采样点时，今天的获取用最新一天的 net 兜底，否则恒为 0。
T.setTrendDays([// 模拟 loadTrend 拉回的数据
  { date: '2026-09-15', credit: 100 },
  { date: '2026-09-16', credit: 50 },
  { date: '2026-09-17', credit: 20 },
]);
T.setDailyNet([
  { at: new Date('2026-09-15T00:00:00').getTime() / 1000, net: -400 },
  { at: new Date('2026-09-16T00:00:00').getTime() / 1000, net: 900 },
]);
T.renderCreditBurn();
const flow = getEl('dashCreditBurn').innerHTML;

ok(flow.includes('ch-earn'), '流水图有「获取」绿色序列');
ok(flow.includes('ch-spent'), '流水图消耗是蓝色实线（与获取同构，不再混排面积）');
ok(flow.includes('ch-dot3-blue'), '消耗线带同规格采样点');
ok(!flow.includes('grad-flow') && !flow.includes('ch-area'), '不再有渐变面积（两种视觉语言混排已撤）');
ok(getEl('dashBurnSub').textContent.includes('1 天净亏'), '有净亏日时副行标出「N 天净亏」');
// 口径：09/15 获取 = net(-400)+消耗(100) = -300（净消耗日照实画负贡献）；
// 09/16 = 900+50 = 950；09/17（今天，无采样）用最新 net 900 兜底 = 920。
// 合计获取 = -300+950+920 = 1570。
ok(flow.includes('获取 1570'), '7 天获取合计 = (-300)+950+920 = 1570');
ok(flow.includes('消耗 170'), '7 天消耗合计 = 100+50+20 = 170');


/* ── 6. 全局状态轮询：任一视图都不能让侧边栏冻住 ──────────────────── */
console.log('statusPoll');
// fetch 桩：记录调用路径，并按路径回不同载荷。
// app.js 的 api() 每次都从全局取 fetch，所以加载后再替换是生效的。
const fetched = [];
const fetchCalls = []; // {url, opts} —— 需要看 POST body 的用例从这里取
// 选域弹窗里「用户点了哪个 radio」：startAddLogin 会 querySelector 读它。
// 桩按选择器返回一个可控的假 radio，测试用 __setAddRealm 切换。
// 注意 openAdd 会把勾选重置回 CN（value="cn" 那条选择器）——桩也要支持，
// 否则看不到「重开弹窗 → 选域归位」这个行为。
let addRealmPick = 'cn';
sandbox.document.querySelector = sel => {
  const s = String(sel);
  if (s.includes('addRealm')) {
    if (s.includes('value="cn"')) addRealmPick = 'cn'; // openAdd 的归位动作
    return { value: addRealmPick, checked: true, dispatchEvent() {} };
  }
  return null;
};
sandbox.__setAddRealm = v => { addRealmPick = v; };
sandbox.__getAddRealm = () => addRealmPick;
const overviewPayload = {
  total: 3, healthy: 0, cooling: 1, disabled: 2, sticky_sessions: 4,
  version: '9.9.9', redis_mode: 'upstash', uptime_sec: 90061, in_flight_full: 0,
  accounts: [{ uid: 'u1', credits: 100 }, { uid: 'u2', credits: 25 }],
};
const statsPayload = {
  ok: true,
  stats: { uptime_sec: 1, totals: {}, models: [], accounts: [], buckets: [], credits: {} },
};
sandbox.fetch = (url, opts) => {
  const u = String(url);
  fetched.push(u);
  fetchCalls.push({ url: u, opts: opts || {} });
  let body = u.endsWith('/overview') ? overviewPayload : statsPayload;
  if (u.endsWith('/login/start')) body = { ok: true, url: 'https://example.test/auth?state=s1', state: 's1', realm: 'cn' };
  // poll 的 URL 带 ?state=…，所以用 includes 而不是 endsWith（endsWith 匹配不上）。
  if (u.includes('/login/poll')) body = { done: true, uid: 'u9', nickname: '新号', realm: 'global', credits: 100, credits_total: 500 };
  return Promise.resolve({ status: 200, ok: true, json: () => Promise.resolve(body),
    headers: { get: () => null } });   // api() 会读 X-Data-Time；桩没有缓存头，返回 null 即可
};

// applyStatus：这是侧边栏 / 顶栏 / 计数卡的唯一更新入口
T.applyStatus(overviewPayload);
eq(getEl('navState').textContent, '无可用账号', 'navState：有账号但无可用');
eq(getEl('navVer').textContent, 'v9.9.9', 'navVer 版本');
eq(getEl('navSub').textContent, '非官方控制台', 'navSub 显示「非官方控制台」（版本号在大字标题与左下角）');
eq(getEl('navRedis').textContent, 'Redis 镜像', 'navRedis 认 upstash');
ok(getEl('navPulse').className.includes('warn'), '有账号但全不可用 → pulse warn');
eq(getEl('subMeta').textContent, '运行 1 天 1 时 1 分', 'subMeta 运行时长');
eq(getEl('sTotal').textContent, 3, '计数卡 total');
eq(getEl('sCredits').textContent, 125, '计数卡积分合计（100+25）');

T.applyStatus(Object.assign({}, overviewPayload, { total: 0, healthy: 0, accounts: [] }));
eq(getEl('navState').textContent, '待添加账号', 'navState：一个号都没有');
ok(getEl('navPulse').className.includes('bad'), '无账号 → pulse bad');
eq(getEl('sCredits').textContent, 0, '计数卡积分归零');

T.applyStatus(Object.assign({}, overviewPayload, { healthy: 2 }));
eq(getEl('navState').textContent, '服务正常', 'navState：有可用号');

// 实时并发：各号在途求和；有并发时数字变绿、并列出正在服务的号
T.applyStatus(Object.assign({}, overviewPayload, {
  healthy: 2,
  accounts: [
    { uid: 'u1', credits: 100, in_flight: 2 },
    { uid: 'u2', credits: 25, in_flight: 1 },
  ],
}));
eq(getEl('navInflight').textContent, 3, '并发 = 各号在途之和（2+1）');
ok(getEl('navInflight').className.includes('hot'), '有并发时并发数字高亮');
eq(getEl('navInflightWho').textContent, 'u1×2、u2', '列出正在服务的号，多个在途带 ×N');

T.applyStatus(Object.assign({}, overviewPayload, {
  healthy: 2,
  accounts: [{ uid: 'u1', credits: 100, in_flight: 0 }],
}));
eq(getEl('navInflight').textContent, 0, '无并发时显示 0');
ok(!getEl('navInflight').className.includes('hot'), '无并发时不加高亮');
eq(getEl('navInflightWho').textContent, '', '无并发时不列号');
eq(getEl('navPulse').className, 'pulse', '有可用号 → pulse 无告警类');

// refreshVisible 的行为随当前视图变化。harness 里 location.hash='' →
// app.js 末尾 go('dashboard') 把 view 设成了 dashboard，所以这里测的就是仪表盘分支。
// 仪表盘必须同时拉 overview（全局状态）与 stats（用量）——只拉 stats 的话，
// 侧边栏的连接指示会永远停在首屏那一刻。
fetched.length = 0;
T.refreshVisible();
ok(fetched.some(u => u.endsWith('/overview')), '仪表盘轮询要拉 overview（否则侧边栏冻住）');
ok(fetched.some(u => u.endsWith('/stats')), '仪表盘轮询要拉 stats');
eq(fetched.length, 2, '仪表盘一轮轮询恰好 2 个请求');

/* ── 使用日志（请求审计流水）───────────────────────────────────────── */
// 这段盯的是「一行的数据到底对不对」：身份（谁）、用量（含真实积分）、
// 失败/缺用量/慢请求的分档，以及空状态与 CSV 的表头列数。
console.log('usageLog');
const urows = [
  { ts: '2026-09-14T16:02:11+08:00', user: '我', model: 'glm-5.3', account: 'cb1a8f38', status: 200, mode: 'stream', has_usage: true, prompt: 470767, completion: 1070, cached: 455000, credit: 0.71, ttfb_ms: 3607, total_ms: 8560, client: 'curl/8.5.0' },
  { ts: '2026-09-14T16:01:03+08:00', user: '第二台设备', model: 'hy3', account: '4863bb32', status: 200, mode: 'stream', has_usage: true, prompt: 488, completion: 8, cached: 0, credit: 0, ttfb_ms: 1200, total_ms: 2100, client: 'OpenAI/Python 1.30' },
  { ts: '2026-09-14T16:00:00+08:00', user: '第二台设备', model: 'hy3', account: '', status: 429, mode: 'stream', has_usage: false, total_ms: 150, client: '' },
  { ts: '2026-09-14T15:59:00+08:00', user: '我', model: 'glm-5.3', account: 'cb1a8f38', status: 200, mode: 'sync', has_usage: true, prompt: 100, completion: 5, cached: 80, credit: 0.02, total_ms: 41000, client: '' },
  { ts: '2026-09-14T15:58:00+08:00', user: '我', model: 'glm-5.3', account: 'cb1a8f38', status: 200, mode: 'stream', has_usage: false, total_ms: 900, client: '' },
];
T.setUseData({
  date: '2026-09-14', days: ['2026-09-14', '2026-09-13'], retention: 7,
  truncated: false, dropped: 0, write_errors: 0,
  summary: { requests: 5, ok: 4, failed: 1 },
  users: [{ name: '我', requests: 3 }, { name: '第二台设备', requests: 2 }],
  models: [{ name: 'glm-5.3', requests: 3 }, { name: 'hy3', requests: 2 }],
  rows: urows,
});
T.setUseUser(''); T.setUseModel(''); T.setUseStatus('all'); T.setUseQuery('');

eq(T.fmtCredit(0), '0', '积分 0 就显示 0');
eq(T.fmtCredit(0.01), '0.01', '积分保留小数（上游给 0.01 就显示 0.01，不四舍五入成 0）');
eq(T.fmtCredit(3), '3', '整数积分不补 .00');
eq(T.fmtCredit(12.34), '12.34', '两位小数原样显示');

eq(T.useUserName({}), '默认', '无 user 的老流水回落「默认」（与后端分桶同口径）');
eq(T.useModelName({ model: '-' }), '未知', '模型占位符回落「未知」');
eq(T.useSlow({ ttfb_ms: 9999 }), false, '首字 9999ms 不算慢');
eq(T.useSlow({ ttfb_ms: 10000 }), true, '首字 10s 算慢');
eq(T.useSlow({ total_ms: 30000 }), true, '总耗时 30s 算慢');

eq(T.useRows().length, 5, '无筛选时全部返回');
T.setUseUser('第二台设备'); eq(T.useRows().length, 2, '按用户筛选——这就是"区分你和你第二台设备"');
T.setUseUser(''); T.setUseModel('glm-5.3'); eq(T.useRows().length, 3, '按模型筛选');
T.setUseModel('');
// 状态按 HTTP 段分档（与参考设计同口径）。夹具里那条 429 属于 4xx。
T.setUseStatus('ok'); eq(T.useRows().length, 4, '只看 2xx 成功');
T.setUseStatus('client'); eq(T.useRows().length, 1, '只看 4xx 客户端错误');
T.setUseStatus('server'); eq(T.useRows().length, 0, '只看 5xx（夹具里没有）');
T.setUseStatus('other'); eq(T.useRows().length, 0, '只看其它非 2xx（夹具里没有）');
// 两个排查档：它们和 HTTP 段有重叠，是"另一条看数据的角度"而不是互斥的类型。
T.setUseStatus('missing'); eq(T.useRows().length, 1, '只看 200 却没有 usage 的（上游异常信号）');
T.setUseStatus('slow'); eq(T.useRows().length, 1, '只看慢请求');
// 非法取值不能把表清空，也不能把全部放行——必须是明确的一种行为。
T.setUseStatus('不存在的档'); eq(T.useRows().length, 5, '未知状态值按「全部」处理（不把表清空）');
T.setUseStatus('all'); T.setUseQuery('curl'); eq(T.useRows().length, 1, '搜索客户端');
T.setUseQuery(''); T.setUseUser('我'); T.setUseQuery('hy3');
eq(T.useRows().length, 0, '多个筛选条件是「与」的关系（我 + hy3 无交集）');
T.setUseUser(''); T.setUseQuery('');

const agg = T.useAgg(T.useRows());
eq(agg.n, 5, '汇总条数');
eq(agg.ok, 4, '汇总成功数');
eq(agg.fail, 1, '汇总失败数');
eq(agg.miss, 1, '汇总缺 usage 数');
eq(agg.prompt, 471355, '汇总输入 token');
eq(agg.completion, 1083, '汇总输出 token');
eq(agg.cached, 455080, '汇总缓存 token');
ok(Math.abs(agg.credit - 0.73) < 1e-9, `汇总消耗积分应为 0.73，实际 ${agg.credit}`);
eq(agg.ttfbAvg, 2404, '平均首字只统计有样本的两次 (3607+1200)/2');

// 单行的 <td> 数必须等于表头 <th> 数，否则整张表右移错位。
eq((T.useRowHTML(urows[0]).match(/<td/g) || []).length, 8, '明细行 8 个单元格（与表头 8 列一致）');
ok(T.useRowHTML(urows[0]).includes('0.71'), '行内显示本次消耗积分');
ok(T.useRowHTML(urows[0]).includes('sdot ok'), '200 用绿点');
ok(T.useRowHTML(urows[2]).includes('sdot bad'), '429 用红点');
ok(T.useRowHTML(urows[2]).includes('row-bad'), '失败行整行标色');
ok(T.useRowHTML(urows[4]).includes('row-miss'), '缺用量行单独标色（上游异常的早期信号）');
ok(T.useRowHTML(urows[4]).includes('—'), '缺用量时 token 显示破折号，不假装是 0');
ok(T.useRowHTML(urows[3]).includes('slow'), '超过 30s 的总耗时标黄');

T.setUseUser(''); T.setUseStatus('all'); T.setUseQuery('');
T.renderUsage();
const ubody = getEl('useBody').innerHTML;
eq((ubody.match(/<tr/g) || []).length, 5, '渲染 5 条明细');
ok(ubody.includes('第二台设备'), '明细里能看到用户');
// 四格 KPI + token 细条（取代原来的六张汇总卡）
ok(getEl('useKpi').innerHTML.includes('0.73'), 'KPI 显示消耗积分');
ok(getEl('useTokStrip').innerHTML.includes('455,080'), 'token 细条显示缓存命中量');
ok(getEl('useTokStrip').innerHTML.includes('输入') && getEl('useTokStrip').innerHTML.includes('输出')
  && getEl('useTokStrip').innerHTML.includes('推理'), 'token 细条有输入/输出/缓存/推理四格');
ok(getEl('useCount').textContent.includes('5'), '条数说明');
ok(getEl('useDate').innerHTML.includes('2026-09-14'), '日期下拉含当天');
eq(getEl('useNote').textContent.includes('保留 7 天'), true, '页头注明保留天数');

// 筛选菜单：五个分类，且每个分类都能列出可选项（含"全部"）。
T.renderUsage();
const menu = getEl('fltMenu').innerHTML;
for (const cat of ['模型', '状态', '模式', '密钥', '账号']) {
  ok(menu.includes('>' + cat + '<'), `筛选菜单有「${cat}」分类`);
}
// 未筛选时按钮不亮角标
eq(getEl('fltCount').hidden, true, '没有生效筛选时不显示角标');
T.setUseModel('hy3'); T.setUseStatus('all'); T.renderUsage();
eq(getEl('fltCount').hidden, false, '有生效筛选时显示角标');
eq(getEl('fltCount').textContent, '1', '角标数字 = 生效筛选个数');
ok(getEl('btnUseFilter').classList.contains('active'), '有筛选时按钮高亮');
ok(getEl('useNote').textContent.includes('模型 hy3'), '页头写明当前筛选了什么');
T.setUseModel(''); T.renderUsage();

// 模式（流式/同步）与出口账号：两个新维度，各自独立生效。
T.setUseMode('sync'); eq(T.useRows().length, 1, '只看同步请求');
T.setUseMode('stream'); eq(T.useRows().length, 4, '只看流式请求');
T.setUseMode('all'); eq(T.useRows().length, 5, '模式回到全部');
T.setUseAccount('cb1a8f38'); eq(T.useRows().length, 3, '只看某个出口账号');
T.setUseAccount('');
// 多个维度是「与」的关系
T.setUseUser('我'); T.setUseMode('stream');
eq(T.useRows().length, 2, '密钥 + 模式是「与」的关系');
T.setUseUser(''); T.setUseMode('all');
eq(T.useActiveFilters().length, 0, '清空后没有生效筛选');

// 清除筛选：没筛东西时不显示；筛了才出现；点一下全清（含搜索框）。
eq(getEl('btnUseClear').hidden, true, '没有筛选时不显示「清除筛选」');
T.setUseModel('hy3'); T.setUseQuery('curl');
T.renderUsage();
eq(getEl('btnUseClear').hidden, false, '有筛选时出现「清除筛选」');
// 角标只数菜单里的五个维度；「清除筛选」还额外覆盖搜索框——两者口径**故意不同**：
// 角标回答"我筛了几个维度"，按钮回答"现在表被什么限制着"。
eq(T.useActiveFilters().length, 1, '角标只数菜单维度（模型），不含搜索框');
eq(T.useHasAnyFilter(), true, '搜索框也算「有筛选」（否则只打字时没有清除按钮）');
T.clearUseFilters();
eq(T.useActiveFilters().length, 0, '清除后没有生效筛选');
eq(getEl('btnUseClear').hidden, true, '清除后按钮自己隐藏');
eq(getEl('useSearch').value, '', '清除筛选要连搜索框一起清（否则表格还是空的，看着像没生效）');
eq(T.useRows().length, 5, '清除后回到全部 5 条');

// 用量格：四个小块各自带底色，边界靠底色而不是靠猜。
const ug = T.useRowHTML(urows[0]).match(/<span class="u">/g) || [];
eq(ug.length, 4, '用量格是 4 个独立的块（输入/输出/缓存/积分）');

// 筛选菜单的交互：开 / 悬停展开分类 / 点选项生效并关闭
T.setUseModel(''); T.renderUsage();
// 假 DOM 给每个元素都初始化成 hidden=false，而真实页面里这个菜单带 hidden 属性。
// 手动对齐初始状态，否则"开/关"这组断言测的是假 DOM 的默认值。
getEl('fltMenu').hidden = true;
eq(T.filterMenuOpen(), false, '初始菜单是关的');
T.openFilterMenu();
eq(T.filterMenuOpen(), true, '点按钮后菜单打开');
eq(getEl('btnUseFilter').getAttribute('aria-expanded'), 'true', '无障碍属性同步');
ok(getEl('fltMenu').innerHTML.includes('状态'), '菜单渲染出分类');
// 状态分类的可选项必须按同一套判定算计数（菜单写 1，筛出来就得是 1 条）
const stOpts = T.fltOptions('status').filter(o => !o.sep);
const clientOpt = stOpts.filter(o => o.v === 'client')[0];
eq(clientOpt.n, 1, '状态选项的计数与筛选结果一致（4xx = 1 条）');
eq(stOpts.filter(o => o.v === 'all')[0].n, 5, '「全部」的计数等于全天请求数');
T.closeFilterMenu();
eq(T.filterMenuOpen(), false, '关闭后菜单收起');
eq(getEl('btnUseFilter').getAttribute('aria-expanded'), 'false', '关闭后无障碍属性也复位');

// 筛不出结果时给的是"空状态"而不是一张没有表头的破表：colspan 必须等于列数。
T.setUseQuery('绝不匹配的关键词');
T.renderUsage();
ok(getEl('useBody').innerHTML.includes('colspan="8"'), '空状态 colspan=8（与表头列数一致）');
T.setUseQuery(''); T.renderUsage();

const csv = T.useCSV(urows);
eq(csv.charCodeAt(0), 0xFEFF, 'CSV 带 UTF-8 BOM——否则 Excel 打开中文是乱码');
ok(csv.indexOf('消耗积分') > 0, 'CSV 表头含积分列');
eq(csv.trim().split('\r\n').length, 6, 'CSV = 表头 + 5 行');
T.setUseUser('第二台设备');
eq(T.useCSV(T.useRows()).trim().split('\r\n').length, 3, 'CSV 只导出当前筛选后的记录');
T.setUseUser('');

/* ── 密钥页 ───────────────────────────────────────────────────────── */
// 盯三件事：行模板列数、默认密钥那行不能出现"删除"、以及「没人用过」要说出来。
console.log('keysPage');
const owner = { id: 'aaa111', name: '主号', key: 'sk-owner-secret-value', masked: 'sk-own……alue', owner: true, requests: 2, failed: 0, credit: 0.03 };
const guest = { id: 'bbb222', name: '访客', key: 'sk-guest-secret-value', masked: 'sk-gue……alue', owner: false, requests: 9, failed: 2, credit: 1.25 };
const idle = { id: 'ccc333', name: '没人用的', key: 'sk-idle-secret-value', masked: 'sk-idl……alue', owner: false, requests: 0, failed: 0, credit: 0 };

const ownerRow = T.keyRowHTML(owner);
const guestRow = T.keyRowHTML(guest);
const idleRow = T.keyRowHTML(idle);

eq((ownerRow.match(/<td/g) || []).length, 5, '密钥行 5 个单元格（与表头 5 列一致）');
eq((guestRow.match(/<td/g) || []).length, 5, '额外密钥行同样是 5 个单元格');

ok(ownerRow.includes('class="own"'), '默认密钥那行带标记（视觉上区分"这不是管理对象"）');
ok(!ownerRow.includes('data-act="del"'), '默认密钥不能有删除按钮——它同时是面板密码');
ok(ownerRow.includes('在「设置」里改'), '默认密钥给出"去哪儿改"的指引，而不是留空');
ok(guestRow.includes('data-act="del"'), '额外密钥有删除按钮');
ok(guestRow.includes('data-act="edit"'), '额外密钥有编辑按钮');
ok(guestRow.includes('data-act="share"'), '额外密钥有分享按钮');
ok(guestRow.includes('data-act="copy"'), '额外密钥有复制按钮');

ok(!ownerRow.includes('sk-owner-secret-value'), '行里只显示掩码，不把完整密钥铺在表格上');
ok(guestRow.includes('9 次'), '用量显示次数');
ok(guestRow.includes('1.25 积分'), '用量显示消耗积分');
ok(guestRow.includes('2 次失败'), '有失败时标出来');
ok(idleRow.includes('没人用过'), '零用量必须明说「没人用过」——留白会被读成加载失败，而这一格决定能不能删');

T.setKeysData({
  keys: [owner, guest, idle], stat_days: 7, max_name: 24,
});
T.renderKeys();
const kbody = getEl('keysBody').innerHTML;
eq((kbody.match(/<tr/g) || []).length, 3, '渲染 3 行密钥');
ok(getEl('keysNote').textContent.includes('额外密钥 2 把'), '页头说明额外密钥条数');
ok(getEl('keysNote').textContent.includes('7 天'), '页头说明用量窗口');

/* ── 图表悬停提示 ─────────────────────────────────────────────────── */
// 映射算错的表现是「提示框永远指错一个小时」——图看着完全正常，只有对着数据
// 逐格核对才发现。所以这里做**往返**：每个桶的中心占比都要映射回它自己。
/* ── 统一提示浮层 ─────────────────────────────────────────────────── */
// 全站只留一种提示样式。原生 title 改不了样式，只能拦截：悬停时把 title 暂时摘下来
// 交给自定义浮层，移开再装回去（不摘会紧接着弹第二个原生框，闪一下很难看）。
console.log('tip');
{
  getEl('tipTarget').hidden = false;
  const target = getEl('tipTarget');
  target.setAttribute('title', '这是原生提示');
  target.setAttribute('data-tip', '这是自定义提示');

  T.tipOver({ target: target, clientX: 400, clientY: 300 });
  ok(getEl('body').innerHTML !== undefined, '浮层建在 body 上');

  // data-tip 优先于 title
  eq(T.tipState().text, '这是自定义提示', 'data-tip 优先于 title');
  eq(target.getAttribute('title'), null, '悬停时摘掉原生 title（否则会弹第二个框）');

  // 移开后 title 必须装回去——不装就永久丢掉原生语义（无障碍工具读不到）
  T.tipOut({ target: target, relatedTarget: null });
  eq(target.getAttribute('title'), '这是原生提示', '移开后把 title 装回去');

  // 只有 title、没有 data-tip 的元素也要被接管（页面上几百处都是这种）
  const onlyTitle = getEl('tipOnlyTitle');
  onlyTitle.setAttribute('title', '只有原生 title');
  T.tipOver({ target: onlyTitle, clientX: 100, clientY: 100 });
  eq(T.tipState().text, '只有原生 title', '只有 title 的元素也被接管');
  eq(onlyTitle.getAttribute('title'), null, '并且同样摘掉原生 title');
  T.tipOut({ target: onlyTitle, relatedTarget: null });
  eq(onlyTitle.getAttribute('title'), '只有原生 title', 'title 装回去');

  // 移到子元素上不算离开
  T.tipOver({ target: target, clientX: 400, clientY: 300 });
  T.tipOut({ target: target, relatedTarget: target });
  eq(T.tipState().text, '这是自定义提示', '移到子元素上不收起浮层');
  T.tipOut({ target: target, relatedTarget: null });
  eq(T.tipState().on, false, '真正离开时收起');

  // ── 有文字的按钮不弹提示 ─────────────────────────────────────────
  // 按钮上已经写着"刷新""复制"，再浮一个框是噪音。但**图标按钮**要保留，
  // 否则没人知道那个只有 SVG 的按钮是干什么的。
  const textBtn = mkBtn('刷新一下', '刷新');
  T.tipOver({ target: textBtn, clientX: 200, clientY: 200 });
  eq(T.tipState().on, false, '有文字的按钮不弹提示');
  eq(T.tipAttr(textBtn), null, '并且把原生 title 也摘掉（否则浏览器自己会弹）');

  const iconBtn = mkBtn('', '切换主题');
  T.tipOver({ target: iconBtn, clientX: 200, clientY: 200 });
  eq(T.tipState().text, '切换主题', '图标按钮（没有文字）保留提示');

  // 只有空白文字的按钮也算图标按钮
  const blankBtn = mkBtn('   ', '空白文字');
  T.tipOver({ target: blankBtn, clientX: 200, clientY: 200 });
  eq(T.tipState().text, '空白文字', '只有空白文字也算图标按钮');

  // 非按钮元素不受这条规则影响（表头、卡片都能弹）
  const th = getEl('tipth');
  th.tagName = 'TH';
  th.setAttribute('title', '表头说明');
  th.textContent = '出口';
  T.tipOver({ target: th, clientX: 200, clientY: 200 });
  eq(T.tipState().text, '表头说明', '带文字的非按钮元素照常弹提示');
}

/* ── 原生控件换自定义外观：写回契约 ───────────────────────────────── */
// 外观层唯一真正要紧的事：把用户的选择**交回原生元素**。
// 业务代码读的是 `f.elements[name].checked` 和 `$('accSort').value`——
// 外观只改自己不改原生，表现就是「看着能点、保存无效」，最难发现也最挨骂。
//
// 只测这三个纯逻辑函数，不去造 DOM：造 DOM 那部分在 Node 里没法验，
// 而"有没有写回原生"完全可以脱离 DOM 测——把可测的部分抽出来就是这个目的。
console.log('controls');
{
  const sel = {
    options: [{ textContent: '按健康度', value: 'health' },
              { textContent: '按积分', value: 'credits' },
              { textContent: '按名称', value: 'name' }],
    selectedIndex: 0,
    _handlers: [],
    dispatchEvent(ev) { this._handlers.forEach(h => h(ev)); },
    addEventListener(_t, h) { this._handlers.push(h); },
  };

  eq(T.dropdownLabel(sel), '按健康度', '下拉标签取原生当前选中项的文字');

  let fired = 0;
  sel.addEventListener('change', () => { fired++; });

  eq(T.pickDropdownOption(sel, 1), true, '选了不同项 → 返回 true');
  eq(sel.selectedIndex, 1, '写回**原生** selectedIndex');
  eq(sel.options[sel.selectedIndex].value, 'credits', '原生 value 跟着变');
  eq(fired, 1, '派发 change —— 业务都靠它（如 useDate 到 loadUsage）');
  eq(T.dropdownLabel(sel), '按积分', '标签同步');

  eq(T.pickDropdownOption(sel, 1), false, '选同一项 → 返回 false（不重复触发）');
  eq(fired, 1, '选同一项不派发 change');

  eq(T.pickDropdownOption(sel, 9), false, '越界索引安全返回 false');
  eq(T.pickDropdownOption(null, 0), false, '空 select 安全返回 false');
  eq(sel.selectedIndex, 1, '非法调用不改变原值');

  const cb = { checked: false, _h: [], dispatchEvent(ev) { this._h.forEach(f => f(ev)); }, addEventListener(_t, f) { this._h.push(f); } };
  let cbFired = 0;
  cb.addEventListener('change', () => { cbFired++; });
  T.toggleCheckbox(cb);
  eq(cb.checked, true, '点开关写回**原生** checked');
  eq(cbFired, 1, '并派发 change（配置保存读的就是这个）');
  T.toggleCheckbox(cb);
  eq(cb.checked, false, '再点一次翻回去');
  eq(cbFired, 2, '每次都派发');
  T.toggleCheckbox(null);
  ok(true, '空 checkbox 安全');
}

/* ── 汇总 ─────────────────────────────────────────────────────────── */
(async () => {
  // 上面只证明了「请求发出去了」。这里等真实 promise 落地，证明 loadStatus
  // 确实把 overview 刷进了侧边栏（applyStatus 被调用），端到端闭环。
  getEl('navState').textContent = '连接中';
  await T.loadStatus(true);
  eq(getEl('navState').textContent, '无可用账号', 'loadStatus 端到端刷到侧边栏');
  eq(getEl('navVer').textContent, 'v9.9.9', 'loadStatus 端到端刷版本号');

  /* 自建确认弹窗：文案、危险色、返回值、关闭 */
  console.log('askDialog');
  const p = T.ask('正文一\n正文二', { title: '标题', ok: '确定', danger: true });
  eq(getEl('cfmTitle').textContent, '标题', 'ask 写入标题');
  eq(getEl('cfmText').textContent, '正文一\n正文二', 'ask 写入正文（保留换行）');
  ok(getEl('cfmVeil').classList.contains('on'), 'ask 打开遮罩');
  eq(getEl('btnCfmOk').textContent, '确定', 'ask 写入确认按钮文案');
  ok(getEl('btnCfmOk').classList.contains('danger'), 'danger 选项给确认按钮上危险色');
  T.closeAsk(true);
  eq(await p, true, '点确认 → ask 返回 true');
  ok(!getEl('cfmVeil').classList.contains('on'), '确认后遮罩关闭');

  const p2 = T.ask('取消测试', {});
  eq(getEl('cfmTitle').textContent, '确认操作', '未给标题时用默认标题');
  ok(!getEl('btnCfmOk').classList.contains('danger'), '非危险操作不上危险色');
  T.closeAsk(false);
  eq(await p2, false, '点取消 → ask 返回 false');

  /* 4a-5. 添加账号：双域选择（国内版 / 国际版）
     为什么放这儿：要等 fetch 桩装好（statusPoll 一节）才能观察 POST body。
     0b2103b 引入「选域 → 获取授权链接」两步流程，ab7fd7c 换基座时前端整段
     被远端版本覆盖，后端 realm 分支还在、UI 没了——点「添加账号」只能拿 CN 链接。 */
  console.log('addRealm');
  // startAddLogin 内部是 api().then(...)：fetch 桩同步记到调用，
  // 但写 DOM 的回调要等几轮微任务。setTimeout 在本 harness 里是空壳（不真调度），
  // 所以用微任务 flush，而不是 sleep。
  const flush = async (n = 12) => { for (let i = 0; i < n; i++) await Promise.resolve(); };
  fetched.length = 0;
  // 先模拟「上一次会话选了国际版」，再打开弹窗：勾选必须归位到 CN。
  // radio 的 checked 是持久 DOM 状态，不重置的话「上次选国际版 → 这次想加
  // 国内号」会沿用上次勾选、加错域。
  sandbox.__setAddRealm('global');
  T.openAdd();
  eq(sandbox.__getAddRealm(), 'cn', '重开弹窗选域归位到国内版（防沿用上次的国际版）');
  ok(!getEl('addPick').hidden, '打开弹窗先显示选域行');
  ok(getEl('addLoad').hidden, '选域态不显示「正在获取授权链接」');
  ok(!getEl('btnStartLogin').hidden && !getEl('btnStartLogin').disabled, '「获取授权链接」按钮亮着');
  eq(fetched.length, 0, '打开弹窗不自动发 login/start（否则用户来不及选域）');

  // 选国际版 → POST body 必须带 realm=global
  sandbox.__setAddRealm('global');
  fetchCalls.length = 0;
  T.startAddLogin();
  const startCall = fetchCalls.find(c => c.url.endsWith('/login/start'));
  ok(!!startCall, 'startAddLogin 发出 login/start');
  eq(JSON.parse(startCall.opts.body).realm, 'global', '选国际版 → body.realm=global');

  // 选国内版 → body.realm=cn（零回归）
  sandbox.__setAddRealm('cn');
  fetchCalls.length = 0;
  T.openAdd();
  T.startAddLogin();
  const cnCall = fetchCalls.find(c => c.url.endsWith('/login/start'));
  eq(JSON.parse(cnCall.opts.body).realm, 'cn', '选国内版 → body.realm=cn');

  // 等 startAddLogin 的回调落地，再断言选域锁定 + 按钮收放
  await flush();
  eq(getEl('addPick').hidden, true, '拿到链接后选域锁定（会话已按该域发起）');
  ok(getEl('addUrl').textContent.includes('auth'), '授权链接写进 addUrl');
  ok(getEl('btnStartLogin').hidden, '「获取授权链接」按钮收起');

  // poll 完成：global 账号文案带「（国际版）」
  await T.pollLogin();
  ok(getEl('addDone').textContent.includes('（国际版）'), 'global 账号完成文案标「国际版」');
  ok(getEl('addDone').textContent.includes('新号'), '完成文案带昵称');

  /* ── 面板切换优化：缓存优先 / 域开关原地更新 / 轮询收敛 ───────────────
     这一段盯的是「切页为什么卡」的三条根因，都是行为契约而非像素：
     1) 数据没变就不重绘（否则 5 秒轮询每 tick 重建上千个节点）；
     2) 不可见的视图不重绘，进页面时再画（否则切页做白工）；
     3) 域开关不重建 DOM（重建会吃掉 CSS 过渡与按压反馈）。 */
  console.log('panelPerf');

  // 1) 签名守卫：同一个签名只画一次
  let drew = 0;
  T.renderIfChanged('utest', 'sig-a', () => drew++);
  T.renderIfChanged('utest', 'sig-a', () => drew++);
  eq(drew, 1, '签名相同 → 不重绘');
  T.renderIfChanged('utest', 'sig-b', () => drew++);
  eq(drew, 2, '签名变化 → 重绘');
  T.renderIfChanged('utest', '', () => drew++);
  eq(drew, 3, 'sig 传空串 → 强制重绘');

  // 2) 脏标记 + paintView：不可见时只记脏，进页面才补画，画过不重复画
  T.setOverview({ session_dead_threshold: 3, accounts: [{ uid: 'u1', credits: 5 }] });
  T.setView('accounts');
  T.markDirty('accounts', 'ov-1');
  eq(T.paintView('accounts'), true, '不可见期间攒下的数据，进页面时补画');
  eq(T.paintView('accounts'), false, '画过之后不再重复画（DOM 已是最新）');
  T.setView('logs');
  T.markDirty('accounts', 'ov-2');
  eq(T.viewDirtyState('accounts'), true, '不可见时数据变了 → 只记脏，不画');

  // 3) 域开关：不重建 DOM、计数原地更新、胶囊跟着走
  T.renderRealmSwitch();
  const cnBtn = T.realmBtn('cn'), cnEl = cnBtn.el, cnN = cnBtn.n;
  T.renderRealmSwitch();
  ok(T.realmBtn('cn').el === cnEl, '域开关不重建 DOM（节点同一性保持）');
  ok(T.realmBtn('cn').n === cnN, '计数字节点也保持同一个（原地改文本）');
  T.setOverview({ accounts: [{ uid: 'a', realm: 'cn' }, { uid: 'b', realm: 'global' }] });
  T.renderRealmSwitch();
  eq(cnBtn.n.textContent, '1', '计数原地更新（国服 1 个）');
  eq(T.realmBtn('all').n.textContent, '2', '全部计数 = 2');

  cnEl.offsetLeft = 70; cnEl.offsetWidth = 52;
  T.realmBtn('global').el.offsetLeft = 130; T.realmBtn('global').el.offsetWidth = 64;
  T.setPanelRealm('cn');
  eq(T.realmInd().style.transform, 'translateX(70px)', '胶囊平移到选中项');
  eq(T.realmInd().style.width, '52px', '胶囊宽度跟随选中项');
  ok(cnEl.classList.contains('on'), '选中项带 on');
  ok(!T.realmBtn('global').el.classList.contains('on'), '非选中项不带 on');
  T.setPanelRealm('global');
  eq(T.realmInd().style.transform, 'translateX(130px)', '切到国际服胶囊跟着滑');

  // 4) 切到国际服：停掉 CN 队列轮询（那些端点国际服没有）
  T.setQueueTimer(42);
  T.setPanelRealm('global');
  eq(T.queueTimerValue(), null, '国际服模式下队列定时器被清掉（不再空转打 CN 端点）');
  ok(sandbox.__timerLog.clear.includes(42), '确实调用了 clearInterval');

  // 5) 后台标签页不轮询
  T.setPanelRealm('all');
  sandbox.document.hidden = true;
  fetched.length = 0;
  T.refreshVisible();
  eq(fetched.length, 0, '标签页在后台 → 一轮轮询一个请求都不发');
  sandbox.document.hidden = false;
  fetched.length = 0;
  T.refreshVisible();
  ok(fetched.length > 0, '回到前台 → 照常轮询');

  // 6) 日志页脚写真实的轮询间隔（以前写死 1.5s，实际 5s）
  T.setLogLines(['2026-09-21 10:00:00 [task] hello']);
  T.paintLogs();
  ok(getEl('termMeta').textContent.includes('5s'), '日志页脚写真实间隔（5s）');
  ok(!getEl('termMeta').textContent.includes('1.5s'), '不再是写死的 1.5s');
  eq(T.pollMs, 5000, 'POLL_MS = 5000（轮询与页脚同源）');

  // 7) 签名必须把「这一页自己的筛选」算进去。
  //    反例是真实踩到的：go() 进账号页时会按域重设 accFilter，而数据一个字节没变——
  //    签名若只看数据，那次重设就会被守卫跳过，表格停在旧筛选上（看着像切了没反应）。
  T.setOverview({ accounts: [{ uid: 'cn1', realm: 'cn', credits: 1 },
    { uid: 'gl1', realm: 'global', credits: 2 }] });
  T.setView('accounts');
  T.setAccFilter('all');
  const sigAll = T.accViewSig();
  T.setAccFilter('global');
  ok(sigAll !== T.accViewSig(), '账号页签名随筛选变化（否则筛选变了会被跳过）');
  T.renderAccounts();
  ok(!getEl('accBody').innerHTML.includes('cn1'), '筛选=国际服后，国服号从表里消失');
  T.setAccFilter('all');
  T.setLogFilter('all'); const logSigA = T.logViewSig();
  T.setLogFilter('task');
  ok(logSigA !== T.logViewSig(), '日志页签名随频道筛选变化');

  console.log(`\n${passes} 通过 / ${fails} 失败`);
  if (fails) process.exit(1);
  console.log('APP.JS BEHAVIOR OK');
})();
