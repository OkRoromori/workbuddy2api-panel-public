'use strict';
/*
 * 仪表盘静态预览生成器。
 *
 * 为什么需要：本机 agent-browser 不支持 Windows，没有任何浏览器渲染验证。
 * harness 能证明「DOM 文本对」，但证明不了「人眼看上去是不是那回事」——
 * 而像素/布局只有真浏览器说了算。这个脚本把 index.html 的**真实 CSS**、
 * app.js 的**真实渲染函数**、一份**仿真快照**三者拼成一张离线页面
 * `dash-preview.html`，用户双击就能看到仪表盘装满数据时的样子，
 * 不必等真实流量跑起来。
 *
 * 它同时是一个自检器：渲染结果结构不对（没渲染出表行、列数不对）就直接
 * 退出码非 0，绝不写一份"看着有内容其实是空壳"的预览页。
 *
 * 用法：node internal/panel/testdata/dash_preview.js
 */

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const panelDir = path.join(__dirname, '..');
const indexPath = path.join(panelDir, 'index.html');
const appPath = path.join(panelDir, 'app.js');
const outPath = path.join(__dirname, '..', '..', '..', 'dash-preview.html');

/* ── 最小 DOM 桩（与 app_harness.js 同构，够跑渲染） ──────────────── */
const els = Object.create(null);
function mkEl(id) {
  return {
    id,
    innerHTML: '', textContent: '', title: '', value: '', checked: false,
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
    closest() { return null; }, querySelectorAll() { return []; },
  };
}
function getEl(id) {
  if (!els[id]) els[id] = mkEl(id);
  return els[id];
}

const sandbox = {
  console,
  setTimeout: () => 0, clearTimeout: () => {}, setInterval: () => 0, clearInterval: () => {},
  Date, Math, JSON, Object, Array, String, Number, Boolean, RegExp, Error, Promise, Set, Map,
  encodeURIComponent, decodeURIComponent, parseInt, parseFloat, isNaN,
  document: {
    getElementById: getEl,
    createElement: () => mkEl('new'),
    querySelectorAll: () => [],
    documentElement: { dataset: {} },
    body: mkEl('body'),
    addEventListener() {},
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
  Notification: function () { this.close = () => {}; },
  fetch: () => Promise.reject(new Error('preview 不发请求')),
  confirm: () => true,
  alert: () => {},
};
sandbox.Notification.permission = 'granted';
sandbox.Notification.requestPermission = () => Promise.resolve('granted');
sandbox.window = sandbox;
sandbox.globalThis = sandbox;
sandbox.self = sandbox;

const src = fs.readFileSync(appPath, 'utf8');
const trailer = `
;globalThis.__t = { renderDashboard, applyStatus, openExpiryDetail, _closeExpiryDetail,
  setKeyNames: v => { keyNames = v; } };
`;
vm.createContext(sandbox);
vm.runInContext(src + trailer, sandbox, { filename: 'app.js' });
const T = sandbox.__t;

/* ── 仿真数据 ─────────────────────────────────────────────────────── */
// 刻意做成"多模型 + 多账号 + 有缓存命中 + 有错误 + 有时序起伏"，
// 因为空格子看不出布局问题，满格子才看得出。别名故意用中文，
// 顺带验证长别名下的列宽。
const UIDS = {
  a: '7f3c1d92-4b8e-4a51-9c0d-1e2f3a4b5c6d',
  b: 'b41a9e07-2d55-4f80-a1c3-9d8e7f6a5b4c',
  c: '0c9e8d7f-6a5b-4c3d-8e2f-1a0b9c8d7e6f',
};

T.applyStatus({
  total: 3, healthy: 2, cooling: 1, disabled: 0, sticky_sessions: 6,
  version: '1.4.2', redis_mode: 'upstash', uptime_sec: 3 * 86400 + 5 * 3600 + 12 * 60,
  in_flight_full: 0,
  accounts: [
    { uid: UIDS.a, alias: '主号', credits: 12840 },
    { uid: UIDS.b, alias: '备用号', credits: 3270 },
    { uid: UIDS.c, alias: '小号·测试用', credits: 610 },
  ],
});

// 密钥名映射（真实运行时由 /panel/api/stats 的 key_names 带来）。
// 故意留一把**没有名字**的密钥：验证"密钥已删/新增但映射还没跟上"时
// 回落显示短指纹，而不是渲染成空白行。
T.setKeyNames({
  '9f2c4a1b7e08d3f5a6c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d': '我的主力密钥',
  '3a5b7c9d1e2f30415263748596a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f': '室友共用',
  'b7d9f1a3c5e708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091': 'CI 机器人',
});

const nowHour = Math.floor(Date.now() / 3600000);
const buckets = [];
// 24 小时窗口：白天高峰 + 深夜低谷 + 一两格完全空闲（空闲柱也要画出来，验证下限高度）。
const hourPattern = [0, 0, 0, 0, 0, 0, 1200, 9800, 42000, 68000, 74000, 61000,
  38000, 52000, 88000, 96000, 71000, 44000, 26000, 18000, 12400, 6800, 2100, 0];
for (let i = 0; i < 24; i++) {
  const tok = hourPattern[i];
  buckets.push({ hour: nowHour - (23 - i), requests: tok ? Math.max(1, Math.round(tok / 2400)) : 0, tokens: tok });
}

function entry(key, o) {
  return Object.assign({
    key, requests: 0, ok: 0, errors: 0, prompt_tokens: 0, completion_tokens: 0,
    total_tokens: 0, cached_tokens: 0, missing_usage: 0,
    avg_ttfb_ms: 0, avg_duration_ms: 0, tokens_per_sec: 0,
  }, o);
}

const snap = {
  started: new Date(Date.now() - 3 * 86400e3).toISOString(),
  uptime_sec: 3 * 86400 + 5 * 3600 + 12 * 60,
  totals: {
    requests: 1284, ok: 1236, errors: 48,
    prompt_tokens: 3481200, completion_tokens: 1864400, total_tokens: 5345600,
    cached_tokens: 902300, missing_usage: 11,
    avg_ttfb_ms: 486.3, avg_duration_ms: 7420, tokens_per_sec: 42.7,
  },
  models: [
    entry('glm-5.2', {
      requests: 742, ok: 719, errors: 23, prompt_tokens: 2120400,
      completion_tokens: 1158600, total_tokens: 3279000, cached_tokens: 611400,
      missing_usage: 4, avg_ttfb_ms: 452.1, avg_duration_ms: 7100, tokens_per_sec: 45.9,
    }),
    entry('deepseek-v4', {
      requests: 356, ok: 344, errors: 12, prompt_tokens: 905600,
      completion_tokens: 482300, total_tokens: 1387900, cached_tokens: 201800,
      missing_usage: 3, avg_ttfb_ms: 531.7, avg_duration_ms: 8200, tokens_per_sec: 36.2,
    }),
    entry('qwen3-coder', {
      requests: 154, ok: 146, errors: 8, prompt_tokens: 388400,
      completion_tokens: 190100, total_tokens: 578500, cached_tokens: 74100,
      missing_usage: 4, avg_ttfb_ms: 498.2, avg_duration_ms: 6900, tokens_per_sec: 28.4,
    }),
    entry('kimi-k2', {
      requests: 32, ok: 27, errors: 5, prompt_tokens: 66800,
      completion_tokens: 33400, total_tokens: 100200, cached_tokens: 15000,
      missing_usage: 0, avg_ttfb_ms: 640.0, avg_duration_ms: 9100, tokens_per_sec: 19.7,
    }),
  ],
  // 按密钥：面板现在展示这一维（账号表已下线）。
  // 用真实形态的 sha256 短指纹，名字含中文与长名，验证列宽与回落。
  keys: [
    entry('9f2c4a1b7e08d3f5a6c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d', {
      requests: 742, ok: 719, errors: 23, prompt_tokens: 2120400,
      completion_tokens: 1158600, total_tokens: 3279000, cached_tokens: 611400,
      missing_usage: 4, avg_ttfb_ms: 452.1, avg_duration_ms: 7100, tokens_per_sec: 45.9,
    }),
    entry('3a5b7c9d1e2f30415263748596a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f', {
      requests: 356, ok: 344, errors: 12, prompt_tokens: 905600,
      completion_tokens: 482300, total_tokens: 1387900, cached_tokens: 201800,
      missing_usage: 3, avg_ttfb_ms: 531.7, avg_duration_ms: 8200, tokens_per_sec: 36.2,
    }),
    entry('b7d9f1a3c5e708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091', {
      requests: 154, ok: 146, errors: 8, prompt_tokens: 388400,
      completion_tokens: 190100, total_tokens: 578500, cached_tokens: 74100,
      missing_usage: 4, avg_ttfb_ms: 498.2, avg_duration_ms: 6900, tokens_per_sec: 28.4,
    }),
    entry('e1f2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708', {
      requests: 32, ok: 27, errors: 5, prompt_tokens: 66800,
      completion_tokens: 33400, total_tokens: 100200, cached_tokens: 15000,
      missing_usage: 0, avg_ttfb_ms: 640.0, avg_duration_ms: 9100, tokens_per_sec: 19.7,
    }),
  ],
  // accounts 仍留在快照里（metrics 仍会算这一维，积分比率靠它归因），
  // 但面板不再渲染——正好顺便验证"多给的字段不会让前端出错"。
  accounts: [
    entry(UIDS.a, {
      requests: 812, ok: 790, errors: 22, prompt_tokens: 2280400,
      completion_tokens: 1224300, total_tokens: 3504700, cached_tokens: 650200,
      missing_usage: 5, avg_ttfb_ms: 468.9, avg_duration_ms: 7250, tokens_per_sec: 44.1,
    }),
  ],
  buckets,
  credits: {
    remain: 45164, used: 4380, samples: 37,
    per_mtoken: 254, est_tokens: 65826771, ready: true, age_sec: 96,
    burn_per_hour: 18.4, runway_hours: 908.7, burn_span_sec: 79200, burn_net: 405,
    // 会过期的积分：比例要跟 remain 对得上（否则卡片会显示"占余额 240%"这种假数）。
    // 真实场景：remain 45,164，到期合计 40,060 = 88.7%。
    expiring_days: 26,
    expiring_batches: [
      { at: '2026-10-11', days: 26, remain: 1600 },
      { at: '2026-10-12', days: 27, remain: 100 },
      { at: '2026-10-13', days: 28, remain: 35500 },
      { at: '2026-10-14', days: 29, remain: 2860 },
    ],
  },
};

T.renderDashboard(snap);

/* ── 自检：结构不对就别写文件 ─────────────────────────────────────── */
let selfFails = 0;
function need(cond, msg) {
  if (cond) return;
  selfFails++;
  console.error('  ✗ 预览自检失败：' + msg);
}
const mBody = getEl('dashModelBody').innerHTML;
const kBody = getEl('dashKeyBody').innerHTML;
need((mBody.match(/<tr\b/g) || []).length === 4, '模型表应渲染 3 行数据 + 1 行折叠行');
const dataRows = mBody.replace(/<tr class="more-row">[\s\S]*?<\/tr>/, '');
need((dataRows.match(/<td\b/g) || []).length / 3 === 13, '模型表每行应 13 格（含积分/Mtok 列）');
// 断言按实际行数推：写死除数的话，改一条样本数据就会误报"列数不对"。
const keyRows = (kBody.match(/<tr>/g) || []).length;
need(keyRows === 4, '密钥表应渲染 4 行');
need((kBody.match(/<td\b/g) || []).length / keyRows === 8, '密钥表每行应 8 格');
need(getEl('dashCards').innerHTML.includes('1,284'), 'KPI 应含请求总数');
// 主数字用中文单位（6583万），副行给完整数值——只给缩写等于没给。
need(getEl('dashCredit').innerHTML.includes('6583万'), '积分卡应含预估 token（中文单位）');
need(getEl('dashCredit').innerHTML.includes('65,826,771 token'), '积分卡应含预估 token 完整数值');
need(getEl('dashCreditViz').innerHTML.includes('class="burn"'), '积分概览卡应渲染额度条');
need(getEl('dashCreditViz').innerHTML.includes('账号数建议'), '积分概览卡应给出账号数建议');
need(!getEl('dashExpiry').hidden, '临期积分应在顶栏显示小卡片');
// 卡片只显示最快到期那一条（不能铺开多行把顶栏撑高）
need(getEl('dashExpiry').innerHTML.includes('等 4 笔'), '卡片应提示还有几笔可展开');
need(!getEl('dashExpiry').innerHTML.includes('2026-10-14'), '卡片上不铺开全部到期日');
// 弹窗要能拿到完整清单
T.openExpiryDetail();
need((getEl('expBody').innerHTML.match(/<tr>/g) || []).length === 4, '明细弹窗应列出 4 个到期日');
T._closeExpiryDetail();
if (selfFails) {
  console.error(`\n预览自检失败 ${selfFails} 项，未写出文件。`);
  process.exit(1);
}

/* ── 拼装静态页 ───────────────────────────────────────────────────── */
let html = fs.readFileSync(indexPath, 'utf8');

function setInner(h, id, inner) {
  const re = new RegExp('(\\bid="' + id + '"[^>]*>)([\\s\\S]*?)(</(div|tbody|span)>)');
  if (!re.test(h)) throw new Error('找不到容器 #' + id + '，index.html 结构变了');
  return h.replace(re, (m, open, _old, close) => open + inner + close);
}
function setText(h, id, text) {
  // 闭合标签不一定是 </span>：dashVizTotal 是 <b>。只认 </span> 的话，
  // 非贪婪匹配会一路吞到"后面第一个 </span>"，把中间的结构一起吃掉。
  const re = new RegExp('(\\bid="' + id + '"[^>]*>)([\\s\\S]*?)(</span>|</b>)');
  if (!re.test(h)) throw new Error('找不到容器 #' + id);
  return h.replace(re, (m, open, _old, close) => open + text + close);
}

html = setInner(html, 'dashCards', getEl('dashCards').innerHTML);
html = setInner(html, 'dashCredit', getEl('dashCredit').innerHTML);
html = setInner(html, 'dashQuality', getEl('dashQuality').innerHTML);
html = setInner(html, 'dashCreditViz', getEl('dashCreditViz').innerHTML);
// 到期卡片是 <button>：不能用 setInner —— 它的闭合标签白名单只有 div/tbody/span，
// 遇到 </button> 会一路吞到后面第一个 </div>，把结构搞坏。这里直接整段替换。
{
  const el = getEl('dashExpiry');
  const open = '<button class="' + el.className + '" id="dashExpiry"' + (el.hidden ? ' hidden' : '') + '>';
  html = html.replace(/<button\b[^>]*\bid="dashExpiry"[^>]*>[\s\S]*?<\/button>/,
    () => open + el.innerHTML + '</button>');
}
// 到期明细弹窗：预览页没有 app.js，点不动按钮，所以直接把弹窗画成打开状态给人看。
// 这里**整段替换**正文而不是用 setInner —— setInner 靠闭合标签推断边界，
// 而弹窗里是 <p> 和 <tbody> 混排，它认不出 </p>，会报"找不到容器"。
{
  const sum = getEl('expSummary').textContent;
  const body = getEl('expBody').innerHTML;
  html = html.replace(
    /(<p class="cfm-text" id="expSummary">)[\s\S]*?(<\/p>\s*<div class="tbl-wrap"[^>]*>\s*<table[^>]*>\s*<thead>[\s\S]*?<\/thead>\s*<tbody id="expBody">)[\s\S]*?(<\/tbody>)/,
    (m, a, b, c) => a + sum + b + body + c
  );
  html = html.replace('<div class="veil" id="expVeil">', '<div class="veil on" id="expVeil">');
}
html = setText(html, 'dashCreditAge', getEl('dashCreditAge').textContent);
html = setInner(html, 'dashModelBody', mBody);
html = setInner(html, 'dashKeyBody', kBody);
html = setText(html, 'dashNote', getEl('dashNote').textContent);
html = setText(html, 'dashKeyNote', getEl('dashKeyNote').textContent);

// 侧边栏 / 顶栏
html = setText(html, 'navState', getEl('navState').textContent);
html = setText(html, 'navVer', getEl('navVer').textContent);
html = setText(html, 'navRedis', getEl('navRedis').textContent);
html = setInner(html, 'navSub', getEl('navSub').textContent); // navSub 是 div
html = setText(html, 'subMeta', getEl('subMeta').textContent);
// navPulse：applyStatus 会改写整个 class（pulse / pulse warn / pulse bad），
// 原标记写作 class="pulse"，这里整段换掉，避免出现两个 class 属性。
const pulseMarkup = '<span class="pulse" id="navPulse"></span>';
if (!html.includes(pulseMarkup)) throw new Error('找不到 #navPulse 原始标记');
html = html.replace(pulseMarkup,
  '<span class="' + getEl('navPulse').className + '" id="navPulse"></span>');

// 只显示仪表盘：默认落地页已经是 dashboard 可见、accounts 带 hidden。
if (!html.includes('id="view-dashboard"') || html.includes('id="view-dashboard" hidden')) {
  throw new Error('仪表盘默认落地页结构变了：view-dashboard 应可见');
}
if (!html.includes('<section class="view" id="view-accounts" hidden>')) {
  throw new Error('账号池应默认 hidden，避免首屏闪一下账号表');
}

// 去掉真实脚本：预览页不需要轮询，也不能让它去请求 API
html = html.replace('<script src="app.js"></script>', '');

// 顶部横幅：这是静态仿真，不是真面板。别让人误以为连上了服务。
const banner = `
<div style="position:fixed;left:0;right:0;bottom:0;z-index:99;background:#1f2937;color:#e5e7eb;
  font:12px/1.5 ui-monospace,Consolas,monospace;padding:8px 14px;text-align:center;border-top:1px solid #374151">
  静态预览 · 数据为仿真样本，非真实流量 · 页面样式与渲染代码取自 panel/index.html + app.js
  <span style="color:#9ca3af">（生成于 ${new Date().toISOString().slice(0, 19).replace('T', ' ')}）</span>
</div>`;
html = html.replace('</body>', banner + '\n</body>');

// 归一到 CRLF：index.html 是 CRLF，横幅模板字面量是 LF，不统一会出现混合行尾
// （本仓库对行尾一致性有要求，见 LOCAL-FIXES.md 末节）。幂等写法。
html = html.replace(/\r\n/g, '\n').replace(/\n/g, '\r\n');

fs.writeFileSync(outPath, html, 'utf8');
console.log('已生成 ' + outPath);
console.log('  ' + Math.round(html.length / 1024) + ' KB · 4 模型 / 4 密钥 / 24 根柱 · 自检通过');
