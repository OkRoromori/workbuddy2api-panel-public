'use strict';
/* ── 状态 ─────────────────────────────────────────────────────────── */
const LS_KEY = 'wb2api.key', LS_THEME = 'wb2api.theme', LS_REALM = 'wb2api.realm';
let theme = localStorage.getItem(LS_THEME) || 'light';   // light | dark | auto
// panelRealm 面板视图域开关：'all' | 'cn' | 'global'（默认 all = 两域一起看，与历史行为一致）。
//
// 只决定「你正在看哪个域的数据」，**不参与请求路由**——某个模型走哪个域由网关侧的
// model_realm（默认域/逐个钉）与模型目录共同决定，与这个开关无关。
let panelRealm = localStorage.getItem(LS_REALM) || 'all';
let view = 'accounts';
let overviewData = null, cfgLoaded = null;
let logPin = true, loginState = null, loginTimer = null;
let refTimer = null;
// lastDataTime 最近一次带 X-Data-Time 头的响应里的数据时间（ISO 字符串）。
// 由 api() 统一捕获；dataTimeSuffix() 把它变成页脚后缀。
let lastDataTime = '';
// POLL_MS 面板轮询间隔（毫秒）。日志页脚要把它写出来，所以只此一处定义——
// 以前页脚硬编码「自动刷新 1.5s」，而真实间隔是 5s，写的和做的不一致。
const POLL_MS = 5000;
// APP_NAME 面板显示名。**只在这里定义一处**：侧边栏大字标题与浏览器标签都由它写。
// 为什么不用「WorkBuddy」当标题：那是腾讯的产品名，占在标题位等于把别人的品牌当成自己的
//（在文档里提一句属于指称性使用，没问题；占品牌位就不一样了）。
// 用版本标识当标题的额外好处：打开面板就知道服务器跑的是哪一版。
const APP_NAME = 'v1.11.1-panel-main';
// APP_SUB 侧边栏小字。写「非官方控制台」而不是再写一遍版本号——上下两行同一个
// 字符串看着像 bug；版本号在大字标题与左下角（v… · 本地内存）都看得到。
const APP_SUB = '非官方控制台';
// applyAppName 把显示名写进侧边栏与浏览器标签（index.html 里留的是同样的文本，
// 无 JS 时也能看到，不至于空白）。
function applyAppName() {
  const el = $('brandName');
  if (el) el.textContent = APP_NAME;
  const sub = $('navSub');
  if (sub) sub.textContent = APP_SUB;
  if (document.title !== undefined) document.title = APP_NAME + ' · 非官方控制台';
}
// 任务队列的轮询定时器与代次。**必须在这里声明**：applyCnOnlyViews() 在模块顶层
// 就会被调用一次（见文件末尾），若等到队列那一节才 let 声明，这里引用会撞 TDZ 直接崩
// ——这个坑本文件已经踩过一次（见 4150 行那段注释）。
// queueTimer / lastQueueSeq 的声明已上移到文件头状态区（applyCnOnlyViews 会在模块
// 顶层引用 queueTimer，声明留在这一节会撞 TDZ）。

const $ = id => document.getElementById(id);
// dataTimeSuffix 页脚的「数据时间」后缀。没有就空串（接口没带缓存或首次同步拉取）。
function dataTimeSuffix() {
  if (!lastDataTime) return '';
  const d = new Date(lastDataTime);
  if (isNaN(d)) return '';
  return ' · 数据 ' + String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
}

/* ── 主题 ─────────────────────────────────────────────────────────── */
/* 两态翻转（浅/深），首次访问跟随系统偏好；点击总是切换可见外观，符合直觉。 */
function effTheme() {
  return theme === 'auto' ? (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark') : theme;
}
function applyTheme() {
  const eff = effTheme();
  document.documentElement.dataset.theme = eff;
  $('icoTheme').innerHTML = eff === 'light'
    ? '<circle cx="8" cy="8" r="3"/><path d="M8 1v2M8 13v2M1 8h2M13 8h2M3.2 3.2l1.4 1.4M11.4 11.4l1.4 1.4M12.8 3.2l-1.4 1.4M4.6 11.4l-1.4 1.4"/>'
    : '<path d="M13.2 9.6A5.6 5.6 0 0 1 6.4 2.8a5.6 5.6 0 1 0 6.8 6.8z"/>';
}
addEventListener('change', applyTheme);
$('btnTheme').onclick = () => {
  theme = effTheme() === 'light' ? 'dark' : 'light';
  localStorage.setItem(LS_THEME, theme);
  applyTheme();
};
applyTheme();

/* ── 域视图开关（全部 / 国服 / 国际服）──────────────────────────────────
   只决定"你在看哪个域的数据"；请求实际走哪个域由后端的 model_realm（默认域/逐个钉）与模型
   目录共同决定，与此开关无关。开关与各视图自己的域 chips **双向同步**，避免出现"开关说国际服、
   页面 chip 说国服"这种自相矛盾的画面。 */
function realmAccountCounts() {
  const n = { all: 0, cn: 0, global: 0 };
  for (const a of ((overviewData && overviewData.accounts) || [])) {
    n.all++;
    n[realmOf(a)]++;
  }
  return n;
}

// REALM_CHIPS 域开关的三项（顺序即显示顺序）。
const REALM_CHIPS = [['all', '全部'], ['cn', '国服'], ['global', '国际服']];
let realmBtns = null, realmInd = null;   // 三个按钮与滑动胶囊（建一次就留着）

// renderRealmSwitch 画出/更新顶栏的域开关。
//
// **只在第一次建 DOM，之后原地更新**（切 class、改计数）。为什么不能每次 innerHTML 重建：
// 重建会把按钮节点整个换掉——CSS 过渡与 :active 按压反馈全部失效，而本函数每 5 秒还会被
// 轮询调一次（applyStatus → 这里），等于每隔 5 秒吃掉一次你的点击反馈。原地更新才能让
// 「胶囊滑过去」这个过渡真的播出来。
function renderRealmSwitch() {
  const el = $('realmSwitch');
  if (!el) return;
  if (!realmBtns) {
    realmInd = document.createElement('span');
    realmInd.className = 'chip-ind';
    el.appendChild(realmInd);
    realmBtns = REALM_CHIPS.map(([k, l]) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'chip';
      b.setAttribute('data-realm', k);   // 用属性而不是 dataset：点击委托靠 closest('[data-realm]')
      b.textContent = l;
      const n = document.createElement('span');
      n.className = 'n';
      b.appendChild(n);
      el.appendChild(b);
      return { k, el: b, n };
    });
  }
  const n = realmAccountCounts();
  const known = n.all > 0; // 账号列表还没到手时先不显示计数，免得出现"全部 0"
  for (const it of realmBtns) {
    it.el.classList.toggle('on', panelRealm === it.k);
    it.n.textContent = known ? String(n[it.k]) : '';
  }
  moveRealmInd();
}

// moveRealmInd 把滑动胶囊挪到当前选中项（过渡由 CSS 承担）。
//
// 只在「选中项或计数」变化时才读布局：offsetLeft/offsetWidth 会强制同步布局，而本函数
// 每 5 秒会被轮询间接触发一次——数据没变时必须一次布局都不读。
let _realmIndKey = '';
function moveRealmInd() {
  if (!realmInd || !realmBtns) return;
  const n = realmAccountCounts();
  const key = panelRealm + '|' + n.all + ',' + n.cn + ',' + n.global;
  if (key === _realmIndKey) return;
  const first = !_realmIndKey;
  _realmIndKey = key;
  const cur = realmBtns.filter(b => b.k === panelRealm)[0] || realmBtns[0];
  if (first) realmInd.style.transition = 'none';   // 首次定位不演：否则一打开页面胶囊从左边滑过来
  realmInd.style.width = (cur.el.offsetWidth || 0) + 'px';
  realmInd.style.transform = 'translateX(' + (cur.el.offsetLeft || 0) + 'px)';
  if (first && typeof requestAnimationFrame === 'function') {
    requestAnimationFrame(() => { realmInd.style.transition = ''; });
  }
}

// applyPanelRealm 把开关落到"当前视图"的筛选上：各视图自己那套域 chips 与开关同口径。
function applyPanelRealm() {
  applyCnOnlyViews();
  const r = panelRealm === 'all' ? 'all' : panelRealm;
  if (view === 'accounts') {
    accFilter = r;
    renderAccChips(accList.length);
    renderAccounts();
  } else if (view === 'models') {
    mdFilter = r;
    renderMdChips(mdCache.length);
    applyMdFilter();
  } else if (view === 'packages') {
    // 积分构成的数据**本身不分域**（服务端 /panel/api/packages 没有 realm 参数），切域只是
    // 换一批账号来看——所以不重查上游（那是逐账号查询，面板里最慢的一条路），
    // 直接用手里的数据按新域重画。
    if (!paintNow('packages')) loadPackages();
  } else if (view === 'usage') {
    // 用量是**服务端按域过滤**的（这样合计与明细口径一致），换域必须重查；查回来之前
    // 不能拿旧域的数字充数——明确清空并提示，而不是让旧行留着（看着像「切了没反应」）。
    if (useDataRealm !== panelRealm) {
      $('useBody').innerHTML = dashEmpty(8, '正在按「' + realmLabel() + '」重新读取…');
      $('useCount').textContent = '';
      $('useNote').textContent = '读取中…';
    }
    loadUsage();
  } else if (view === 'dashboard') {
    // stats 里本来就带 by_realm 分域切片 → 先用手里的原始 payload 切一刀重画（秒开），
    // 再后台对齐一次（顺带把趋势按域重拉，force 绕过 60 秒节流）。
    renderDashFromRaw();
    loadStats();
    loadTrend(true);
  } else if (view === 'keys') {
    // 密钥的「最近 7 天」用量列按域统计（服务端算），同用量页：换域要重查，
    // 查回来之前不拿旧域的数字充数。
    if (keysRealm !== panelRealm) {
      $('keysBody').innerHTML = dashEmpty(5, '正在按「' + realmLabel() + '」重新读取…');
      $('keysNote').textContent = '读取中…';
    }
    loadKeys();
  }
}

// dashRealmNote 仪表盘在域视图下的诚实提示：说清哪些数字是这一域的、哪些仍是全局口径。
// 分域统计从 rv 存在那一刻开始（RealmSince），此前的历史累计没有域维度——如实标注，不摊派。
function dashRealmNote(rv, s) {
  const note = $('dashRealmNote'), t = $('dashRealmNoteText');
  if (!note || !t) return;
  if (panelRealm === 'all') { note.hidden = true; return; }
  const name = panelRealm === 'global' ? '国际服' : '国服';
  if (!rv) {
    t.textContent = '当前看的是「' + name + '」视图，但该域还没有分域统计数据（服务刚升级或该域暂无流量）：' +
      '下面的数字仍是两域合计。';
    note.hidden = false;
    return;
  }
  let since = '';
  if (s.realm_since) {
    const d = new Date(s.realm_since);
    since = '（分域统计自 ' + d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' +
      String(d.getDate()).padStart(2, '0') + ' ' + String(d.getHours()).padStart(2, '0') + ':' +
      String(d.getMinutes()).padStart(2, '0') + ' 起，此前的历史累计不分域、只在「全部」里）';
  }
  // 积分口径（第 3 步）也按域了：余额/消耗/实测比率/净速率/到期批次都取该域的值。只有当该域
  // 还没采样到（服务刚升级、或该域账号这一轮余额刷新失败）时才如实说明，而不是让旧口径的数字
  // 冒充当前域。
  const creditsOK = !!(rv.credits && rv.credits.ready);
  t.textContent = '当前看的是「' + name + '」视图：请求 / token / 积分 / 按模型 / 按密钥 / 到期批次' +
    '都只算这一域' + since + (creditsOK ? '。' : '。该域的积分口径尚未采样到（等下一轮余额刷新）。');
  note.hidden = false;
}

// applyCnOnlyViews 国内服专属页面（任务管理 / 任务中心）在国际服模式下只留说明卡片。
// 纯类名切换（CSS 负责收起其余卡片），不发请求、不动数据。
function applyCnOnlyViews() {
  const cnOnly = panelRealm === 'global';
  // 切到国际服时停掉任务队列的 3 秒轮询：它打的全是 CN 端点（开学季/成长任务），
  // 国际服没有这些体系——继续轮询等于对着一个必然报错的接口空转。
  // 切回国服后进入任务中心会重新拉一次（go() 里 pollQueueOnce），不影响使用。
  if (cnOnly && queueTimer) { clearInterval(queueTimer); queueTimer = null; }
  for (const id of ['view-checkin', 'view-taskcenter']) {
    const el = $(id);
    if (!el) continue;
    el.classList.toggle('realm-global', cnOnly);
  }
  // 仪表盘的提示由 dashRealmNote() 在取到数据后写（要说清"分域统计自 X 起"这类只有数据才知道的
  // 事实）。这里只在「全部」模式下把它收起来，避免切回全部后还挂着一条过时提示。
  const note = $('dashRealmNote');
  if (note && panelRealm === 'all') note.hidden = true;
}

// setPanelRealm 切换域视图。silent=true 表示"由视图自己的 chips 反向同步过来"，
// 此时不必再回头改视图（避免重复渲染）。
function setPanelRealm(v, silent) {
  if (v !== 'all' && v !== 'cn' && v !== 'global') v = 'all';
  panelRealm = v;
  localStorage.setItem(LS_REALM, v);
  renderRealmSwitch();
  if (!silent) applyPanelRealm();
}

$('realmSwitch').addEventListener('click', ev => {
  const b = ev.target.closest('[data-realm]');
  if (b) setPanelRealm(b.dataset.realm);
});
renderRealmSwitch();
applyCnOnlyViews(); // 启动时也应用一次：刷新后若停在「国际服」，CN 专属页就该是说明态

/* ── 请求 ─────────────────────────────────────────────────────────── */
async function api(path, opts = {}) {
  const h = Object.assign({}, opts.headers || {});
  const k = localStorage.getItem(LS_KEY);
  if (k) h['Authorization'] = 'Bearer ' + k;
  if (opts.body) h['Content-Type'] = 'application/json';
  const r = await fetch('/panel/api/' + path, Object.assign({}, opts, { headers: h }));
  if (r.status === 401) { openKey(); throw new Error('密钥无效或未填写'); }
  // 慢接口带缓存（packages/usage/models），服务端用这个头说明「数据是什么时候取的」。
  // 存全局一份，渲染函数据此在页脚显示「数据时间 HH:MM」——看到旧的不再是黑盒。
  const dt = r.headers.get('X-Data-Time');
  if (dt) lastDataTime = dt;
  const d = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
  return d;
}
function toast(msg, cls) {
  const el = document.createElement('div');
  el.className = 'tst ' + (cls || '');
  el.textContent = msg;
  $('toasts').appendChild(el);
  setTimeout(() => el.remove(), 3600);
}

/* ── 统一提示浮层 ────────────────────────────────────────────────────
   全站只保留**一种**提示样式：白底 + 发丝边 + 浮层阴影，和图表悬停卡一致。

   之前有三套，同一个东西三种长相：
     · 浏览器原生 title（几百处）——样式完全改不了，各系统还不一样
     · .q 的 ::after 黑气泡（只有"账号数建议"用）
     · .chart-tip 白卡片（图表）

   原生 title 既然改不了样式，就只能**拦截**：悬停时把它挪进 data-tip 并摘掉原属性
   （不摘的话原生提示会在自定义浮层之后紧接着弹第二个框），移开再放回去——
   放回去是为了不丢原生语义（无障碍工具仍能读到）。

   用事件委托挂在 document 上，所以**动态生成的元素自动适用**，不必挨个改标记；
   这也是"所有"能做到的原因。 */
let _tipEl = null;      // 延迟创建：首屏不需要它，别白加一个常驻 DOM
let _tipOwner = null;   // 当前正在提示的元素
let _tipTitle = null;   // 被临时摘下的 title 原文
let _tipW = 0, _tipH = 0; // 浮层尺寸缓存（每次 mousemove 都读 offsetWidth 会反复触发布局）

function tipEl() {
  if (!_tipEl) {
    _tipEl = document.createElement('div');
    _tipEl.className = 'pop tip';
    document.body.appendChild(_tipEl);
  }
  return _tipEl;
}

// placeTip 把浮层摆到光标附近：默认在上方居中，上方放不下就翻到下方，左右夹在视口内。
// 夹边是必须的——表格最右列的元素若按光标居中，提示会顶出屏幕看不到。
function placeTip(el, x, y) {
  const vw = window.innerWidth || 1200;
  el.style.left = Math.max(8, Math.min(vw - _tipW - 8, x - _tipW / 2)) + 'px';
  el.style.top = (y - _tipH - 14 < 8 ? y + 18 : y - _tipH - 14) + 'px';
}

function showTip(text, x, y) {
  const el = tipEl();
  el.textContent = text; // 提示文本全是自造文本，不解析 HTML
  el.classList.add('on');
  // 量一次尺寸就缓存：offsetWidth/Height 会强制同步布局，放在 mousemove 里
  // 等于每个像素都重排一次——页面上悬浮着整张表格时肉眼可见地卡。
  _tipW = el.offsetWidth || 160;
  _tipH = el.offsetHeight || 36;
  placeTip(el, x, y);
}

function hideTip() {
  if (_tipEl) _tipEl.classList.remove('on');
  if (_tipOwner && _tipTitle !== null) {
    _tipOwner.setAttribute('title', _tipTitle);
    _tipTitle = null;
  }
  _tipOwner = null;
}

// tipWanted 这个元素该不该弹提示。
//
// **自己有文字的按钮不弹**：按钮上已经写着"刷新""复制""清屏"，再浮一个框纯属噪音。
// 但**图标按钮**（只有 SVG、没有文字）要保留——不弹的话就没人知道它是干什么的。
//
// 判据用"有没有可见文字"而不是列举白名单：列举法每加一个新按钮都得记得补，
// 漏了就静默失效；"没有文字才弹"这条规则自动适用于以后所有按钮。
function tipWanted(el) {
  if (!el || (el.tagName || '').toUpperCase() !== 'BUTTON') return true;
  return (el.textContent || '').trim() === '';
}

document.addEventListener('mouseover', ev => {
  const t = ev.target && ev.target.closest ? ev.target.closest('[data-tip],[title]') : null;
  if (!t || t === _tipOwner) return;
  // 不该弹的（有文字的按钮）**也要把 title 摘掉**——留着的话浏览器会自己弹原生提示，
  // 那就等于白改。摘掉之后这块地方彻底安静。
  if (!tipWanted(t)) {
    if (t.hasAttribute('title')) t.removeAttribute('title');
    return;
  }
  hideTip(); // 换目标：先把上一个的 title 装回去
  let text = t.getAttribute('data-tip');
  if (text == null) text = t.getAttribute('title');
  if (!text) return;
  _tipTitle = t.hasAttribute('title') ? t.getAttribute('title') : null;
  if (_tipTitle !== null) t.removeAttribute('title'); // 摘掉原生提示，否则会紧接着弹第二个框
  _tipOwner = t;
  showTip(text, ev.clientX, ev.clientY);
});

document.addEventListener('mousemove', ev => {
  if (!_tipOwner) return;
  // 目标已被重渲染换掉（仪表盘/明细表都在定时重绘）：mouseout 不会再来，
  // 不主动收就会在屏幕上挂一个永远不消失的浮层。
  if (document.contains && !document.contains(_tipOwner)) { hideTip(); return; }
  placeTip(tipEl(), ev.clientX, ev.clientY);
});

document.addEventListener('mouseout', ev => {
  if (!_tipOwner) return;
  // 移到自己的子元素上不算离开（mouseout 会从子元素冒泡上来）
  if (ev.relatedTarget && _tipOwner.contains && _tipOwner.contains(ev.relatedTarget)) return;
  hideTip();
});

/* ── 原生控件换成自定义外观 ──────────────────────────────────────────
   为什么必须自己做：<select> 展开的那个列表、checkbox 的勾、数字框的箭头，
   都是**浏览器/系统画的**，CSS 碰不到——外面再美化，点开那一瞬还是系统长相。

   核心取舍：**原生元素保留、只做外观层**。
   `collectConfig()` 走 `f.elements[name]`，`loadConfig()` 读写 `el.checked`/`el.value`，
   业务代码全按原生控件写的。所以自定义控件负责：
     读 → 从原生元素取选项与当前值
     写 → 用户选了之后写回原生 value/checked，并派发 change 事件
   这样业务代码一行都不用改，也不会出现"外观变了、保存失效"这种最恶心的 bug。 */

// pickDropdownOption 选定第 i 项：写回**原生** select 并派发 change。
//
// 为什么把它单独抽出来：这是整套自定义外观里**唯一真正要紧的契约**——
// 外观层必须把用户的选择交回原生元素，否则表现就是"看着能点、保存无效"，
// 是最难发现的一类 bug。抽成独立函数后可以直接测，不需要真浏览器
//（造 DOM 的代码没法在 Node 里测，但这段可以）。
// 返回是否真的换了选项。
function pickDropdownOption(sel, i) {
  if (!sel || !sel.options) return false;
  if (i < 0 || i >= sel.options.length) return false;
  if (sel.selectedIndex === i) return false; // 选的是同一项：不派发，免得白触发一次重载
  sel.selectedIndex = i;
  sel.dispatchEvent(new Event('change', { bubbles: true }));
  return true;
}

// toggleCheckbox 翻转**原生** checkbox 并派发 change。点自定义开关走这里——
// 只改外观不改原生的话，配置保存时会拿到没变过的旧值。
function toggleCheckbox(cb) {
  if (!cb) return;
  cb.checked = !cb.checked;
  cb.dispatchEvent(new Event('change', { bubbles: true }));
}

// dropdownLabel 原生 select 当前选中项的文字（自定义按钮显示它）。
function dropdownLabel(sel) {
  const o = sel && sel.options ? sel.options[sel.selectedIndex] : null;
  return o ? o.textContent : '';
}

// dropdownFor select → 自定义下拉。幂等：同一个 select 调多次只建一层外观。
function dropdownFor(sel) {
  if (!sel || sel.dataset.ddDone) return;
  sel.dataset.ddDone = '1';

  const wrap = document.createElement('div');
  wrap.className = 'dd';
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'dd-btn';
  btn.setAttribute('aria-haspopup', 'listbox');
  btn.setAttribute('aria-expanded', 'false');
  btn.innerHTML = '<span class="dd-t"></span>' +
    '<svg class="dd-c" viewBox="0 0 16 16" width="12" height="12" fill="none" ' +
    'stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true">' +
    '<path d="M4 6.4 8 10.4l4-4"/></svg>';
  const menu = document.createElement('div');
  menu.className = 'pop dd-menu';
  menu.hidden = true;
  menu.setAttribute('role', 'listbox');

  sel.parentNode.insertBefore(wrap, sel);
  wrap.appendChild(sel);
  wrap.appendChild(btn);
  wrap.appendChild(menu);
  sel.classList.add('native-hidden');

  const label = () => {
    const o = sel.options[sel.selectedIndex];
    return o ? o.textContent : '';
  };

  const close = () => { menu.hidden = true; wrap.classList.remove('open'); btn.setAttribute('aria-expanded', 'false'); };
  const open = () => {
    // 靠右时翻到右边对齐，免得菜单顶出窗口（和筛选菜单同一套判据思路）
    const r = wrap.getBoundingClientRect();
    const vw = window.innerWidth || 1200;
    menu.classList.toggle('right', (vw - r.left) < 200);
    menu.hidden = false; wrap.classList.add('open'); btn.setAttribute('aria-expanded', 'true');
  };

  btn.addEventListener('click', ev => {
    ev.stopPropagation();
    // 先关掉其它已打开的下拉：同时开着两个会让人不知道点的是哪个
    closeAllDropdowns(wrap);
    if (menu.hidden) { paint(); open(); } else close();
  });

  // 每次打开都重画：选项可能被别处重建过（如请求审计的日期列表）
  function paint() {
    btn.querySelector('.dd-t').textContent = label();
    menu.innerHTML = Array.from(sel.options).map((o, i) =>
      '<button type="button" class="dd-o' + (i === sel.selectedIndex ? ' on' : '') + '" data-i="' + i + '" role="option">' +
      '<span class="dd-ck"><svg viewBox="0 0 16 16" width="13" height="13" fill="none" ' +
      'stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
      '<path d="M3.2 8.4 6.6 11.8 12.8 4.8"/></svg></span>' +
      esc(o.textContent) + '</button>').join('');
  }

  menu.addEventListener('click', ev => {
    const o = ev.target.closest ? ev.target.closest('.dd-o') : null;
    if (!o) return;
    pickDropdownOption(sel, Number(o.dataset.i));
    syncSelect(sel);
    close();
  });

  // 原生 change（代码改 sel.value 后也可能派发）也要同步外观
  sel.addEventListener('change', () => { paint(); btn.querySelector('.dd-t').textContent = label(); });
  syncSelect(sel);
}

// syncSelect 刷新某个 select 的外观（选项/选中值变了之后调）。
// 幂等且安全：没被包装过的 select 直接跳过。
function syncSelect(sel) {
  if (!sel || !sel.dataset.ddDone) return;
  const wrap = sel.closest ? sel.closest('.dd') : null;
  if (!wrap) return;
  const btn = wrap.querySelector('.dd-btn');
  const o = sel.options[sel.selectedIndex];
  if (btn) btn.querySelector('.dd-t').textContent = o ? o.textContent : '';
}

function closeAllDropdowns(except) {
  document.querySelectorAll('.dd').forEach(w => {
    if (w === except) return;
    w.classList.remove('open');
    const m = w.querySelector('.dd-menu');
    if (m) m.hidden = true;
    const b = w.querySelector('.dd-btn');
    if (b) b.setAttribute('aria-expanded', 'false');
  });
}

// switchFor checkbox → 自定义开关。原生 checkbox 留作数据源。
function switchFor(cb) {
  if (!cb || cb.dataset.swDone) return;
  cb.dataset.swDone = '1';
  const sw = document.createElement('span');
  sw.className = 'tgl';
  sw.setAttribute('role', 'switch');
  sw.tabIndex = 0;
  const paint = () => {
    sw.classList.toggle('on', !!cb.checked);
    sw.setAttribute('aria-checked', cb.checked ? 'true' : 'false');
    sw.setAttribute('tabindex', '0');
  };
  // 点开关 → 翻转原生 checkbox → 让它自己派发 change，
  // 这样任何监听 change 的业务逻辑都照常触发（不是只改外观）。
  const toggle = ev => {
    ev.preventDefault();
    toggleCheckbox(cb);
    paint();
  };
  sw.addEventListener('click', toggle);
  sw.addEventListener('keydown', ev => {
    if (ev.key === ' ' || ev.key === 'Enter') toggle(ev);
  });
  cb.addEventListener('change', paint);
  cb.parentNode.insertBefore(sw, cb.nextSibling);
  cb.classList.add('native-hidden');
  paint();
}

// enhanceControls 把所有原生控件套上自定义外观。
//
// 放在 app.js 末尾、业务代码之后调用：让业务代码先按原生控件把自己初始化好
// （loadConfig 要往 el.checked/el.value 里写），再套外观去读最终状态。
function enhanceControls() {
  document.querySelectorAll('select').forEach(dropdownFor);
  document.querySelectorAll('input[type=checkbox]').forEach(switchFor);
}

// 点空白处关掉所有自定义下拉，以及打开的 <details> 菜单。
// <details> 原生行为是"再点一次 summary 才关"，点别处它一直开着——
// 而它看起来就是个弹出菜单，用户会理所应当地点外面关掉它。
document.addEventListener('click', ev => {
  closeAllDropdowns(null);
  document.querySelectorAll('details[open]').forEach(d => {
    if (ev.target && d.contains && d.contains(ev.target)) return;
    d.open = false;
  });
});
// Esc 关闭
document.addEventListener('keydown', ev => {
  if (ev.key !== 'Escape') return;
  closeAllDropdowns(null);
  document.querySelectorAll('details[open]').forEach(d => { d.open = false; });
});

/* ask 自建确认弹窗。返回 Promise<boolean>。
   为什么不用原生 confirm：一是样式改不了、和面板完全两个气质；二是长文案在原生弹窗里
   挤成一坨，用户容易看漏关键信息（比如「不可恢复」）。这里允许标题 + 正文 + 危险色。 */
let _askResolve = null;
function ask(text, opts) {
  const o = opts || {};
  $('cfmTitle').textContent = o.title || '确认操作';
  $('cfmText').textContent = text;
  const ok = $('btnCfmOk');
  ok.textContent = o.ok || '确认';
  ok.className = o.danger ? 'primary' : 'primary';
  ok.classList.toggle('danger', !!o.danger);
  $('cfmVeil').classList.add('on');
  setTimeout(() => ok.focus(), 60);
  return new Promise(res => { _askResolve = res; });
}
function _closeAsk(v) {
  $('cfmVeil').classList.remove('on');
  if (_askResolve) { const r = _askResolve; _askResolve = null; r(v); }
}
$('btnCfmOk').onclick = () => _closeAsk(true);
$('btnCfmCancel').onclick = () => _closeAsk(false);
$('cfmVeil').addEventListener('click', ev => { if (ev.target === $('cfmVeil')) _closeAsk(false); });
document.addEventListener('keydown', ev => {
  if (ev.key === 'Escape' && $('cfmVeil').classList.contains('on')) _closeAsk(false);
  if (ev.key === 'Enter' && $('cfmVeil').classList.contains('on')) _closeAsk(true);
});
// esc 文本/属性双安全转义。不能只用 div.innerHTML（它转义 <>& 但不转义引号），
// 否则字符串拼进 HTML 属性（如 title="uid: ..."）时引号可闭合属性并注入事件处理器。
// 显式替换 5 个字符：& < > " '（& 必须最先，避免二次转义）。
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}
function ago(iso) {
  if (!iso || iso.startsWith('0001-')) return '—';
  const s = (Date.now() - new Date(iso)) / 1000;
  if (s < 0) return '刚刚';
  if (s < 60) return Math.floor(s) + ' 秒前';
  if (s < 3600) return Math.floor(s / 60) + ' 分钟前';
  if (s < 86400) return Math.floor(s / 3600) + ' 小时前';
  return Math.floor(s / 86400) + ' 天前';
}
function dur(sec) {
  sec = Math.max(0, Math.round(sec));
  const h = Math.floor(sec / 3600), m = Math.floor(sec % 3600 / 60), s = sec % 60;
  return h ? h + '时' + String(m).padStart(2, '0') + '分' : m ? m + '分' + String(s).padStart(2, '0') + '秒' : s + '秒';
}

/* ── 密钥门 ───────────────────────────────────────────────────────── */
function openKey() { $('keyVeil').classList.add('on'); setTimeout(() => $('keyInput').focus(), 60); }
$('btnKey').onclick = async () => {
  const v = $('keyInput').value.trim();
  if (!v) return;
  localStorage.setItem(LS_KEY, v);
  try {
    await api('overview');
    $('keyErr').hidden = true;
    $('keyVeil').classList.remove('on');
    start();
  } catch (e) { $('keyErr').hidden = false; }
};
$('keyInput').addEventListener('keydown', e => { if (e.key === 'Enter') $('btnKey').click(); });

/* ── 仪表盘 ─────────────────────────────────────────────────────────
   数据源 /panel/api/stats：用量指标（落盘，重启后仍在）。
   本视图**只读**——不做任何运维动作，纯观测，与账号池的职责分开。

   「预估可用 token」是估算而非承诺，口径写在卡片 title 里：
   用上游返回的「已消耗积分增量 ÷ 本进程 token 增量」求实测比率，再乘剩余积分。
   已知偏差：签到/旅行/夜猫子等后台任务消耗的积分进了分子，但它们的 token 不经
   /v1 计数、不进分母 → 比率偏大 → 预估偏小。方向保守，可接受。              */

// fmtInt 千分位整数；null/undefined 显示「-」（区别于真实的 0）。
function fmtInt(n) {
  if (n == null) return '-';
  return Number(n).toLocaleString('en-US');
}

// fmtTok token 数缩写：1234 → 1.2k，1234567 → 123万，42138357788 → 421亿。
//
// 十亿以上改用**中文单位（亿 / 万亿）**，不再用 B。
// 原来的 `n/1e9 + 'B'` 有两个问题，都真实咬过人：
//   1. 中文界面里 B 是外来单位，看不出是十亿还是亿（1B = 10 亿，不是 1 亿）；
//   2. 量级一旦上百亿就只剩两位小数，`63.00B` 这种显示把
//      「知道大概多少」也丢掉了 —— 用户原话：「63b 我实际是不知道多少的」。
// 中文单位下 1 亿 = 1e8，量级跳一级要多 10 倍数字，两位小数比 B 精确一个数量级。
//
// 注意：这张表**只用于不追求精确的场合**（卡片主数字、图表轴、表格概览）。
// 需要确切数值的地方用 fmtInt（千分位完整值），两者常并排出现。
function fmtTok(n) {
  n = Number(n) || 0;
  if (n < 1000) return String(n);
  if (n < 10000) return (n / 1000).toFixed(1) + 'k';
  if (n < 1000000) return Math.round(n / 1000) + 'k';
  if (n < 100000000) {
    // 去掉取整产生的多余 ".0"：9999999 ÷ 1e4 = 999.9999 → toFixed(1) → "1000.0万"，
    // 看着像四个有效数字其实只有三位有效。剥掉更干净，也不影响其它值。
    return ((n / 10000).toFixed(n < 10000000 ? 1 : 0) + '万').replace(/\.0(?=万)/, '');
  }
  if (n < 1e12) return (n / 100000000).toFixed(2) + '亿';
  return (n / 1e12).toFixed(2) + '万亿';
}

// fmtRate 「积分/百万token」单价：按量级选小数位，不用固定的 toFixed(2)。
//
// 为什么不能固定两位：缓存命中率极高的环境单价可低到千分位（如 0.097）。
// 固定两位会把 0.097 显示成 "0.10"，更极端时直接 "0.00"——数字是算对的，
// 但看起来像没数据，比直接显示「—」更有误导性。所以小到一定程度就多给有效数字。
function fmtRate(v) {
  v = Number(v) || 0;
  if (v <= 0) return '—';
  return v < 0.01 ? v.toFixed(4) : v.toFixed(2);
}

// fmtMs 毫秒；<=0 视为「未观测到」（非流式请求没有 TTFB），显示破折号。
function fmtMs(v) {
  v = Number(v) || 0;
  if (v <= 0) return '—';
  return v >= 1000 ? (v / 1000).toFixed(2) + 's' : Math.round(v) + 'ms';
}

// pctOf a 占 b 的百分比；b<=0 时返回 0（而不是 NaN 或 Infinity）。
function pctOf(a, b) {
  return b > 0 ? (a / b) * 100 : 0;
}

// uptime 运行时长：跨天折成「N天H时」，不足一天复用 dur()。
function uptime(sec) {
  sec = Math.max(0, Math.round(sec));
  const d = Math.floor(sec / 86400);
  if (d > 0) return d + '天' + Math.floor((sec % 86400) / 3600) + '时';
  return dur(sec);
}

// hhmm / clock 自行补零，不用 toLocaleTimeString——后者依赖运行环境 locale，
// 在 Node 假 DOM 与浏览器里结果可能不同，测试就没法断言了。
function hhmm(ms) {
  const d = new Date(ms);
  return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
}
function clock(ms) {
  const d = new Date(ms);
  return hhmm(ms) + ':' + String(d.getSeconds()).padStart(2, '0');
}

// agoSec 把「距今秒数」说成人话。
function agoSec(s) {
  s = Math.max(0, Math.round(s));
  if (s < 60) return s + ' 秒前';
  if (s < 3600) return Math.floor(s / 60) + ' 分钟前';
  if (s < 86400) return Math.floor(s / 3600) + ' 小时前';
  return Math.floor(s / 86400) + ' 天前';
}

/* ── 指标条（单价与性能）────────────────────────────────────────────
   这一层是「次级指标」：有价值但没必要各占一张卡。此前它们渲染成 8 张
   与首行同质的数字卡——同屏 13 张卡片长得一模一样，是「重复元素」观感的
   主要来源。现在收敛成一条紧凑指标带（.mstrip），无卡片 chrome，悬停看口径。 */
function mi(label, value, tip, vcls) {
  return '<div class="mi"' + (tip ? ' title="' + esc(tip) + '"' : '') + '>' +
    '<span class="k">' + esc(label) + '</span>' +
    '<span class="v' + (vcls ? ' ' + vcls : '') + '">' + value + '</span></div>';
}

// kpi 生成一张 KPI 卡（「请求审计」页的 KPI 行仍在用它；仪表盘已改用 mi 指标带）。
function kpi(label, value, cls, sub, tip, vcls) {
  const info = tip
    ? '<svg class="info" viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><circle cx="8" cy="8" r="6.2"/><path d="M8 7.4v4M8 5.05v.02"/></svg>'
    : '';
  return '<div class="stat' + (cls ? ' ' + cls : '') + '">' +
    (sub ? '<div class="sub">' + esc(sub) + '</div>' : '') +
    '<div class="v' + (vcls ? ' ' + vcls : '') + '">' + value + '</div>' +
    '<div class="k"' + (tip ? ' title="' + esc(tip) + '"' : '') + '>' +
    '<span>' + esc(label) + '</span>' + info + '</div>' +
    '</div>';
}

// dashEmpty 空状态占位行。cols 必须等于该表的 <th> 数，否则表格塌陷。
function dashEmpty(cols, msg) {
  return '<tr><td colspan="' + cols + '"><div class="dash-empty">' + esc(msg) + '</div></td></tr>';
}

/* ── 英雄 KPI（仪表盘首行）──────────────────────────────────────────
   首行是全页的视觉锚点：彩色图标芯片（.ico + t-* 色对）+ 标签 + 大数字。
   以前的方案是三行同质的米白数字卡——同屏没有任何视觉重心，这是「界面显得素」
   的最大来源。现在首行五卡各占一色，下方两行继续用普通 kpi()（无图标、更密），
   层次自然拉开：hero 抢眼，细节数字安静排队。 */
const KPI_ICONS = {
  pulse: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M1.8 8h2.9l1.8-4.6 3.1 9.2 1.8-4.6h2.8"/></svg>',
  check: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6.2"/><path d="M5.4 8.2l1.8 1.8 3.4-3.6"/></svg>',
  stack: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"><path d="M8 2.2 14 5.4 8 8.6 2 5.4z"/><path d="M2 8.4l6 3.2 6-3.2M2 11.2l6 3.2 6-3.2"/></svg>',
  coin: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"><circle cx="8" cy="8" r="6.1"/><path d="M5.6 5.2h4.8M5.6 7.6h4.8M8 5.2V12"/></svg>',
  clock: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"><circle cx="8" cy="8" r="6.1"/><path d="M8 4.6V8l2.4 1.6"/></svg>',
};

// kpiCard 英雄卡（横向：图标在左，标签/数字/副行在右，比竖排省近一半高度）。
// o 可选：tone（t-blue/t-violet/t-green/t-amber/t-rose/t-cyan）、icon（KPI_ICONS 键）、
// cls（挂在 .stat 上，复用 good/warn/bad 数字着色）、sub、tip。
function kpiCard(label, value, o) {
  o = o || {};
  const info = o.tip
    ? '<svg class="info" viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><circle cx="8" cy="8" r="6.2"/><path d="M8 7.4v4M8 5.05v.02"/></svg>'
    : '';
  return '<div class="stat hero' + (o.cls ? ' ' + o.cls : '') + '">' +
    '<div class="ico ' + (o.tone || 't-blue') + '">' + (KPI_ICONS[o.icon] || '') + '</div>' +
    '<div class="hero-body">' +
    '<div class="k"' + (o.tip ? ' title="' + esc(o.tip) + '"' : '') + '><span>' + esc(label) + '</span>' + info + '</div>' +
    '<div class="v">' + value + '</div>' +
    (o.sub ? '<div class="sub">' + esc(o.sub) + '</div>' : '') +
    '</div></div>';
}

// dashKeyCell 密钥列：优先显示名字（key_names 映射），查不到就显示短指纹。
//
// 为什么必须有回落：metrics 里的指纹是**写入当时**的密钥身份，密钥被删掉或
// 改名后映射就查不到了。这时显示短指纹而不是留白——留白会被读成"没有密钥"，
// 而事实是"有一把已经不存在的密钥留下了流量"，这两件事的处置完全不同。
function dashKeyCell(fp) {
  const name = keyNames[fp];
  const short = fp ? fp.slice(0, 8) : '未鉴权';
  const sub = name && name !== short ? '<div class="id">' + esc(short) + '</div>' : '';
  return '<td class="who"><div class="nm">' + esc(name || short) + '</div>' + sub + '</td>';
}

// dashNum 数字单元格：0 用浅色，避免一片黑压过重点。
function dashNum(v, color) {
  const cls = v ? '' : ' style="color:var(--ink-3)"';
  return '<td class="num"' + (color ? ' style="color:' + color + '"' : cls) + '>' + fmtInt(v) + '</td>';
}

function renderDashboard(s) {
  if (!s) return;
  const t = s.totals || {};
  const c = s.credits || {};
  const models = s.models || [];

  const pool = (overviewData && overviewData.accounts) || [];
  const poolOk = pool.reduce((a, x) => a + (x.success_count || 0), 0);
  const poolErr = pool.reduce((a, x) => a + (x.err_total || 0), 0);
  const poolReq = poolOk + poolErr;
  const poolIn = pool.reduce((a, x) => a + (x.prompt_tokens || 0), 0);
  const poolOut = pool.reduce((a, x) => a + (x.completion_tokens || 0), 0);
  const poolCached = pool.reduce((a, x) => a + (x.cached_tokens || 0), 0);
  const req = t.requests || poolReq;
  const okN = t.requests ? t.ok : poolOk;
  const errN = t.requests ? t.errors : poolErr;
  const inTok = t.prompt_tokens || poolIn;
  const outTok = t.completion_tokens || poolOut;
  const totTok = t.total_tokens || (poolIn + poolOut);
  const cachedTok = t.cached_tokens || poolCached;
  const okRate = pctOf(okN, req);

  /* 可视化首屏：token 构成条 + 积分水位。
     这两块回答的是「量花在哪、还剩多少」——表格里得逐行读，这里一眼看到比例。 */
  renderCreditViz(c);
  renderExpiryChip(c, c.remain || 0);

  /* 可视化：模型占比（累计口径，与按模型表一致）、余额 24h 走势、
     近 7 天积分消耗（趋势接口按天聚合）、近 7 天趋势（独立接口，60 秒节流）。
     原「账号积分分布」卡与账号管理页完全重复，已撤。 */
  renderDonut(models);
  _lastDailyNet = c.daily_net || [];
  renderCreditBurn();
  loadTrend(false);

  /* 首行：4 个英雄 KPI（彩色图标芯片）。运行时长不在这里——顶栏 meta 已常驻显示，
     再做一张卡就是重复元素；首行只留「量与钱」。 */
  const okCls = req ? (okRate >= 99 ? 'good' : okRate >= 90 ? 'warn' : 'bad') : '';
  $('dashCards').innerHTML =
    kpiCard('请求总数', fmtInt(req), { tone: 't-blue', icon: 'pulse',
      sub: t.requests ? null : '账号累计',
      tip: '累计处理的 /v1 请求数（含失败，写在账号文件里，重启还在）' }) +
    kpiCard('成功率', req ? okRate.toFixed(1) + '%' : '—',
      { tone: 't-green', icon: 'check', cls: okCls,
        sub: req ? fmtInt(okN) + ' / ' + fmtInt(errN) : null, tip: '成功 / 失败' }) +
    kpiCard('token 合计', fmtTok(totTok), { tone: 't-violet', icon: 'stack',
      sub: fmtInt(totTok), tip: '输入 + 输出，重启后继续累加' }) +
    kpiCard('剩余积分', fmtInt(c.remain), { tone: 't-amber', icon: 'coin',
      sub: c.age_sec >= 0 ? agoSec(c.age_sec) + '刷新' : null,
      tip: '上游 billing 返回的剩余积分合计（各账号明细见「账号」页）' });

  /* 次级指标：一条紧凑指标带。缓存命中率不再单列——token 构成条图例已给
     同一个数，表格里还有逐模型列，三处重复只剩噪音。
     判据用 samples 而不是 per_mtoken > 0：缓存命中率极高的环境单价可低到
     千分位以下，拿它当「有没有数据」的开关，精度一变就会把「算出来了」误判成「样本不足」。 */
  const rateKnown = c.samples > 0 && c.per_mtoken > 0;
  const estReady = c.samples > 0 && c.est_tokens > 0;
  $('dashCredit').innerHTML =
    mi('预估可用 token', estReady ? fmtTok(c.est_tokens) : '—',
      '剩余积分 ÷ 实测比率，按实测比率外推。这是估算不是承诺：积分也会被签到/任务消耗，上游计费口径也可能变' +
        (estReady ? '\n当前估算：' + fmtInt(c.est_tokens) + ' token' : ''),
      estReady ? '' : 'warn') +
    mi('实测 积分/百万token', rateKnown ? fmtRate(c.per_mtoken) : '—',
      '全池加权平均：累计上游已消耗积分 ÷ 累计本机 token（大请求话语权大，不是逐请求算术平均）。\n' +
      '签到/旅行等后台任务消耗的积分计入分子但 token 不在分母 → 比率偏大 → 预估值偏保守' +
        (c.samples > 0 ? '\n样本：' + fmtInt(c.samples) + ' 个采样区间' : ''),
      rateKnown ? '' : 'warn');
  $('dashQuality').innerHTML =
    mi('平均·请求', req && totTok ? fmtTok(Math.round(totTok / req)) : '—',
      '每个请求平均消耗的 token（输入 + 输出）') +
    mi('平均 tok/s', t.tokens_per_sec ? t.tokens_per_sec.toFixed(1) : '—',
      '按「输出 token ÷ 请求总时长」占比加权，不是各请求速率的算术平均（后者会被一个极短请求带偏）') +
    mi('平均 TTFB', fmtMs(t.avg_ttfb_ms),
      '仅流式请求：首个 data 帧到达耗时；非流式无此项') +
    mi('缺 usage', fmtInt(t.missing_usage),
      '返回 200 却没有 usage 的请求数。持续 >0 说明上游流被截断或 usage 格式变了',
      t.missing_usage > 0 ? 'warn' : '') +
    mi('覆盖模型', fmtInt(models.length), '本次进程内实际跑过的模型数');

  /* 按模型：默认只展开前三名（二八定律——头部模型覆盖几乎全部流量），
     其余折叠进「展开」按钮。模型总数会随上游增长，不折叠的话这张表
     会把整个仪表盘越撑越长。 */
  _lastModels = models;
  renderModelTable();

  /* 按密钥：谁在消耗。密钥名来自 key_names（指纹 → 名字），
     取不到名字就显示短指纹——留白会被读成「这一行没有密钥」。 */
  const ks = s.keys || [];
  const maxKeyTok = Math.max(1, ...ks.map(k => k.total_tokens || 0));
  $('dashKeyBody').innerHTML = ks.map(k =>
    '<tr>' + dashKeyCell(k.key) +
    dashNum(k.requests) +
    '<td class="num">' + (k.prompt_tokens ? fmtTok(k.prompt_tokens) : '—') + '</td>' +
    '<td class="num">' + (k.completion_tokens ? fmtTok(k.completion_tokens) : '—') + '</td>' +
    '<td class="num">' + (k.total_tokens ? fmtTok(k.total_tokens) : '—') +
      (k.total_tokens ? '<div class="mini-bar"><i style="width:' +
        (k.total_tokens / maxKeyTok * 100).toFixed(1) + '%"></i></div>' : '') + '</td>' +
    // 积分列是这行的主语：token 量不等于扣费额（倍率/缓存差异大），只有上游实扣数可信。
    '<td class="num"' + (k.credits ? '' : ' style="color:var(--ink-3)"') + '>' + (k.credits ? fmtCredit(k.credits) : '—') + '</td>' +
    '<td class="num">' + (k.total_tokens && k.requests ? fmtTok(Math.round(k.total_tokens / k.requests)) : '—') + '</td>' +
    dashNum(k.errors, k.errors ? 'var(--bad)' : '') +
    '</tr>').join('') || dashEmpty(8, '还没有密钥用量。');
  $('dashKeyNote').textContent = ks.length
    ? ks.length + ' 把密钥有流量（重启后继续累计）'
    : '';
}

// niceMax 把轴上限取整到 1/2/5 × 10^k。
//
// 直接拿最大值当上限的话，刻度会是 287764 / 215823 / … 这种读不出来的数；
// 取整之后顶格是 300k，中间几档也全是整数。
function niceMax(v) {
  if (!(v > 0)) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  const d = v / p;
  return (d <= 1 ? 1 : d <= 2 ? 2 : d <= 5 ? 5 : 10) * p;
}

/* ── 模型占比环形图（累计口径，与「按模型」表同源）────────────────────
   前 6 名各占一段（--d0..--d5），其余合并成「其他」（--d6）。段用 SVG 的
   stroke-dasharray 画（圆环 = 一根粗描边），颜色走内联 stroke 属性——
   不给段挂类名，从根上避开「CSS 全局规则改写 SVG 形状」这类事故。 */
function renderDonut(models) {
  const box = $('dashDonut');
  if (!box) return;
  const sub = $('dashDonutSub');
  const list = (models || []).filter(m => (m.total_tokens || 0) > 0);
  if (!list.length) {
    if (sub) sub.textContent = '';
    box.innerHTML = '<div class="viz-empty" style="padding:30px 0">还没有模型用量。发一次 /v1 请求后回来刷新。</div>';
    return;
  }
  const topN = 6;
  const parts = list.slice(0, topN).map(m => ({ name: m.key, v: m.total_tokens || 0 }));
  const restTok = list.slice(topN).reduce((a, m) => a + (m.total_tokens || 0), 0);
  if (restTok > 0) parts.push({ name: '其他 ×' + (list.length - topN), v: restTok });
  const tot = parts.reduce((a, p) => a + p.v, 0);

  const S = 150, R = 55, CXY = S / 2, SW = 21;
  const C = 2 * Math.PI * R;
  let segs = '', off = 0;
  parts.forEach((p, i) => {
    const len = (p.v / tot) * C;
    segs += '<circle cx="' + CXY + '" cy="' + CXY + '" r="' + R + '" fill="none"' +
      ' stroke="var(--d' + Math.min(i, 6) + ')" stroke-width="' + SW + '" stroke-opacity=".92"' +
      ' stroke-dasharray="' + Math.max(0, len - 1.5).toFixed(2) + ' ' + (C - Math.max(0, len - 1.5)).toFixed(2) + '"' +
      ' stroke-dashoffset="' + (-off).toFixed(2) + '"><title>' +
      esc(p.name + ' · ' + fmtTok(p.v) + ' token · ' + (p.v / tot * 100).toFixed(1) + '%') + '</title></circle>';
    off += len;
  });
  const center =
    '<text class="ch-donut-c" x="' + CXY + '" y="' + (CXY - 1) + '" text-anchor="middle" font-size="17" font-weight="600">' +
      fmtTok(tot) + '</text>' +
    '<text class="ch-donut-s" x="' + CXY + '" y="' + (CXY + 17) + '" text-anchor="middle">累计 token</text>';
  const legend = parts.map((p, i) =>
    '<div class="it" title="' + esc(p.name + ' ' + fmtInt(p.v) + ' token') + '">' +
      '<span class="sw" style="background:var(--d' + Math.min(i, 6) + ')"></span>' +
      '<span class="nm">' + esc(p.name) + '</span>' +
      '<b>' + fmtTok(p.v) + '</b>' +
      '<span class="pct">' + (p.v / tot * 100).toFixed(1) + '%</span></div>').join('');
  if (sub) sub.textContent = list.length + ' 个模型';
  box.innerHTML = '<div class="donut-wrap">' +
    '<svg width="' + S + '" height="' + S + '" viewBox="0 0 ' + S + ' ' + S +
      '" role="img" aria-label="模型 token 占比">' +
      '<g transform="rotate(-90 ' + CXY + ' ' + CXY + ')">' + segs + '</g>' + center + '</svg>' +
    '<div class="donut-legend">' + legend + '</div></div>';
}

// ── 图表悬停提示 ────────────────────────────────────────────────────
// 为什么不用 SVG 的 <title>：它只覆盖柱子本身（细长一条），鼠标滑过柱子之间的
// 缝隙、或者停在折线上时毫无反应，而且必须等系统默认的延迟才弹。
// 这里改成整块绘图区都能触发，并配一条竖线指出当前命中哪个小时。
// rafMove 把 mousemove 处理器包成每帧最多执行一次：鼠标在图上快速划过时
// 高频事件里做 getBoundingClientRect + 样式写入，会跟浏览器渲染互相踩（掉帧）。
// 节流后同一帧内只算最后一次位置，视觉完全无损，绘制压力降一个数量级。
function rafMove(fn) {
  // 假 DOM（测试桩）没有 requestAnimationFrame：退回同步执行，语义不变。
  const raf = typeof requestAnimationFrame === 'function' ? requestAnimationFrame
    : function (cb) { cb(); };
  let queued = false, lastEv = null;
  return function (ev) {
    lastEv = ev;
    if (queued) return;
    queued = true;
    raf(() => { queued = false; fn(lastEv); });
  };
}

// keyNames 密钥指纹 → 显示名（来自 /panel/api/stats 的 key_names）。
// metrics 只认指纹，名字是展示层的事，所以映射放在前端。
// 取不到名字的指纹（密钥已删/改名前的旧流水）回落显示短指纹，而不是留白——
// 留白会让人以为那一行没有密钥。
let keyNames = {};

/* ── 近 14 天用量趋势（/panel/api/trend，audit 流水按天聚合）──────────
   与积分流水同一份按天数据（trendData.days）：趋势卡画 token/请求双轴，
   流水卡画积分消耗/获取。拉取按 60 秒节流（loadTrend），窗口 14 天。 */
let trendData = null, trendAt = 0, trendRealm = 'all'; // trendRealm：当前趋势数据属于哪个域（切域要重拉）
let trendRows = []; // 趋势卡当前画的数据（最近 7 天），供悬停取值

// dayLabel "2026-09-11" → "09/11"。
function dayLabel(date) {
  return String(date || '').slice(5).replace('-', '/');
}

// trendChart 趋势卡：柱 = 用量（左轴），紫虚线 = 请求数（右轴）。
function trendChart(days) {
  if (!days || !days.length) return '<div class="viz-empty" style="padding:24px 0">还没有趋势数据。</div>';
  const W = 760, H = 150, L = 56, R = 48, T = 14, B = 28;
  const iw = W - L - R, ih = H - T - B;
  const n = days.length;
  const maxTok = niceMax(Math.max(1, ...days.map(d => d.total || 0)));
  const maxReq = niceMax(Math.max(1, ...days.map(d => d.requests || 0)));
  const yReq = v => T + ih - (v / maxReq) * ih;
  const step = iw / n;
  const bw = Math.min(46, Math.max(8, step * 0.42));

  let grid = '';
  for (let i = 0; i <= 4; i++) {
    const y = (T + (ih / 4) * i).toFixed(1);
    grid += '<line class="ch-grid" x1="' + L + '" y1="' + y + '" x2="' + (L + iw) + '" y2="' + y + '"/>' +
      '<text class="ch-axis" x="' + (L - 8) + '" y="' + (+y + 4) + '" text-anchor="end">' +
        fmtTok(Math.round(maxTok * (1 - i / 4))) + '</text>' +
      '<text class="ch-axis" x="' + (L + iw + 8) + '" y="' + (+y + 4) + '" text-anchor="start">' +
        fmtInt(Math.round(maxReq * (1 - i / 4))) + '</text>';
  }

  let bars = '', pts = [], dots = '';
  days.forEach((d, i) => {
    const cx = L + step * i + step / 2;
    const has = (d.total || 0) > 0;
    const h = has ? Math.max(3, (d.total / maxTok) * ih) : 2;
    bars += '<rect class="ch-bar' + (has ? '' : ' ch-bar-zero') + '" x="' + (cx - bw / 2).toFixed(1) +
      '" y="' + (T + ih - h).toFixed(1) + '" width="' + bw.toFixed(1) + '" height="' + h.toFixed(1) + '" rx="3"/>';
    const ry = yReq(d.requests || 0);
    pts.push(cx.toFixed(1) + ',' + ry.toFixed(1));
    if ((d.requests || 0) > 0) {
      dots += '<circle class="ch-dot2" cx="' + cx.toFixed(1) + '" cy="' + ry.toFixed(1) + '" r="2.6"/>';
    }
  });

  const xs = days.map((d, i) =>
    '<text class="ch-axis" x="' + (L + step * i + step / 2).toFixed(1) + '" y="' + (T + ih + 17) +
      '" text-anchor="middle">' + esc(dayLabel(d.date)) + '</text>').join('');

  return '<svg viewBox="0 0 ' + W + ' ' + H + '" role="img" aria-label="近 7 天用量与请求数">' +
    grid + bars + '<polyline class="ch-line2" points="' + pts.join(' ') + '"/>' + dots + xs + '</svg>';
}

function renderTrendView() {
  if (!trendData) return;
  const days = (trendData.days || []).slice(-7); // 趋势卡固定最近 7 天；流水卡用全量 14 天
  trendRows = days;
  $('dashTrend').innerHTML = trendChart(days);
  wireTrendHover();
  const tok = days.reduce((a, d) => a + (d.total || 0), 0);
  const req = days.reduce((a, d) => a + (d.requests || 0), 0);
  const fail = days.reduce((a, d) => a + (d.failed || 0), 0);
  $('dashTrendTok').textContent = '用量 ' + fmtTok(tok) + ' token';
  $('dashTrendReq').textContent = '请求 ' + fmtInt(req) + ' 次';
  // 副标题日期范围必须与图上切片一致（slice(-7)），不能用 trendData.since/until
  // ——那是 14 天全量窗口，会写出「09/04 ~ 09/17」配一张只有 7 天的图。
  const range = days.length && days[0].date && days[days.length - 1].date
    ? dayLabel(days[0].date) + ' ~ ' + dayLabel(days[days.length - 1].date) : '';
  $('dashTrendNote').textContent =
    range + (fail ? ' · 失败 ' + fmtInt(fail) : '');
  renderCreditBurn(); // 流水卡与趋势同源，一并刷新
}

// loadTrend 拉取趋势。force = 跳过 60 秒节流（顶栏「刷新」用；切域时也走 force，
// 否则会被 60 秒节流挡住，看起来像"点了开关数字不动"）。
function loadTrend(force) {
  const box = $('dashTrend');
  if (!box) return Promise.resolve();
  // 域视图：趋势也按域聚合（与请求审计同一份流水、同一口径）。缓存键含域，跨域切会重新拉。
  const realmQ = panelRealm === 'all' ? '' : ('&realm=' + panelRealm);
  if (!force && trendData && trendAt && Date.now() - trendAt < 60000 && trendRealm === panelRealm) {
    renderTrendView();
    return Promise.resolve();
  }
  return api('trend?days=14' + realmQ).then(d => {
    trendData = d.trend || null;
    trendAt = Date.now();
    trendRealm = panelRealm;
    renderTrendView();
  }).catch(() => {
    // 失败只在「图上还什么都没有」时提示：轮询场景下静默保留旧图。
    // children 探活要容忍假 DOM（测试桩的元素没有 children 属性）。
    if (!(box.children && box.children.length)) {
      box.innerHTML = '<div class="viz-empty" style="padding:28px 0">趋势读取失败，稍后自动重试。</div>';
    }
  });
}

function trendHitIndex(ratio, n) {
  const W = 760, L = 56, R = 48;
  if (!(ratio >= 0) || ratio > 1 || !n || n < 1) return -1;
  const x = ratio * W;
  if (x < L || x > W - R) return -1;
  let i = Math.floor(((x - L) / (W - L - R)) * n);
  if (i >= n) i = n - 1;
  return i >= 0 ? i : -1;
}

function wireTrendHover() {
  const wrap = $('trendWrap'), host = $('dashTrend'), tip = $('trendTip'), cur = $('trendCursor');
  if (!wrap || !host || !tip || !cur || !host.querySelector) return;
  const svg = host.querySelector('svg');
  if (!svg || !svg.getBoundingClientRect) return;
  const hide = () => { tip.hidden = true; cur.hidden = true; };
  svg.onmouseleave = hide;
  svg.onmousemove = rafMove(ev => {
    const sr = svg.getBoundingClientRect(), wr = wrap.getBoundingClientRect();
    if (!sr.width || !trendRows.length) { hide(); return; }
    const i = trendHitIndex((ev.clientX - sr.left) / sr.width, trendRows.length);
    if (i < 0) { hide(); return; }
    const d = trendRows[i];
    tip.innerHTML =
      '<div class="tt-h">' + esc(d.date || '') + '</div>' +
      '<div class="tt-r"><i class="ch-sw"></i><span>用量</span><b>' + fmtInt(d.total || 0) + '</b></div>' +
      '<div class="tt-r"><i class="ch-sw ch-sw-ln2"></i><span>请求</span><b>' + fmtInt(d.requests || 0) + '</b></div>' +
      ((d.failed || 0) > 0
        ? '<div class="tt-r"><i class="ch-sw" style="background:var(--bad);opacity:.85"></i><span>失败</span><b>' + fmtInt(d.failed) + '</b></div>'
        : '');
    tip.hidden = false;
    cur.hidden = false;
    const W = 760, L = 56, R = 48, T = 14, H = 150, B = 28;
    cur.style.left = ((sr.left - wr.left) +
      ((L + ((W - L - R) / trendRows.length) * (i + 0.5)) / W) * sr.width).toFixed(1) + 'px';
    cur.style.top = ((sr.top - wr.top) + (T / H) * sr.height).toFixed(1) + 'px';
    cur.style.height = (((H - T - B) / H) * sr.height).toFixed(1) + 'px';
    const tw = tip.offsetWidth || 150;
    const tx = Math.max(tw / 2 + 4, Math.min(wr.width - tw / 2 - 4, ev.clientX - wr.left));
    tip.style.left = tx.toFixed(1) + 'px';
    tip.style.top = (ev.clientY - wr.top).toFixed(1) + 'px';
    tip.classList.toggle('below', ev.clientY - wr.top < 76);
  });
}

/* ── 近 14 天积分流水（消耗 vs 获取，柱 + 线）────────────────────────────
   消耗来自审计流水的逐日 credit 求和（实扣口径）；获取走另一条路——
   余额净变化 + 消耗：净变化覆盖签到/任务/抽奖等一切 /v1 之外的进账，
   加回被减掉的消耗就是当天真实的进账额。两条序列同轴，一眼读出
   「收支是否平衡」。视觉与余额走势同语言：消耗蓝面积、获取绿线条。 */
let _lastDailyNet = [];
// 流水图的悬停数据与几何（必须与 renderCreditBurn 里的常量一致）。
let flowRows = []; // { date, spent, earned }
const FL_W = 760, FL_H = 150, FL_L = 56, FL_R = 16, FL_T = 14, FL_B = 24;

// flowHitIndex 鼠标横向占比 → 第几天（越界 -1）。
function flowHitIndex(ratio, n) {
  if (!(ratio >= 0) || ratio > 1 || !n || n < 1) return -1;
  const x = ratio * FL_W;
  if (x < FL_L || x > FL_W - FL_R) return -1;
  let i = Math.floor(((x - FL_L) / (FL_W - FL_L - FL_R)) * n);
  if (i >= n) i = n - 1;
  return i >= 0 ? i : -1;
}

// wireFlowHover 光标竖线 + 浮层：显示当天的消耗 / 获取（与趋势图同一交互语言）。
function wireFlowHover() {
  const wrap = $('flowWrap'), host = $('dashCreditBurn'), tip = $('flowTip'), cur = $('flowCursor');
  if (!wrap || !host || !tip || !cur || !host.querySelector) return;
  const svg = host.querySelector('svg');
  if (!svg || !svg.getBoundingClientRect) return;
  const hide = () => { tip.hidden = true; cur.hidden = true; };
  svg.onmouseleave = hide;
  svg.onmousemove = rafMove(ev => {
    const sr = svg.getBoundingClientRect(), wr = wrap.getBoundingClientRect();
    if (!sr.width || !flowRows.length) { hide(); return; }
    const i = flowHitIndex((ev.clientX - sr.left) / sr.width, flowRows.length);
    if (i < 0) { hide(); return; }
    const d = flowRows[i];
    tip.innerHTML =
      '<div class="tt-h">' + esc(dayLabel(d.date)) + '</div>' +
      '<div class="tt-r"><i class="ch-sw-line-blue"></i><span>消耗</span><b>' + fmtCredit(d.spent) + '</b></div>' +
      '<div class="tt-r"><i class="ch-sw ch-sw-earn"></i><span>获取</span><b>' + fmtCredit(d.earned) + '</b></div>' +
      (d.earned - d.spent !== 0
        ? '<div class="tt-r"><i class="ch-sw" style="background:' + (d.earned >= d.spent ? 'var(--ok)' : 'var(--bad)') + ';opacity:.85"></i><span>净</span><b>' +
          (d.earned - d.spent > 0 ? '+' : '') + fmtCredit(d.earned - d.spent) + '</b></div>'
        : '');
    tip.hidden = false;
    cur.hidden = false;
    cur.style.left = ((sr.left - wr.left) +
      ((FL_L + ((FL_W - FL_L - FL_R) / flowRows.length) * (i + 0.5)) / FL_W) * sr.width).toFixed(1) + 'px';
    cur.style.top = ((sr.top - wr.top) + (FL_T / FL_H) * sr.height).toFixed(1) + 'px';
    cur.style.height = (((FL_H - FL_T - FL_B) / FL_H) * sr.height).toFixed(1) + 'px';
    const tw = tip.offsetWidth || 150;
    const tx = Math.max(tw / 2 + 4, Math.min(wr.width - tw / 2 - 4, ev.clientX - wr.left));
    tip.style.left = tx.toFixed(1) + 'px';
    tip.style.top = (ev.clientY - wr.top).toFixed(1) + 'px';
    tip.classList.toggle('below', ev.clientY - wr.top < 76);
  });
}

function renderCreditBurn() {
  const box = $('dashCreditBurn');
  if (!box) return;
  const sub = $('dashBurnSub');
  const days = (trendData && trendData.days) || [];
  if (!days.length) {
    if (sub) sub.textContent = '';
    box.innerHTML = '<div class="viz-empty" style="padding:22px 0">趋势数据加载中…</div>';
    return;
  }
  const spent = days.map(d => d.credit || 0);
  // dailyNet 是「到当日末的余额净变化」。拿它按日对齐：净变化 + 消耗 = 获取。
  // 没有对应净变化数据的日子（如进程刚重启、窗口还没铺满）获取按 0 画，不编数。
  const netByKey = {};
  let lastNet = null;
  (_lastDailyNet || []).forEach(d => {
    const k = new Date(d.at * 1000);
    netByKey[k.getFullYear() + '-' + String(k.getMonth() + 1).padStart(2, '0') + '-' + String(k.getDate()).padStart(2, '0')] = d.net;
    lastNet = d;
  });
  const earned = days.map((d, i) => {
    if (i === days.length - 1 && lastNet && !(d.date in netByKey)) {
      // 今天可能还没跨过任何一个采样点：用最新一天的 net 顶上，否则今天恒为 0
      return (lastNet.net || 0) + spent[i];
    }
    return (netByKey[d.date] || 0) + spent[i];
  });
  const max = Math.max(Math.max(...spent, ...earned), 0.0001);
  const totSpent = spent.reduce((a, b) => a + b, 0);
  const totEarned = earned.reduce((a, b) => a + b, 0);
  const negDays = earned.filter(v => v < 0).length;
  if (sub) sub.textContent =
    '消耗 ' + fmtCredit(totSpent) + ' · 获取 ' + fmtCredit(totEarned) + ' 积分' +
    (negDays ? ' · ' + negDays + ' 天净亏' : '');

  // 几何：双线对称。获取可能为负（净亏日），绘图时夹到 0——负值本身用
  // 右上角「N 天净亏」说明，画进坐标系会把多边形翻转到轴下方（第一版栽在这）。
  const W = FL_W, H = FL_H, L = FL_L, R = FL_R, T = FL_T, B = FL_B;
  const iw = W - L - R, ih = H - T - B, n = days.length;
  const nice = v => {
    if (!(v > 0)) return 1;
    const p = Math.pow(10, Math.floor(Math.log10(v)));
    const d = v / p;
    return (d <= 1 ? 1 : d <= 2 ? 2 : d <= 5 ? 5 : 10) * p;
  };
  const maxV = nice(max);
  const cx = i => (n === 1 ? L + iw / 2 : L + (iw / (n - 1)) * i);
  const Y = v => T + ih - (Math.max(0, v) / maxV) * ih;

  let grid = '';
  for (let i = 0; i <= 3; i++) {
    const y = (T + (ih / 3) * i).toFixed(1);
    grid += '<line class="ch-grid" x1="' + L + '" y1="' + y + '" x2="' + (L + iw) + '" y2="' + y + '"/>' +
      '<text class="ch-axis" x="' + (L - 8) + '" y="' + (+y + 4) + '" text-anchor="end">' +
        fmtInt(Math.round(maxV - maxV * (i / 3))) + '</text>';
  }
  // 两条序列同为「积分」、同一坐标轴——用**完全对称**的画法：同粗实线 +
  // 各自的采样圆点，谁也不特殊。此前消耗走渐变面积、获取走裸线，
  // 两种视觉语言同屏被用户指为违和；渐变面积在两线贴地时还会糊成一团。
  const lineOf = vals => vals.map((v, i) => (i ? 'L' : 'M') + cx(i).toFixed(1) + ' ' + Y(v).toFixed(1)).join(' ');
  const dotsOf = (vals, cls) => vals
    .map((v, i) => '<circle class="' + cls + '" cx="' + cx(i).toFixed(1) + '" cy="' + Y(v).toFixed(1) + '" r="2.6"/>')
    .join('');
  const xs = days.map((d, i) =>
    (i % 2 === 0 || i === n - 1)
      ? '<text class="ch-axis" x="' + cx(i).toFixed(1) + '" y="' + (T + ih + 17) + '" text-anchor="middle">' + esc(dayLabel(d.date)) + '</text>'
      : '').join('');
  flowRows = days.map((d, i) => ({ date: d.date, spent: spent[i], earned: earned[i] }));
  box.innerHTML = '<div class="chart"><svg viewBox="0 0 ' + W + ' ' + H + '" role="img" aria-label="近 14 天积分流水：消耗与获取">' +
    grid +
    '<path class="ch-earn" d="' + lineOf(earned) + '"/>' +
    '<path class="ch-spent" d="' + lineOf(spent) + '"/>' +
    dotsOf(earned, 'ch-dot3') + dotsOf(spent, 'ch-dot3-blue') + xs + '</svg></div>' +
    '<div class="chart-x"><span class="ch-lg"><i class="ch-sw-line-blue"></i><span>消耗 ' + fmtCredit(totSpent) + '</span></span>' +
    '<span class="ch-lg"><i class="ch-sw-earn"></i><span>获取 ' + fmtCredit(totEarned) + '</span></span></div>';
  wireFlowHover();
}

/* ── 按模型表（默认前三，其余折叠）───────────────────────────────── */
let modelsExpanded = false;
let _lastModels = [];

function renderModelTable() {
  const models = _lastModels;
  // moreRow 折叠行：>3 个模型时追加在第 topN 行之后（点击走 flipModels）。
  const moreRow = models.length > 3
    ? '<tr class="more-row"><td colspan="13">' +
      '<button type="button" class="xs ghost" data-more>' +
      (modelsExpanded ? '收起 ▴' : '展开其余 ' + (models.length - 3) + ' 个模型 ▾') +
      '</button></td></tr>'
    : '';
  const topN = modelsExpanded ? models.length : Math.min(3, models.length);
  // 占比分母用全池合计，不用「相对最大模型」——后者在去掉进度条后失去存在理由，
  // 且与直觉冲突（最大者恒显 100.0%）。改成占总量后与「模型占比」donut 卡同口径，
  // 两处数字能直接对上；分母取 max(1, Σ) 防零请求时除 0。
  const totTok = models.reduce((a, m) => a + (m.total_tokens || 0), 0);
  const maxTok = Math.max(1, totTok);
  $('dashModelBody').innerHTML = models.slice(0, topN).map(m => {
    const share = pctOf(m.total_tokens, maxTok);
    const hit = pctOf(m.cached_tokens, m.prompt_tokens);
    // 每模型单价：上游 usage 按模型累计的 credits ÷ 该模型百万 token。
    // 无积分记录（上游没回 usage.credit）显示「—」，与别处口径一致。
    const rate = m.credits > 0 && m.total_tokens > 0 ? fmtRate(m.credits / (m.total_tokens / 1e6)) : '—';
    return '<tr>' +
      '<td class="who"><div class="nm">' + esc(m.key) + '</div></td>' +
      dashNum(m.requests) +
      '<td class="num">' + fmtTok(m.prompt_tokens) + '</td>' +
      '<td class="num">' + fmtTok(m.completion_tokens) + '</td>' +
      '<td class="num">' + fmtTok(m.total_tokens) + '</td>' +
      '<td class="num">' + share.toFixed(1) + '%</td>' +
      '<td class="num">' + fmtTok(m.cached_tokens) + '</td>' +
      '<td class="num">' + (m.prompt_tokens ? hit.toFixed(1) + '%' : '—') + '</td>' +
      '<td class="num">' + rate + '</td>' +
      '<td class="num">' + (m.requests ? fmtTok(Math.round(m.total_tokens / m.requests)) : '—') + '</td>' +
      '<td class="num">' + (m.tokens_per_sec ? m.tokens_per_sec.toFixed(1) : '—') + '</td>' +
      '<td class="num">' + fmtMs(m.avg_ttfb_ms) + '</td>' +
      dashNum(m.errors, m.errors ? 'var(--bad)' : '') +
      '</tr>';
  }).join('') + moreRow || dashEmpty(13, '还没有模型用量。发一次 /v1 请求后回来刷新。');
  // querySelector 探活：假 DOM（测试桩）没有这个方法，折叠切换走 flipModels 导出。
  const box = $('dashModelBody');
  if (box.querySelector) {
    const btn = box.querySelector('[data-more]');
    if (btn) btn.onclick = flipModels;
  }
}

// flipModels 折叠/展开模型表（按钮与测试共用同一入口）。
function flipModels() { modelsExpanded = !modelsExpanded; renderModelTable(); }

// renderExpiryChip 到期提醒（顶栏右端）。
//
// **为什么只显示一条**：实测到期分散在 4 个日期，全部列出会把顶栏撑高一大截，
// 而顶栏是每页都在的常驻区。所以这里只报"最快到期的那一条"，完整清单点开看。
//
// **但只报一条有踩过的坑**：最早那天可能金额很小（实测 10-11 只有 1,600，
// 而 10-13 有 35,500）。只看到 1,600 会让人以为风险不大，实际近期要走 40,060。
// 所以卡片上同时给出**合计**，并把"还有 N 笔"写出来——引导点开看全貌。
//
// 档位按最近一批剩余天数（用户指定）：>10 天绿、≤10 天黄、≤3 天红。
// 颜色之外还带文字标记（充裕/尽快用/快过期），灰度与色觉障碍下也能分辨。
let _expiryCache = { batches: [], total: 0, remain: 0 };

function renderExpiryChip(c, remain) {
  const el = $('dashExpiry');
  if (!el) return;
  const days = typeof c.expiring_days === 'number' ? c.expiring_days : -1;
  const batches = Array.isArray(c.expiring_batches) ? c.expiring_batches : [];
  if (days < 0 || !batches.length) {
    el.hidden = true;
    el.textContent = '';
    _expiryCache = { batches: [], total: 0, remain: 0 };
    return;
  }
  let total = 0;
  for (const b of batches) total += (b.remain || 0);
  if (total <= 0) {
    el.hidden = true;
    el.textContent = '';
    _expiryCache = { batches: [], total: 0, remain: 0 };
    return;
  }
  const first = batches[0];
  const tier = days <= 3 ? 'bad' : days <= 10 ? 'warn' : 'ok';
  const tag = days <= 3 ? '快过期' : days <= 10 ? '尽快用' : '充裕';
  const when = days === 0 ? '今天' : days + ' 天后';

  el.className = 'expiry-chip ' + tier;
  el.hidden = false;
  el.title = '点击查看全部 ' + batches.length + ' 个到期日';
  el.innerHTML =
    '<span class="ex-h">' + fmtInt(first.remain || 0) + ' 积分 ' + when + '到期 · ' + tag + '</span>' +
    '<span class="ex-n">共 ' + fmtInt(total) + ' 待到期</span>' +
    '<span class="ex-more">' +
      (batches.length > 1 ? '等 ' + batches.length + ' 笔' : '明细') + '</span>';

  _expiryCache = { batches, total, remain };
}

// openExpiryDetail 打开到期明细弹窗。数据取自上一次 renderExpiryChip 的缓存，
// 不再请求接口——避免"点了弹窗但数字和卡片对不上"。
function openExpiryDetail() {
  const { batches, total, remain } = _expiryCache;
  if (!batches.length) return;
  const pctAll = remain > 0 ? Math.round(total / remain * 100) : 0;
  $('expSummary').textContent =
    '共 ' + batches.length + ' 个到期日，合计 ' + fmtInt(total) + ' 积分' +
    '（占当前余额 ' + pctAll + '%）。';
  $('expBody').innerHTML = batches.map(b => {
    const d = typeof b.days === 'number' ? b.days : 0;
    const left = d === 0 ? '今天' : d + ' 天';
    const pct = remain > 0 ? Math.round((b.remain || 0) / remain * 100) : 0;
    // 最近的 3 天内用红字标出，和卡片档位呼应。
    const cls = d <= 3 ? ' style="color:var(--bad);font-weight:600"' : '';
    return '<tr>' +
      '<td>' + esc(b.at || '') + '</td>' +
      '<td class="num"' + cls + '>' + left + '</td>' +
      '<td class="num">' + fmtInt(b.remain || 0) + '</td>' +
      '<td class="num">' + pct + '%</td>' +
    '</tr>';
  }).join('');
  $('expVeil').classList.add('on');
  setTimeout(() => $('btnExpClose').focus(), 60);
}

function _closeExpiryDetail() { $('expVeil').classList.remove('on'); }

$('dashExpiry').onclick = openExpiryDetail;
$('btnExpClose').onclick = _closeExpiryDetail;
$('expVeil').addEventListener('click', ev => {
  if (ev.target === $('expVeil')) _closeExpiryDetail();
});
document.addEventListener('keydown', ev => {
  if (ev.key === 'Escape' && $('expVeil').classList.contains('on')) _closeExpiryDetail();
});
// Esc 收掉内容型弹窗（任务 / 添加账号）。密钥门（keyVeil）不在列：
// 它是鉴权闸门，不能被「随手一按」关掉。
document.addEventListener('keydown', ev => {
  if (ev.key !== 'Escape') return;
  if ($('taskVeil').classList.contains('on')) closeTasks();
  else if ($('addVeil').classList.contains('on')) closeAdd();
});

// renderCreditViz 积分概览卡。
//
// 回答三件事：
//   1. 存量——剩余 / 本周期已耗 + 额度条（两段占比）
//   2. 还能撑多久——**按余额自身的净变化速率**外推（不是按 token 倒推）
//   3. 账号数建议——再加几个号签到进账能盖住消耗
//
// **为什么额度条要标「本周期口径」**：两段是「本周期已耗 / 剩余」。它**不是硬配额**——
// 积分随签到增长，所以已耗占比会随时间变小、永远到不了 100%。它回答的是"这一周期流过去了多少"，
// 不是"还剩百分之几额度"。两段用不同纹理（实心/斜纹）+ 不同色，不靠单一颜色区分。
//
// **为什么续航必须用「余额净变化」而不是「token 速率」**（这条踩过坑，记在这）：
// 积分不是消耗品配额，它会**进账**——签到/任务每天发。用 token 速率倒推是看不见进账的：
// 今天耗 100、明天签到领 1000，token 法会算出"正在快速烧光"，真相却是余额在涨。
// 后端按 5 分钟采样余额、取窗口首尾差 ÷ 真实时间跨度，得到净速率；
// 余额涨了速率就是负的，续航自然显示为"净增长"。
//
// 这张卡只讲积分：24 小时 token 柱已挪去「消耗节奏」卡，一张卡不混两种口径。
function renderCreditViz(c) {
  const box = $('dashCreditViz');
  if (!box) return;
  if ($('dashCreditAge')) {
    $('dashCreditAge').textContent = c.age_sec >= 0 ? agoSec(c.age_sec) + '刷新' : '';
  }
  const remain = Math.max(0, c.remain || 0);
  const used = Math.max(0, c.used || 0);
  if (remain + used <= 0) {
    box.innerHTML = '<div class="viz-empty">还没有积分数据。等一次余额刷新。</div>';
    return;
  }

  const burn = typeof c.burn_per_hour === 'number' ? c.burn_per_hour : 0;  // 净消耗 积分/小时
  const runway = c.runway_hours > 0 ? c.runway_hours : 0;
  const spanSec = c.burn_span_sec || 0;
  const hasBurn = spanSec > 0;          // 样本够久，能出速率
  const gaining = hasBurn && burn <= 0; // 净增长：签到领得比用得多

  // 能签到进账的账号数：禁用号不参与签到排程，所以只数未禁用的。
  const nEarn = ((overviewData && overviewData.accounts) || []).filter(a => !a.disabled).length;

  // 额度条：本周期「已耗 / 剩余」两段占比。
  const total = remain + used;
  const usedPct = total > 0 ? used / total * 100 : 0;
  const freePct = 100 - usedPct;

  // 「剩余可用」的三种状态：净增长不报倒计时；有速率才给天数；样本不够就明说。
  // 数值与单位分开存：单位要用小一号字体渲染（见 .mtx .v .unit）。
  let lifeVal = '—', lifeUnit = '', lifeCls = '', lifeNote = '';
  if (gaining) {
    lifeVal = '净增长'; lifeCls = 'good'; lifeNote = '签到进账大于消耗';
  } else if (runway > 0) {
    if (runway >= 48) { lifeVal = (runway / 24).toFixed(1); lifeUnit = ' 天'; }
    else if (runway >= 1) { lifeVal = runway.toFixed(1); lifeUnit = ' 小时'; }
    else { lifeVal = String(Math.round(runway * 60)); lifeUnit = ' 分钟'; }
    lifeCls = runway < 24 ? 'bad' : runway < 72 ? 'warn' : 'ok';
    lifeNote = '按当前净消耗速率';
  } else {
    lifeNote = '才采到 1 个点，下次刷新出数';
  }

  // 窗口长度如实说明，并在不满一天时**明确标注「不足一天」**。
  //
  // 为什么必须标：速率按「窗口首尾差 ÷ 跨度」算。窗口短于 24 小时时，里面可能只含
  // 0 次或 1 次签到脉冲（签到每天给全池灌约 1,100 积分），所以这个日速率是**方向性参考**，
  // 不等于稳态日消耗。不标的话用户会把它当成精确值——实测窗口只有 7.7 小时，差得很远。
  const fullDay = spanSec >= 86400;
  const spanTxt = spanSec >= 3600 ? (spanSec / 3600).toFixed(1) + ' 小时'
    : spanSec >= 60 ? Math.round(spanSec / 60) + ' 分钟'
    : spanSec + ' 秒';
  const sinceTxt = hasBurn ? (fullDay ? '实测 ' + (spanSec / 86400).toFixed(1) + ' 天' : '样本 ' + spanTxt)
    : '';

  // 卡片直接从额度条开始：剩余/已耗的**绝对值**在下面的 bar-x 与首行英雄卡
  // 已经有了，这里再放大字就是同一屏第三次出现——重复元素是从这里长出来的。
  box.innerHTML =
    '<div class="burn">' +
      '<i class="used" style="width:' + usedPct.toFixed(2) + '%"></i>' +
      '<i class="free" style="width:' + freePct.toFixed(2) + '%"></i>' +
    '</div>' +
    '<div class="bar-x">' +
      '<span>已耗 <b>' + fmtInt(used) + '</b> <span class="pct">' + usedPct.toFixed(1) + '%</span></span>' +
      '<span>剩余 <b>' + fmtInt(remain) + '</b> <span class="pct">' + freePct.toFixed(1) + '%</span></span>' +
    '</div>' +
    '<div class="mtx" style="margin:16px 0 8px">' +
      '<div class="it"><span class="lb">净消耗速率</span><span class="v sm">' +
        (hasBurn ? (burn > 0 ? '−' : '+') + fmtInt(Math.round(Math.abs(burn) * 24)) +
          '<span class="unit"> 积分/天</span>' : '—') + '</span>' +
        '<span class="note">' + (sinceTxt || '待累积样本') +
          (hasBurn && !fullDay ? ' <span class="warn-note">· 不足一天</span>' : '') + '</span></div>' +
      '<div class="it"><span class="lb">剩余可用</span><span class="v sm ' + lifeCls + '">' +
        lifeVal + (lifeUnit ? '<span class="unit">' + lifeUnit + '</span>' : '') + '</span>' +
        '<span class="note">' + lifeNote + '</span></div>' +
    '</div>' +
    loopBlock(burn, hasBurn, nEarn, fullDay);
}

// loopBlock 「无限循环」建议：再加几个号，签到进账才够覆盖消耗。
//
// 推导（设现有 N 个号、每号日进账 I、当前净消耗 B 积分/时；B 已含现有号的进账）：
//
//	毛消耗 = B×24 + N×I
//	平衡时 N'×I = 毛消耗  →  N' = N + B×24 ÷ I
//
// 所以「还要加的号数 = 每日净消耗 ÷ 每号每日签到进账」。
//
// **口径（必须标注）**：I 取 pool.DefaultCheckinCredit = 100。
// 上游 billing 不返回签到数额，这是个「惯例假设」不是实测——真实进账可能不同，
// 所以结论只能当方向性参考。B 是实测量出来的，那部分可信。
//
// fullDay=false 表示 B 的样本不足 24 小时：那时 B 连签到脉冲都还没平均掉，
// 结论比平时更粗，气泡里必须说清，否则用户会拿它当准数。
function loopBlock(burn, hasBurn, nEarn, fullDay) {
  if (!hasBurn) return '';
  const PER_DAY = 100; // 与 pool.DefaultCheckinCredit 对齐
  // 口径说明挂在问号气泡里，不占版面——它是"需要时才看"的内容，常驻会变噪音。
  const tip = '按当前净消耗速率估算，让签到进账刚好盖住消耗所需的账号数。\n\n' +
    '算法：日净消耗 ÷ 每号每日签到进账（按 ' + PER_DAY + ' 积分估算）+ 现有号数。\n\n' +
    '注意：消耗速率是实测的；但每号每日签到进账上游不返回真实数额，' + PER_DAY +
    ' 是惯例估值，所以结论只能当方向参考。' +
    (fullDay ? '' : '\n\n另外：当前速率样本不足一天，签到脉冲还没被平均掉，这个建议比满一天时更粗。');
  const q = '<span class="q" data-tip="' + esc(tip) + '">?</span>';
  // 问号跟在标签右边（不是左边）：先读到"账号数建议"这几个字，
  // 有疑问时视线自然往右找解释，符合从左到右的阅读顺序。
  const lb = '<span class="loop-lb">账号数建议' + q + '</span>';

  if (burn <= 0) {
    return '<div class="loop ok">' +
      '<div class="loop-h">' + lb + '<span style="flex:1"></span>' +
        '<b>已够 · 无限循环</b></div>' +
    '</div>';
  }
  const need = nEarn + Math.ceil(burn * 24 / PER_DAY);
  return '<div class="loop">' +
    '<div class="loop-h">' + lb + '<span style="flex:1"></span>' +
      '<b>' + need + ' 个</b></div>' +
    '<div class="loop-note">现在 ' + nEarn + ' 个，再补 ' + (need - nEarn) + ' 个即可收支平衡</div>' +
  '</div>';
}

// hourLabel Unix 小时 → 「14时」/「昨天23时」。跨天时带上日期，避免看不出是昨天的柱。
function hourLabel(h) {
  const d = new Date(h * 3600000);
  const now = new Date();
  const day = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const diff = Math.round((today - day) / 86400000);
  const hm = String(d.getHours()).padStart(2, '0') + ':00';
  if (diff === 0) return hm;
  if (diff === 1) return '昨天 ' + hm;
  return (d.getMonth() + 1) + '/' + d.getDate() + ' ' + hm;
}

function accessBase() {
  return (location.origin || (location.protocol + '//' + location.host)) + '/v1';
}
function fillAccess() {
  if ($('dashBase')) $('dashBase').textContent = accessBase();
}
function copyText(text, okMsg) {
  if (!text) { toast('没有可复制的内容', 'err'); return; }
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(() => toast(okMsg, 'ok'), () => toast('复制失败', 'err'));
  } else toast('当前环境不支持复制', 'err');
}
$('btnCopyBase').onclick = () => copyText(accessBase(), '接口地址已复制');
$('btnCopyKey').onclick = () => {
  const k = localStorage.getItem(LS_KEY);
  if (!k) { toast('还没保存密钥：先在登录框输入一次', 'err'); return; }
  copyText(k, '密钥已复制');
};

// dashRaw 最近一次 /panel/api/stats 的原始 payload。切域时在客户端重新切片即可
//（payload 里本来就带 by_realm），不必重查——这是仪表盘切域「秒开」的关键。
let dashRaw = null;

// dashStats 按当前域开关把原始 payload 切成「该看的那份」：域视图下把 Totals/Models/
// Keys/Buckets 整块替换成该域的分域切片（同形，渲染代码一行不用改）。缺失（后端未升级/
// 该域还没有分域数据）则回落全量，并在提示里说明——不给假数据。
function dashStats() {
  const s = dashRaw || {};
  const rv = (panelRealm !== 'all' && s.by_realm) ? s.by_realm[panelRealm] : null;
  const out = rv ? Object.assign({}, s, {
    totals: rv.totals, models: rv.models, keys: rv.keys, buckets: rv.buckets,
    // 积分口径：余额/消耗/实测比率/净速率/到期批次都换成该域的
    //（算法与池级共用 creditsViewOf，只是范围限定在该域账号上）。
    credits: rv.credits || s.credits,
  }) : s;
  dashRealmNote(rv, out);
  return out;
}

// dashSig 仪表盘的数据签名：影响渲染的全部字段。age_sec 按 60s 取整——标签本来就显示
//「X 分钟前」，秒级变化不值得一次整页重画；uptime 不进签名（运行时长在顶栏，由 applyStatus 刷）。
function dashSig(s) {
  const c = s.credits || {};
  const acct = ((overviewData && overviewData.accounts) || [])
    .map(a => a.uid + ':' + (a.credits || 0) + ':' + (a.disabled ? 1 : 0)).join(',');
  return JSON.stringify([panelRealm, s.totals, s.models, s.keys, s.buckets,
    { r: c.remain, u: c.used, sp: c.samples, pk: c.per_mtoken, et: c.est_tokens,
      bph: c.burn_per_hour, span: c.burn_span_sec, net: c.burn_net,
      eb: c.expiring_batches, ed: c.expiring_days,
      am: c.age_sec == null ? -1 : Math.floor(c.age_sec / 60) },
    acct]);
}

// renderDashFromRaw 用手里已有的原始 payload 重画仪表盘（不发请求）。
function renderDashFromRaw() {
  if (!dashRaw) return false;
  const s = dashStats();
  renderIfChanged('dashboard', dashSig(s), () => renderDashboard(s));
  return true;
}

async function loadStats() {
  fillAccess();
  if (!overviewData) {
    try { applyStatus(await api('overview')); } catch (e) { /* 密钥门会另开 */ }
  }
  try {
    const d = await api('stats');
    // 密钥名跟着 stats 一起来（指纹 → 名字），渲染前先落地。
    keyNames = d.key_names || {};
    dashRaw = d.stats || {};
    renderDashFromRaw();
    $('dashNote').textContent = '累计用量 · 重启保留 · ' + clock(Date.now()) + ' 刷新';
  } catch (e) {
    $('dashNote').textContent = '读取失败：' + e.message;
  }
}

/* ── 视图渲染的公共设施 ─────────────────────────────────────────────
   面板 10 个视图的数据都是整块 innerHTML 重建。三条纪律，都是为了让「切换」不再卡：
   1) 数据没变就不重绘（签名相同 → 跳过）——5 秒轮询每 tick 把表重建一遍是最大的浪费；
   2) 不可见的视图不重绘（数据照常落内存，进页面时再画）——切页时不做白工的 DOM；
   3) 进页面先用手里的数据画一遍，再后台刷新（stale-while-revalidate）——
      动画才有内容可演，不会「先空/先旧、数据到了再跳变」。 */
const viewSig = {};     // 视图 → 上次渲染用的数据签名
const viewDirty = {};   // 视图 → 内存里有比 DOM 更新的数据，等它被看见时再画

// renderIfChanged 签名变了才重绘。sig 传 '' 表示强制重绘。返回是否真的画了。
function renderIfChanged(v, sig, render) {
  if (sig && viewSig[v] === sig) return false;
  viewSig[v] = sig || '';
  viewDirty[v] = false;
  render();
  return true;
}

// markDirty 数据变了但这个视图不可见：只记一笔，等进页面时再画。
function markDirty(v, sig) { if (viewSig[v] !== sig) viewDirty[v] = true; }

// paintNow 用当前内存里的数据把这个视图画出来（不判断脏、不发请求）。
// 返回 false = 手里还没有能画的数据（调用方照旧显示加载态/空态）。
function paintNow(v) {
  if (v === 'accounts') { if (!overviewData) return false; renderAccounts(overviewData.accounts || []); return true; }
  if (v === 'checkin') { if (!overviewData) return false; renderCheckin(overviewData.accounts || []); return true; }
  if (v === 'dashboard') return renderDashFromRaw();
  // 用量/密钥是**服务端按域过滤**的：手里的数据属于别的域时不能拿来充数（会显示错域的数字）。
  if (v === 'usage') { if (!useData || useDataRealm !== panelRealm) return false; renderUsage(); return true; }
  if (v === 'keys') { if (!keysData || keysRealm !== panelRealm) return false; renderKeys(); return true; }
  // 积分构成的数据不分域（服务端没有 realm 参数），手里有就能直接画。
  if (v === 'packages') { if (!pkData) return false; renderPackages(pkData); return true; }
  if (v === 'models') { if (!mdCache.length) return false; renderModels(); return true; }
  if (v === 'logs') { if (!logLines.length) return false; paintLogs(); return true; }
  // 任务中心不在这里：它进页面时走 loadSchoolStatus(true)（quiet，不清空），旧矩阵会一直
  // 显示到新数据到达——本身就是缓存优先，不需要额外补一层。
  // 设置页的表单 DOM 一直在（字段是原地改值，不是整块重建），有配置就算「能画」。
  if (v === 'config') return !!cfgLoaded;
  return false;
}

// paintView 进页面时调用：只有「内存里的数据比 DOM 新」才重画。
// 注意不强制重画——DOM 里本来就是上次那份数据的渲染结果，重画只是白烧 CPU 且会让
// 刚播的入场动画从头再来一次。
function paintView(v) {
  if (!viewDirty[v]) return false;
  if (!paintNow(v)) return false;
  viewDirty[v] = false;
  return true;
}

// realmLabel 域开关的显示名（提示文案里用）。
function realmLabel() { return panelRealm === 'global' ? '国际服' : panelRealm === 'cn' ? '国服' : '全部'; }

/* ── 路由 ─────────────────────────────────────────────────────────── */
const TITLES = { dashboard: '仪表盘', accounts: '账号', checkin: '任务管理', taskcenter: '任务中心', packages: '积分构成', models: '模型', keys: '密钥', config: '设置', logs: '运行日志', usage: '请求审计' };
function go(v) {
  view = v;
  document.querySelectorAll('.view').forEach(s => s.hidden = s.id !== 'view-' + v);
  document.querySelectorAll('.nav a').forEach(a => a.classList.toggle('on', a.dataset.view === v));
  $('ttl').textContent = TITLES[v];
  // 「添加账号」只出现在账号管理。签到页不管号，总览更不该出现。
  $('btnAdd').hidden = v !== 'accounts';
  // 先用手里的数据把这个视图画出来（有数据就秒开，入场动画也有内容可演），
  // 再触发下面的刷新——刷新回来若数据没变，签名守卫会跳过重画，画面不会跳。
  paintView(v);
  if (v === 'dashboard') loadStats();
  if (v === 'accounts' || v === 'checkin') loadOverview(true);
  // 进入页面时把跨页持久的域开关落到该页筛选上（否则"国际服模式"下进账号页会看到两域的号）
  if (v === 'accounts' || v === 'models') {
    if (panelRealm === 'all') {
      if (v === 'accounts') accFilter = 'all';
    } else if (v === 'accounts') {
      accFilter = panelRealm;
    } else {
      mdFilter = panelRealm;
    }
  }
  // 国内服专属页面：国际服模式下只留说明卡片，且**不发**那些只会打 CN 端点的请求
  if (v === 'checkin' && panelRealm !== 'global') fillCheckinSchedule();
  if (v === 'taskcenter' && panelRealm !== 'global') { loadSchoolStatus(true); pollQueueOnce(); }
  if (v === 'checkin' || v === 'taskcenter') applyCnOnlyViews();
  if (v === 'packages') loadPackages();
  // 用数据判断「要不要拉模型目录」，而不是用 DOM 里有没有行：DOM 是渲染结果，
  // 拿它当缓存标记会让「空目录」每次进来都重查一遍。
  if (v === 'models' && !mdCache.length) loadModels();
  if (v === 'config') loadConfig();
  if (v === 'logs') loadLogs();
  if (v === 'usage') loadUsage();
  if (v === 'keys') loadKeys();
  // 日志视图是全宽终端，顶栏跟着放宽，底边才和内容对齐（否则那条线比终端宽一截）。
  document.body.classList.toggle('logs-view', v === 'logs');
}
document.querySelectorAll('.nav a').forEach(a => a.onclick = e => { e.preventDefault(); go(a.dataset.view); history.replaceState(null, '', '#' + a.dataset.view); });

/* ── 账号池 ───────────────────────────────────────────────────────── */
// 死号相关操作只在**存在禁用账号时**才显示：平时不占按钮栏，
// 有号变灰了才带着数量冒出来（数量即按钮标签，一眼知道要处理几个）。
function updateDeadAccountButtons(list) {
  const n = (list || []).filter(s => s.disabled).length;
  const rv = $('btnReviveAll'), rm = $('btnRemoveDisabled');
  rv.hidden = n === 0;
  rm.hidden = n === 0;
  if (n > 0) {
    rv.textContent = '批量解冻 (' + n + ')';
    rm.textContent = '清理禁用号 (' + n + ')';
  }
}

/* ── 健康度 ─────────────────────────────────────────────────────────
   把池里散落的运行态信号（禁用/熔断/冷却/连续失败/连续 12153/历史错误率）
   压成一个 0-100 的分数。分数用来"排序和一眼分辨"，但绝不黑箱——每个扣分项
   都记进 reasons，挂在该单元格的 title 上，鼠标一放就知道为什么扣。
   权重依据：能否被选中（禁用/熔断）> 近期是否在连续出错 > 历史成功率。   */
function healthOf(s) {
  const rs = [];
  let score = 100;
  const bl = (new Date(s.breaker_until || 0) - Date.now()) / 1000;
  const cool = Math.max(s.cool_remaining_sec || 0, 0);

  if (s.disabled) {
    rs.push('已禁用（不参与选号）' + (s.disabled_reason ? '：' + s.disabled_reason : ''));
    return { score: 0, reasons: rs };
  }
  if (bl > 0) { score -= 45; rs.push('熔断中，剩余 ' + dur(bl)); }
  else if (cool > 0) {
    score -= 30;
    rs.push((s.cool_kind === 'hard_credit' ? '积分耗尽冷却' : '限流冷却') + '，剩余 ' + dur(cool));
  }
  const bf = s.breaker_fails || 0;
  if (bf > 0 && bl <= 0) { score -= Math.min(20, bf * 7); rs.push('连续失败 ' + bf + ' 次'); }
  if (s.retry_count) { score -= Math.min(15, s.retry_count * 5); rs.push('累计熔断 ' + s.retry_count + ' 次'); }
  const sd = s.session_dead_fails || 0;
  if (sd > 0) {
    const th = (overviewData && overviewData.session_dead_threshold) || 3;
    score -= Math.min(30, sd * 12);
    rs.push('连续 12153 会话失效 ' + sd + '/' + th + '（达阈值即禁用）');
  }
  if ((s.soft_streak || 0) >= 3) { score -= 10; rs.push('连续限流 ' + s.soft_streak + ' 次（退避中）'); }

  const ok = s.success_count || 0, err = s.err_total || 0;
  if (ok + err >= 5) {                       // 样本太小不算率，否则 1/1 就成了 50%
    const rate = err / (ok + err);
    if (rate > 0.05) {
      score -= Math.round(Math.min(25, rate * 60));
      rs.push('历史错误率 ' + Math.round(rate * 100) + '%（' + err + '/' + (ok + err) + '）');
    }
  }
  score = Math.max(0, Math.min(100, Math.round(score)));
  if (!rs.length) rs.push('无异常信号');
  return { score, reasons: rs };
}

let accFilter = 'all', accQuery = '', accSort = 'health', accList = [];

function accKind(s) {
  if (s.disabled) return 'off';
  const bl = (new Date(s.breaker_until || 0) - Date.now()) / 1000;
  const cool = Math.max(s.cool_remaining_sec || 0, bl > 0 ? bl : 0);
  return cool > 0 ? 'cool' : 'ok';
}

// realmOf 账号所属域（cn/global）。后端 realm 恒为 cn/global，字段缺失时按 cn 兜底
// （与后端 realmKey 口径一致：空 realm = 国服）。chip 筛选与状态列「国服/国际服」徽标同源。
function realmOf(s) { return s.realm === 'global' ? 'global' : 'cn'; }

function visibleAccounts(list) {
  const q = accQuery.trim().toLowerCase();
  let out = (list || []).filter(s => {
    // 全局域开关先过一道（跨页持久：刷新后进账号页仍只看该域）。
    if (panelRealm !== 'all' && realmOf(s) !== panelRealm) return false;
    // accFilter 一列承载两维：状态（all/ok/cool/off）或域（cn/global，同组互斥单选）。
    // 与开关是「与」关系：开关管域、chip 管状态。
    if (accFilter === 'cn' || accFilter === 'global') {
      if (realmOf(s) !== accFilter) return false;
    } else if (accFilter !== 'all' && accKind(s) !== accFilter) return false;
    if (!q) return true;
    return [s.alias, s.nickname, s.uid].join(' ').toLowerCase().includes(q);
  });
  if (accSort === 'credits') out.sort((a, b) => (b.credits || 0) - (a.credits || 0));
  else if (accSort === 'name') {
    out.sort((a, b) => (a.alias || a.nickname || a.uid || '').localeCompare(b.alias || b.nickname || b.uid || '', 'zh'));
  } else {
    // 健康分先各算一次：原来写成 healthOf(b).score - healthOf(a).score，比较器每次比较
    // 都要算两遍（n log n 次比较 → 2·n·log n 次调用，每次含 new Date 与字符串拼接）。
    // 号少时看不出来，号多了就是切页/轮询那一下的顿挫来源。
    const sc = new Map(out.map(s => [s, healthOf(s).score]));
    out.sort((a, b) => sc.get(b) - sc.get(a));
  }
  return out;
}

function renderAccChips(list) {
  const el = $('accChips');
  if (!el) return;
  const n = { all: list.length, ok: 0, cool: 0, off: 0, cn: 0, global: 0 };
  for (const s of list) {
    n[accKind(s)]++;
    n[realmOf(s)]++;          // 域计数与状态计数独立统计：chips 两维都展示
  }
  // 前四个是状态 chip，后两个是域 chip（cn/global）。visibleAccounts 里
  // accFilter==='cn'|'global' 走域过滤分支，与状态维互斥单选（同一条 accFilter 槽位）。
  el.innerHTML = [['all', '全部'], ['ok', '可用'], ['cool', '冷却'], ['off', '禁用'],
                  ['cn', '国服'], ['global', '国际服']].map(([k, l]) =>
    '<button type="button" class="chip' + (accFilter === k ? ' on' : '') + '" data-f="' + k + '">' +
    l + '<span class="n">' + n[k] + '</span></button>').join('');
}

// probeCell 账号表的「验活」列：一行结果 + 一行时间。
//
// 三种状态要能一眼分开：从未验活 / 上次通 / 上次失败（失败时把"连续几次"也带上，
// 因为距自动禁用还差几次才是真正要关心的事）。
function probeCell(s) {
  if (!s.probe_at) return '<span style="color:var(--ink-3)">未验活</span>';
  const n = s.probe_fails || 0;
  const th = (overviewData && overviewData.probe_threshold) || 3;
  if (s.probe_ok) {
    return '<span class="sdot ok"></span>通过<div class="mut">' + esc(ago(s.probe_at)) + '</div>';
  }
  // 失败：连续次数用红/黄分档——差得远用黄，快触发了用红。
  const cls = n + 1 >= th ? 'bad' : 'warn';
  return '<span class="sdot ' + cls + '"></span>失败 ' + n + '/' + th +
    '<div class="mut">' + esc(ago(s.probe_at)) + '</div>';
}

// probeTitle 验活列的悬停说明：失败原因、下一步该做什么。
function probeTitle(s) {
  if (!s.probe_at) return '还没验活过。可以在「设置 → 账号验活」里点「立即验活」跑一轮。';
  const when = new Date(s.probe_at).toLocaleString();
  if (s.probe_ok) return '最近一次验活：' + when + ' · 通过';
  const th = (overviewData && overviewData.probe_threshold) || 3;
  const n = s.probe_fails || 0;
  return '最近一次验活：' + when + ' · 失败\n' +
    '连续失败 ' + n + ' 次，再失败 ' + Math.max(0, th - n) + ' 次将自动禁用。\n' +
    (s.probe_err ? '原因：' + s.probe_err : '');
}

// accViewSig 账号表渲染所依赖的**一切**：数据 + 这一页自己的筛选/搜索/排序。
// 签名必须把筛选算进去——否则「切页时按域重设筛选」这种「数据没变、筛选变了」的情况
// 会被签名守卫跳过，表格就停在旧筛选上（看着像切了没反应）。
function accViewSig() { return acctSig(accList) + '|' + accFilter + '|' + accQuery + '|' + accSort; }
// ckViewSig 签到表的签名（同口径）。
function ckViewSig() { return acctSig(ckList) + '|' + ckFilter + '|' + ckQuery; }

function renderAccounts(list) {
  if (list) accList = list;
  // 记下「这次画的是哪份数据 + 哪组筛选」：所有调用点（轮询/切页/改筛选）都靠它去重，
  // 于是同一份内容只会被画一次。
  viewSig.accounts = accViewSig(); viewDirty.accounts = false;
  const tb = $('accBody');
  updateDeadAccountButtons(accList); // 空池时 n=0 → 两个按钮自动隐藏
  renderAccChips(accList);
  if (!accList.length) {
    if ($('accCount')) $('accCount').textContent = '';
    tb.innerHTML = '<tr><td colspan="10"><div class="empty"><div class="big">还没有账号</div>点击右上角「添加账号」，用浏览器登录一个 WorkBuddy 账号</div></td></tr>';
    return;
  }
  const vis = visibleAccounts(accList);
  if ($('accCount')) $('accCount').textContent = vis.length === accList.length ? accList.length + ' 个账号' : vis.length + ' / ' + accList.length;
  if (!vis.length) {
    tb.innerHTML = '<tr><td colspan="10"><div class="empty"><div class="big">没有符合条件的账号</div>换个筛选或清空搜索</div></td></tr>';
    return;
  }
  const maxCred = Math.max(1, ...accList.map(s => s.credits || 0));
  tb.innerHTML = vis.map(s => {
    const bl = (new Date(s.breaker_until || 0) - Date.now()) / 1000;
    const cool = Math.max(s.cool_remaining_sec || 0, bl > 0 ? bl : 0);
    let cls = '', tag;
    // 国服/国际服徽标：挂在状态标签后面，两种状态都看得见（禁用号也标注归属）。
    // 国服蓝（realm-tag 默认）、国际服紫（realm-tag global），与 index.html 配色对一致。
    const isGlobal = realmOf(s) === 'global';
    const realmTag = '<span class="realm-tag' + (isGlobal ? ' global' : '') + '">' + (isGlobal ? '国际服' : '国服') + '</span>';
    if (s.disabled) { cls = 'off'; tag = '<span class="tag bad">已禁用</span>' + realmTag; }
    else if (cool > 0) {
      cls = 'cool';
      const kind = bl > (s.cool_remaining_sec || 0) ? '熔断' : (s.cool_kind === 'hard_credit' ? '积分冷却' : '限流冷却');
      tag = '<span class="tag warn">' + kind + ' · ' + dur(cool) + '</span>' + realmTag;
    } else tag = '<span class="tag ok">可用</span>' + realmTag;
    // 正在服务：在途 > 0。整行加 busy 类（绿条 + 淡绿底），一眼看出现在是谁在跑。
    const busy = (s.in_flight || 0) > 0;
    if (busy) cls = (cls ? cls + ' ' : '') + 'busy';
    const note = s.reason ? '<div class="hint" style="font-size:11.5px;color:var(--ink-3);margin-top:3px">' + esc(s.reason) + '</div>' : '';
    const short = s.uid.length > 16 ? s.uid.slice(0, 16) + '…' : s.uid;
    // 显示名优先级：别名 > 原始昵称 > 未命名。设了别名时把原始昵称挂在第二行，
    // 免得用户改完名忘了这个号原本是哪个。
    const shown = s.alias || s.nickname || '';
    const nmHtml = shown ? esc(shown) : '<span style="color:var(--ink-3)">未命名</span>';
    const sub = (s.alias && s.nickname) ? short + ' · ' + s.nickname : short;
    const cred = s.credits == null ? '—' : s.credits;
    const frozen = s.disabled || cool > 0;
    const h = healthOf(s);
    const hc = h.score >= 80 ? 'ok' : h.score >= 50 ? 'warn' : 'bad';
    // 行上**不挂 title**：挂了之后鼠标停在行内任何地方都弹「uid: …」，
    // 而 uid 本来就在账号格第二行显示着——纯重复的噪音。需要看完整 uid 时
    // 悬停账号名那一格即可（那里有"点击修改显示名称"的提示，且 uid 就在下面）。
    return '<tr class="' + cls + '">' +
      '<td class="mark" aria-hidden="true"><i></i></td>' +
      '<td class="who"><div class="nm edit" data-a="alias" data-u="' + esc(s.uid) + '" title="点击修改显示名称">' + nmHtml + '</div><div class="id">' + esc(sub) + '</div></td>' +
      '<td>' + tag + note + '</td>' +
      '<td class="hp" title="' + esc(h.reasons.join('；')) + '"><span class="tag ' + hc + '">' + h.score + '</span>' +
        '<div class="bar"><i class="' + hc + '" style="width:' + h.score + '%"></i></div></td>' +
      '<td class="cred"><div class="n">' + cred + '</div><div class="bar"><i style="width:' + Math.round((s.credits || 0) / maxCred * 100) + '%"></i></div></td>' +
      '<td class="num">' + (s.success_count || 0) + ' <span style="color:var(--ink-3)">/</span> <span style="color:var(--bad)">' + (s.err_total || 0) + '</span></td>' +
      '<td class="num">' + (s.in_flight || 0) +
        (busy ? '<div class="mut" style="color:var(--ok)">使用中</div>'
              : '<div class="mut">' + esc(ago(s.last_used)) + '</div>') + '</td>' +
      '<td class="num" title="' + esc(probeTitle(s)) + '">' + probeCell(s) + '</td>' +
      '<td class="num" style="color:var(--ink-3)">' + ago(s.last_success) + '</td>' +
      '<td class="acts"><div class="btn-row">' +
        // 单号验活：真发一次极小请求，判定与定时验活同源（连败达阈值会照常自动禁用）。
        '<button class="xs ghost" data-a="probe" data-u="' + esc(s.uid) + '" title="真发一次极小请求，看这个号还能不能干活；与定时验活同一套判定（连续失败达阈值会自动禁用）">验活</button>' +
        // 国际服专属：补地区 → 注册激活 → 领 trial → 等入账 → 读余额。
        // 加号时会自动跑一遍，但结果不可见；号卡在 0 积分或激活失败时用这个按钮重跑并看逐步结果。
        (realmOf(s) === 'global'
          ? '<button class="xs" data-a="activate" data-u="' + esc(s.uid) + '" title="补地区 → 注册激活 → 领 trial 积分 → 刷新余额（国际服专有链路，可反复点）">激活</button>'
          : '') +
        (frozen ? '<button class="xs primary" data-a="revive" data-u="' + esc(s.uid) + '">解冻</button>'
                : '<button class="xs ghost" data-a="disable" data-u="' + esc(s.uid) + '">禁用</button>') +
        '<button class="xs ghost danger" data-a="remove" data-u="' + esc(s.uid) + '">移除</button>' +
      '</div></td></tr>';
  }).join('');
}

// applyStatus 刷新与「当前看哪个视图」无关的全局状态：侧边栏连接指示、顶栏元信息、
// 账号池计数卡，以及别名映射依赖的 overviewData。
//
// 为什么必须从 loadOverview 拆出来：这些字段原本搭 loadOverview 的顺风车每 5 秒更新，
// 而轮询（refreshVisible）只对「当前视图」发请求。默认落地页从账号池改成仪表盘后，
// 仪表盘只轮询 stats，侧边栏的「服务正常 / 无可用账号」就会在首屏刷一次之后永远冻住
// ——一个不再反映真实状态的状态指示器，比没有更危险。
function applyStatus(d) {
  overviewData = d;
  renderRealmSwitch(); // 开关上的账号计数随账号列表刷新（也就此时才知道分域号数）
  // 顶部计数按域开关取值：后端已给出分域计数（口径与 /status 一致），切到某域时这几个数字
  // 就是那一域的——比前端自己按账号列表重算可靠（判死/冷却的口径在池侧）。
  const rt = panelRealm === 'all' ? null : ((d.realm_totals || {})[panelRealm] || null);
  $('sTotal').textContent = rt ? rt.total : d.total;
  $('sHealthy').textContent = rt ? rt.healthy : d.healthy;
  $('sCooling').textContent = rt ? rt.cooling : d.cooling;
  $('sDisabled').textContent = rt ? rt.disabled : d.disabled;
  $('sCredits').textContent = (d.accounts || [])
    .filter(s => panelRealm === 'all' || realmOf(s) === panelRealm)
    .reduce((a, s) => a + (s.credits || 0), 0);
  $('sSticky').textContent = d.sticky_sessions;
  // 实时并发：各号在途数求和。顺带找出哪个号正在服务（可能在多个）。
  // 域开关生效时只统计该域的号（看某域时，顶部并发数也该是那一域的）。
  const accts = (d.accounts || []).filter(s => panelRealm === 'all' || realmOf(s) === panelRealm);
  const inflight = accts.reduce((a, s) => a + (s.in_flight || 0), 0);
  const nf = $('navInflight');
  if (nf) {
    nf.textContent = inflight;
    nf.className = 'sig-v' + (inflight > 0 ? ' hot' : '');
  }
  const who = $('navInflightWho');
  if (who) {
    const busy = accts.filter(s => (s.in_flight || 0) > 0);
    who.textContent = busy.length
      ? busy.map(s => uidName(s.uid) + (s.in_flight > 1 ? '×' + s.in_flight : '')).join('、')
      : '';
    who.title = busy.length ? busy.map(s => s.uid).join('\n') : '';
  }
  $('navSub').textContent = APP_SUB;   // 小字固定写「非官方控制台」，别再写版本号（见 APP_SUB 注释）
  $('navVer').textContent = 'v' + d.version;
  $('navRedis').textContent = d.redis_mode === 'upstash' ? 'Redis 镜像' : '本地内存';
  $('navState').textContent = d.healthy > 0 ? '服务正常' : (d.total ? '无可用账号' : '待添加账号');
  const p = $('navPulse');
  p.className = 'pulse' + (d.healthy > 0 ? '' : (d.total ? ' warn' : ' bad'));
  const up = Math.floor(d.uptime_sec);
  $('subMeta').textContent = '运行 ' + (up >= 86400 ? Math.floor(up / 86400) + ' 天 ' : '') + Math.floor(up % 86400 / 3600) + ' 时 ' + Math.floor(up % 3600 / 60) + ' 分';
}

// loadStatus 只刷新上面的全局状态，不重画账号表。
// 给仪表盘轮询用：那边看不到账号表，没必要每 5 秒拼一次用不上的 HTML。
async function loadStatus(quiet) {
  try { applyStatus(await api('overview')); }
  catch (e) { if (!quiet) toast(e.message, 'err'); }
}

// acctSig 账号列表的数据签名：只取影响渲染的字段（含签到页要用的 today_checked）。
// 用它把「数据没变」的轮询整轮跳掉——5 秒一次、每次上千个节点重建，是最大的浪费。
function acctSig(list) {
  return (list || []).map(a => [a.uid, a.credits || 0, a.disabled ? 1 : 0, a.in_flight || 0,
    a.success_count || 0, a.err_total || 0, a.probe_at || '', a.probe_ok ? 1 : 0,
    a.today_checked ? 1 : 0, a.cool_remaining_sec || 0, a.breaker_until || ''].join(':')).join(',');
}

async function loadOverview(quiet) {
  try {
    const d = await api('overview');
    applyStatus(d);
    $('accNote').textContent = d.in_flight_full ? d.in_flight_full + ' 个账号并发已满' : '';
    accList = d.accounts || [];
    ckList = accList;
    // 两张表共用这一份数据，但各自**只在被看见时**重画：切到账号页却顺手把隐藏的签到表
    // 也重建一遍是纯白工（两张表加起来上千个节点）。不可见时只记一笔，进页面时再画。
    if (view === 'accounts') renderIfChanged('accounts', accViewSig(), () => renderAccounts(accList));
    else markDirty('accounts', accViewSig());
    if (view === 'checkin') renderIfChanged('checkin', ckViewSig(), () => renderCheckin(ckList));
    else markDirty('checkin', ckViewSig());
  } catch (e) { if (!quiet) toast(e.message, 'err'); }
}

/* ── 任务管理（独立页：不管账号增删，只管签到、旅行、活跃、保活与官方任务）───── */
let ckFilter = 'all', ckQuery = '', ckList = [];

function ymd(iso) {
  if (!iso || String(iso).startsWith('0001-')) return '—';
  return String(iso).slice(0, 10);
}
function hoursLabel(arr) {
  if (!arr || !arr.length) return '—';
  return arr.map(h => String(h).padStart(2, '0') + ':00').join(' / ');
}
function accNameCell(s) {
  const short = (s.uid || '').length > 16 ? s.uid.slice(0, 16) + '…' : (s.uid || '');
  const shown = s.alias || s.nickname || '';
  const nm = shown ? esc(shown) : '<span style="color:var(--ink-3)">未命名</span>';
  const sub = (s.alias && s.nickname) ? short + ' · ' + s.nickname : short;
  return '<td class="who"><div class="nm">' + nm + '</div><div class="id">' + esc(sub) + '</div></td>';
}
function visibleCheckins(list) {
  const q = ckQuery.trim().toLowerCase();
  return (list || []).filter(s => {
    if (ckFilter === 'signed' && !s.today_checked) return false;
    if (ckFilter === 'pending' && s.today_checked) return false;
    if (!q) return true;
    return [s.alias, s.nickname, s.uid].join(' ').toLowerCase().includes(q);
  });
}
function renderCkChips(list) {
  const el = $('ckChips');
  if (!el) return;
  const n = { all: list.length, signed: 0, pending: 0 };
  for (const s of list) { if (s.today_checked) n.signed++; else n.pending++; }
  el.innerHTML = [['all', '全部'], ['signed', '已签'], ['pending', '未签']].map(([k, l]) =>
    '<button type="button" class="chip' + (ckFilter === k ? ' on' : '') + '" data-ck="' + k + '">' +
    l + '<span class="n">' + n[k] + '</span></button>').join('');
}
function renderCheckin(list) {
  if (list) ckList = list;
  viewSig.checkin = ckViewSig(); viewDirty.checkin = false;
  const tb = $('ckBody');
  if (!tb) return;
  const signed = ckList.filter(s => s.today_checked).length;
  const pending = ckList.length - signed;
  const todaySum = ckList.reduce((a, s) => a + (s.today_credit || 0), 0);
  const monthSum = ckList.reduce((a, s) => a + (s.month_credit || 0), 0);
  if ($('ckSigned')) $('ckSigned').textContent = signed + ' / ' + ckList.length;
  if ($('ckPending')) $('ckPending').textContent = pending;
  if ($('ckTodaySum')) $('ckTodaySum').textContent = todaySum ? '+' + todaySum : '0';
  if ($('ckMonthSum')) $('ckMonthSum').textContent = monthSum;
  renderCkChips(ckList);
  if (!ckList.length) {
    if ($('ckCount')) $('ckCount').textContent = '';
    tb.innerHTML = '<tr><td colspan="8"><div class="empty"><div class="big">还没有账号</div>先去「账号管理」添加</div></td></tr>';
    return;
  }
  const vis = visibleCheckins(ckList);
  if ($('ckCount')) $('ckCount').textContent = vis.length === ckList.length ? ckList.length + ' 个账号' : vis.length + ' / ' + ckList.length;
  if (!vis.length) {
    tb.innerHTML = '<tr><td colspan="8"><div class="empty"><div class="big">没有符合条件的账号</div>换个筛选或清空搜索</div></td></tr>';
    return;
  }
  tb.innerHTML = vis.map(s => {
    const tag = s.today_checked
      ? '<span class="tag ok">已签</span>'
      : '<span class="tag mute">未签</span>';
    const today = s.today_checked ? ('+' + (s.today_credit || 0)) : '—';
    return '<tr>' +
      accNameCell(s) +
      '<td><span class="tag mute">API</span></td>' +
      '<td>' + tag + '</td>' +
      '<td class="num">' + today + '</td>' +
      '<td class="num">' + (s.month_credit || 0) + '</td>' +
      '<td class="num">' + (s.credits == null ? '—' : s.credits) + '</td>' +
      '<td class="num" style="color:var(--ink-3)">' + ymd(s.last_checkin) + '</td>' +
      '<td class="acts"><div class="btn-row">' +
        '<button class="xs ghost" data-a="checkin" data-u="' + esc(s.uid) + '">签到</button>' +
        '<button class="xs ghost" data-a="tasks" data-u="' + esc(s.uid) + '">任务</button>' +
      '</div></td></tr>';
  }).join('');
}
async function fillCheckinSchedule() {
  try {
    const d = await api('config');
    cfgLoaded = d.config;
    const on = !!(cfgLoaded.schedule && cfgLoaded.schedule.checkin_enabled);
    const hours = (cfgLoaded.schedule && cfgLoaded.schedule.checkin_hours) || [];
    $('ckWhen').textContent = hoursLabel(hours) + (on ? '' : ' · 已关闭');
    $('btnCkOn').classList.toggle('primary', on);
    $('btnCkOff').classList.toggle('primary', !on);
  } catch (e) { /* 签到页没有配置也不挡表格 */ }
}
async function setCheckinEnabled(on) {
  try {
    await api('config', { method: 'POST', body: JSON.stringify({ schedule: { checkin_enabled: on } }) });
    toast(on ? '定时签到已启用' : '定时签到已关闭', 'ok');
    fillCheckinSchedule();
  } catch (e) { toast(e.message, 'err'); }
}

$('accChips').addEventListener('click', ev => {
  const b = ev.target.closest('[data-f]');
  if (!b) return;
  accFilter = b.dataset.f;
  // 域 chip 与全局开关同口径（它们是同一个维度的两种入口）；状态 chip（可用/冷却/禁用）
  // 不动域——它与开关是「与」关系（例如"国际服里可用的号"），不被开关覆盖。
  if (accFilter === 'cn' || accFilter === 'global' || accFilter === 'all') {
    setPanelRealm(accFilter === 'all' ? 'all' : accFilter, true);
  }
  renderAccChips(accList.length);
  renderAccounts();
});
$('accSearch').addEventListener('input', () => { accQuery = $('accSearch').value; renderAccounts(); });
$('accSort').addEventListener('change', () => { accSort = $('accSort').value; renderAccounts(); });

$('accBody').addEventListener('click', async ev => {
  const b = ev.target.closest('[data-a]');
  if (!b) return;
  const u = b.dataset.u, a = b.dataset.a;
  // 改名走弹窗，不走下面的「按钮禁用 + 刷新」流程
  if (a === 'alias') { openAlias(u); return; }
  if (a === 'remove') {
    const ok = await ask('会同时删除该账号的池状态和 auths/ 下的凭证文件。删除后需要重新登录才能再用。', { title: '移除账号？', ok: '移除', danger: true });
    if (!ok) return;
  }
  if (a === 'disable') {
    const ok = await ask('禁用后该账号不再参与选号，积分和签到记录都保留，随时可以解冻。', { title: '禁用账号？', ok: '禁用' });
    if (!ok) return;
  }
  b.disabled = true;
  try {
    if (a === 'revive') {
      await api('accounts/' + encodeURIComponent(u) + '/revive', { method: 'POST' });
      toast('已解冻', 'ok');
    } else if (a === 'probe') {
      // 验活要真跑一次推理（1-3 秒），按钮上直接给出进行中状态，免得以为没点到。
      b.textContent = '验活中…';
      const r = await api('accounts/' + encodeURIComponent(u) + '/probe', { method: 'POST' });
      toast(r.ok ? '验活通过：这个号能正常干活'
                 : ('验活失败：' + (r.error || '未知') + (r.auto_disabled ? '　已自动禁用（可在本行解冻）' : '')),
        r.ok ? 'ok' : 'err');
    } else if (a === 'activate') {
      // 逐步结果直接播报：卡在哪一步（补地区/激活/trial/读余额）一眼可见。
      const r = await api('accounts/' + encodeURIComponent(u) + '/activate', { method: 'POST' });
      const steps = (r.steps || []).map(x => x.name + '：' + (x.detail || (x.ok ? '完成' : '失败')));
      const tail = (r.credits == null ? '' : '　当前积分 ' + r.credits);
      toast('激活' + (r.ok ? '完成' : '未完成') + '　' + steps.join('；') + tail, r.ok ? 'ok' : 'err');
    } else if (a === 'disable') {
      await api('accounts/' + encodeURIComponent(u) + '/disable', { method: 'POST' });
      toast('已禁用', 'ok');
		} else if (a === 'remove') {
			await api('accounts/' + encodeURIComponent(u) + '/remove', { method: 'POST' });
			toast('已移除', 'ok');
    }
  } catch (e) { toast(e.message, 'err'); }
  finally { b.disabled = false; loadOverview(true); }
});

/* ── 账号显示名称（别名）─────────────────────────────────────────────
   别名存在 data/aliases.json，不写进 auths/ 凭据（那会被 token 刷新整份重写抹掉）。
   留空保存 = 清除别名，回落原始昵称。 */
let aliasUID = null;

function openAlias(uid) {
  aliasUID = uid;
  const acc = ((overviewData && overviewData.accounts) || []).find(x => x.uid === uid) || {};
  const orig = acc.nickname || '';
  $('aliasWho').textContent = '账号 ' + uid.slice(0, 16) +
    (orig ? '　原始昵称：' + orig : '　（该账号无原始昵称）');
  const inp = $('aliasInput');
  inp.value = acc.alias || '';
  $('aliasErr').hidden = true;
  $('aliasVeil').classList.add('on');
  setTimeout(() => { inp.focus(); inp.select(); }, 60);
}

function closeAlias() {
  $('aliasVeil').classList.remove('on');
  aliasUID = null;
}

async function saveAlias(val) {
  if (!aliasUID) return;
  const btn = $('btnAliasSave');
  btn.disabled = true;
  try {
    const r = await api('accounts/' + encodeURIComponent(aliasUID) + '/alias', {
      method: 'POST',
      body: JSON.stringify({ alias: val })
    });
    closeAlias();
    toast(r.alias ? '已改名为「' + r.alias + '」' : '已恢复原始昵称', 'ok');
    loadOverview(true);
  } catch (e) {
    const el = $('aliasErr');
    el.textContent = e.message;
    el.hidden = false;
  } finally { btn.disabled = false; }
}

$('btnAliasSave').onclick = () => saveAlias($('aliasInput').value);
$('btnAliasClear').onclick = () => saveAlias('');
$('btnAliasCancel').onclick = closeAlias;
$('aliasVeil').addEventListener('click', ev => { if (ev.target === $('aliasVeil')) closeAlias(); });
$('aliasInput').addEventListener('keydown', ev => {
  if (ev.key === 'Enter') { ev.preventDefault(); saveAlias($('aliasInput').value); }
  else if (ev.key === 'Escape') { ev.preventDefault(); closeAlias(); }
});

$('ckChips').addEventListener('click', ev => {
  const b = ev.target.closest('[data-ck]');
  if (!b) return;
  ckFilter = b.dataset.ck;
  renderCheckin();
});
$('ckSearch').addEventListener('input', () => { ckQuery = $('ckSearch').value; renderCheckin(); });
$('ckBody').addEventListener('click', async ev => {
  const b = ev.target.closest('[data-a]');
  if (!b) return;
  const u = b.dataset.u, a = b.dataset.a;
  if (a === 'tasks') { openTasks(u); return; }
  b.disabled = true;
  try {
    if (a === 'checkin') {
      const r = await api('accounts/' + encodeURIComponent(u) + '/checkin', { method: 'POST' });
      toast('签到完成' + (r.credits != null ? '，积分 ' + r.credits : '') + (r.checkin_message ? '（' + r.checkin_message + '）' : ''), 'ok');
    }
  } catch (e) { toast(e.message, 'err'); }
  finally { b.disabled = false; loadOverview(true); }
});
$('btnCkOn').onclick = () => setCheckinEnabled(true);
$('btnCkOff').onclick = () => setCheckinEnabled(false);

$('btnCheckinAll').onclick = async () => {
  try { await api('checkin_all', { method: 'POST' }); toast('全部签到已开始，几秒后本页会刷新进度', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnKeepaliveAll').onclick = async () => {
  try { await api('keepalive_all', { method: 'POST' }); toast('全部保活已开始，结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnAutoAll').onclick = async () => {
  const n = ((overviewData && overviewData.available) != null ? overviewData.available : null);
  const ok = await ask(
    '对「全部未禁用账号」逐个依次执行，不是只跑一个号。\n每个号大约 1–2 分钟，' +
    (n ? '当前共 ' + n + ' 个可用账号，' : '') + '期间请不要重复点。',
    { title: '给全部账号做任务？', ok: '全部开始' });
  if (!ok) return;
  try { await api('auto_all', { method: 'POST' }); toast('全部做任务已开始，进度在本页或运行日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnTravelAll').onclick = async () => {
  try { await api('travel_all', { method: 'POST' }); toast('旅行巡检已开始（含领养链路），结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnActivityAll').onclick = async () => {
  try { await api('activity_all', { method: 'POST' }); toast('活跃上报已开始，结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnDeadAudit').onclick = async () => {
  try { await api('dead_audit', { method: 'POST' }); toast('死号自检已开始：能 refresh 成功的误判号会自动复活，结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};

// 激活全部国际服号：补地区 → 注册激活 → 领 trial → 等入账 → 读余额（幂等，可反复点）。
// 逐个播报结果，失败不吞——「哪个号卡住了、卡在哪一步」是这个按钮唯一的价值。
$('btnActivateGlobal').onclick = async () => {
  const b = $('btnActivateGlobal');
  b.disabled = true;
  try {
    const r = await api('accounts/activate_all', { method: 'POST' });
    const n = r.count || 0;
    if (!n) { toast('没有国际服账号', 'err'); return; }
    const lines = (r.results || []).map(x =>
      (x.nickname || (x.uid || '').slice(0, 8)) + (x.ok ? '：积分 ' + x.credits : '：失败 ' + (x.error || '未知')));
    toast('激活 ' + n + ' 个国际服账号' + (r.failed ? '（失败 ' + r.failed + '）' : '') + '　' + lines.join('；'),
      r.failed ? 'err' : 'ok');
    await loadOverview(true);
  } catch (e) { toast(e.message, 'err'); }
  finally { b.disabled = false; }
};
$('btnReviveAll').onclick = async () => {
  try {
    const r = await api('revive_all', { method: 'POST' });
    toast(r.revived > 0 ? `已解冻 ${r.revived} 个账号` : '没有需要解冻的账号', 'ok');
    await loadOverview(true);
  } catch (e) { toast(e.message, 'err'); }
};
$('btnRemoveDisabled').onclick = async () => {
  // 破坏性操作：删凭证文件不可撤销，必须让用户看清后果再确认。
  const n = $('btnRemoveDisabled').textContent.replace(/[^\d]/g, '');
  const ok = await ask(
    '这 ' + n + ' 个账号会立即出池，auths/ 下的凭证文件会被删除，之后需要重新登录才能再用。\n\n' +
    '如果只是想救回被误判的号，请改用「死号自检」或「批量解冻」。',
    { title: '永久移除 ' + n + ' 个禁用账号？', ok: '永久移除', danger: true });
  if (!ok) return;
  try {
    const r = await api('remove_disabled', { method: 'POST' });
    if (r.message) toast(r.message, 'err'); // 部分失败：文件删除出错
    else toast(`已移除 ${r.removed} 个账号`, 'ok');
    await loadOverview(true);
  } catch (e) { toast(e.message, 'err'); }
};

/* ── 模型 ─────────────────────────────────────────────────────────── */
/* 实测上限标注（从 v1.11 移植）：scripts/probe_max_tokens.py --panel-out 写入探测结果，
   /panel/api/model_probes 只读透传。探测键带域前缀（cn:glm-5.2），模型表显示裸名，
   按「精确命中或 :后缀」关联。无数据时本列退回上游声称值。 */
function fmtK(n) { n = Number(n || 0); return n >= 1000 ? Math.round(n / 1000) + 'K' : String(n); }
function probeDays(ts) {
  if (!ts) return null;
  const t = new Date(String(ts).replace(' ', 'T'));
  const d = (Date.now() - t.getTime()) / 86400000;
  return isNaN(d) ? null : Math.floor(d);
}
// outCell 最大输出列：有实测就叠加标注，没有就用上游声称值。
function outCell(m, pr) {
  if (!pr) return '<td class="num">' + (m.max_output_tokens ? fmtK(m.max_output_tokens) : '—') + '</td>';
  const tip = '声称 ' + (pr.claimed ? fmtK(pr.claimed) : '?') + ' · 实测 ' + (pr.measured ? fmtK(pr.measured) : '?') +
    (pr.note ? ' · ' + pr.note : '') + (pr.tested_at ? ' · 探测于 ' + pr.tested_at : '');
  const days = probeDays(pr.tested_at);
  const stale = days !== null && days > 30 ? ' · ' + days + ' 天前' : '';
  if (pr.verdict === 'clamped' && pr.measured) {
    if (pr.claimed && pr.measured < pr.claimed) {
      const x = pr.claimed / pr.measured;
      const xs = (x >= 10 ? Math.round(x) : Math.round(x * 10) / 10) + '×';
      return '<td class="num" title="' + esc(tip) + '"><span style="color:var(--warn);font-weight:600">' +
        fmtK(pr.measured) + ' ⚠</span><div class="note">钳制 ' + xs + stale + '</div></td>';
    }
    return '<td class="num" title="' + esc(tip) + '"><span style="color:var(--ok)">' + fmtK(pr.measured) +
      (pr.claimed && pr.measured > pr.claimed ? ' ↑' : ' ✓') + '</span></td>';
  }
  if (pr.verdict === 'at_least' && pr.measured)
    return '<td class="num" title="' + esc(tip) + '"><span style="color:var(--ink-3)">≥' + fmtK(pr.measured) + '</span></td>';
  return '<td class="num" title="' + esc(tip) + '"><span style="color:var(--ink-3)">?</span><div class="note">未测出' + stale + '</div></td>';
}

// mdFilter 模型页筛选：'all' / 'cn' / 'global' / 'dup'（重复模型视图，可逐个钉域）。
// 与账号页 accFilter 互相独立。
let mdFilter = 'all';

// mdRealmCfg 最近读到的 model_realm 配置（{prefer, pins}）。
// 域归属口径与网关**完全一致**：pins[裸名] → prefer → 'cn'
//（单一事实来源 internal/server/resolve_model.go，两侧不可各写一套）。
let mdRealmCfg = { prefer: 'cn', pins: {} };

// mdPickRealm 裸名的生效域（与网关同一套优先级）。
function mdPickRealm(bare) {
  const p = (mdRealmCfg.pins || {})[bare];
  if (p === 'cn' || p === 'global') return p;
  return mdRealmCfg.prefer === 'global' ? 'global' : 'cn';
}

// mdDupGroups 收集「两域同名」的模型：裸名 → {cn: 条目, global: 条目}。
// 只有这类模型需要选域——名字唯一的模型走哪一域是确定的。
function mdDupGroups() {
  const byBare = {};
  for (const m of mdCache) {
    const b = mdBareOf(m);
    (byBare[b] = byBare[b] || {})[mdRealmOf(m)] = m;
  }
  const out = {};
  for (const b of Object.keys(byBare)) {
    if (byBare[b].cn && byBare[b].global) out[b] = byBare[b];
  }
  return out;
}

// realmTag 域徽标（国服蓝 / 国际服紫），与账号页、模型页同款。
function realmTag(realm, title) {
  const g = realm === 'global';
  return '<span class="realm-tag' + (g ? ' global' : '') + '"' +
    (title ? ' title="' + esc(title) + '"' : '') + '>' + (g ? '国际服' : '国服') + '</span>';
}

// saveModelRealm 只提交 model_realm 这一段（服务端深合并其余字段，配置不会被洗掉）。
// 保存即热生效：网关下一个请求就按新域解析，/v1/models 的归属随之变化，无需重启。
// pins 是**整体替换**语义（见 cmd/server/main.go saveConfig），所以「改回跟随默认」能删键。
async function saveModelRealm(patch, okMsg) {
  try {
    await api('config', { method: 'POST', body: JSON.stringify({ model_realm: patch }) });
  } catch (e) {
    toast('保存失败：' + e.message, 'err');
    return false;
  }
  if (patch.prefer) mdRealmCfg.prefer = patch.prefer;
  if (patch.pins) mdRealmCfg.pins = patch.pins;
  toast(okMsg + '（已写入配置，立即生效）', 'ok');
  renderMdChips(mdCache.length);
  applyMdFilter();
  return true;
}

// mdRealmOf 模型条目的域：后端 id 恒为 "cn:模型名" / "global:模型名"（panel.go
// panelModelEntry 拼前缀），裸名兜底视为 cn（与账号页 realmOf 口径一致）。
function mdRealmOf(m) {
  const i = (m.id || '').indexOf(':');
  if (i <= 0) return 'cn';
  const p = m.id.slice(0, i);
  return p === 'global' ? 'global' : 'cn';
}

// bareModelName 去掉域前缀的裸模型名——**这才是对外的调用值**：
// /v1/models 下发给客户端的就是裸名，域由网关按 model_realm 配置解析（模型页「重复模型」
// 视图里可钉）。面板各处的 "cn:"/"global:" 只是页面内部用来分行的键，凡是要显示给用户、
// 或要写进密钥白名单的值，都先过这里。
function bareModelName(id) {
  const s = id || '';
  const i = s.indexOf(':');
  return i > 0 ? s.slice(i + 1) : s;
}

// mdBareOf 模型条目的裸名（模型页 / 密钥页共用同一口径）。
function mdBareOf(m) {
  return bareModelName(m && m.id);
}

async function loadModels() {
  const tb = $('mdBody');
  tb.innerHTML = '<tr><td colspan="7"><div class="empty">正在向上游查询…</div></td></tr>';
  try {
    const [d, pr, cf] = await Promise.all([
      api('models'),
      api('model_probes').catch(() => ({})),
      // 域路由配置（prefer/pins）：只用于「重复模型」视图展示与改写；读不到就按默认
      //（缺省 cn，与网关一致），模型页其余部分照常工作。
      api('config').catch(() => null),
    ]);
    if (cf && cf.config) mdRealmCfg = Object.assign({ prefer: 'cn', pins: {} }, cf.config.model_realm || {});
    const probes = (pr && pr.probes) || {};
    // 探测键带域前缀；模型表显示裸名 → 精确命中，或按 ":后缀" 关联。
    const probeOf = id => probes[id] || probes['cn:' + id] ||
      Object.entries(probes).find(([k]) => k.endsWith(':' + id))?.[1];
    const all = d.models || [];
    if (!all.length) { tb.innerHTML = '<tr><td colspan="7"><div class="empty">上游未返回模型</div></td></tr>'; renderMdChips(0); return; }
    // 缓存到 window 外的模块级变量不可行（此函数可能被重复调用），直接每次重查；
    // 筛选只是视图层过滤，不重新打上游。
    mdCache = all;
    renderIfChanged('models', mdViewSig(), renderModels);
    $('mdNote').textContent = all.length + ' 个模型 · 缓存 120s' + dataTimeSuffix();
  } catch (e) {
    tb.innerHTML = '<tr><td colspan="7"><div class="empty">' + esc(e.message) + '</div></td></tr>';
  }
}

// mdCache 最近一次上游返回的全量模型列表；域 chip 点击时在视图层过滤，不重打上游。
let mdCache = [];

// renderMdChips 模型页域筛选条：全部 / 国服 / 国际服，计数随最近一次数据。
function renderMdChips(total) {
  const el = $('mdChips');
  if (!el) return;
  // 计数口径 = 过滤口径：按「条目所属域」数（域视图就是该域的目录）。重名模型在两个域各有
  // 一条目录条目（各自带本域倍率），两边的计数里各算一次，各域视图里也各出现一次。
  const n = { all: total, cn: 0, global: 0, dup: Object.keys(mdDupGroups()).length };
  for (const m of mdCache) n[mdRealmOf(m)]++;
  el.innerHTML = [['all', '全部'], ['cn', '国服'], ['global', '国际服'], ['dup', '重复模型']].map(([k, l]) =>
    '<button type="button" class="chip' + (mdFilter === k ? ' on' : '') + '" data-mf="' + k + '">' +
    l + '<span class="n">' + n[k] + '</span></button>').join('');
}

// renderDupRows「重复模型」视图表体：一行一个重名模型，带上「当前生效域」徽标、两域倍率
// 与逐模型钉域下拉，另有一行整体默认域。
//
// 为什么放这里而不是设置页：这两个开关的效果（/v1/models 里某个模型归哪一域）只有在这里
// 才看得见——配置项和它影响的对象应该在同一屏。
function renderDupRows(tb) {
  const groups = mdDupGroups();
  // 域开关生效时只列该域可用的重名模型（重名 = 两域都有，所以正常情况下不受影响；
  // 但若某域目录变化导致只剩一侧，这里能避免列出"该域其实没有"的行）。
  const names = Object.keys(groups)
    .filter(b => panelRealm === 'all' || (groups[b][panelRealm] != null))
    .sort();
  const pins = mdRealmCfg.pins || {};
  const preferSel = '<select data-dup-prefer>' +
    '<option value="cn"' + (mdRealmCfg.prefer !== 'global' ? ' selected' : '') + '>国服优先</option>' +
    '<option value="global"' + (mdRealmCfg.prefer === 'global' ? ' selected' : '') + '>国际服优先</option>' +
    '</select>';
  const note = '<tr><td colspan="7"><div class="dup-note">两域同名的模型按 ' + preferSel +
    ' 决定默认域，下表可逐个钉死；客户端也可用 <code>global:模型名</code> 强制某一域。</div></td></tr>';
  if (!names.length) {
    tb.innerHTML = note + '<tr><td colspan="7"><div class="empty">当前没有两域同名的模型</div></td></tr>';
    tb.querySelectorAll('select').forEach(s => dropdownFor(s));
    return;
  }
  tb.innerHTML = note + names.map(b => {
    const g = groups[b];
    const eff = mdPickRealm(b);
    const credit = r => (g[r] && g[r].credits) ? g[r].credits : '—';
    const pinSel = '<select data-dup-pin="' + esc(b) + '">' +
      '<option value=""' + (pins[b] ? '' : ' selected') + '>跟随默认</option>' +
      '<option value="cn"' + (pins[b] === 'cn' ? ' selected' : '') + '>国服</option>' +
      '<option value="global"' + (pins[b] === 'global' ? ' selected' : '') + '>国际服</option>' +
      '</select>';
    return '<tr class="dup-row"><td class="mark" aria-hidden="true"><i></i></td>' +
      '<td class="who"><div class="nm">' + esc(b) + realmTag(eff, '当前按此域调用') + '</div>' +
      '<div class="id">' + esc((g[eff] && g[eff].name) || '') + '</div></td>' +
      '<td colspan="5"><span class="rates">国服 ' + esc(credit('cn')) + ' · 国际服 ' + esc(credit('global')) +
      '</span>' + pinSel + '</td></tr>';
  }).join('');
  tb.querySelectorAll('select').forEach(s => dropdownFor(s));
}

// applyMdFilter 按 mdFilter 过滤 mdCache 并渲染表体；徽标分色与账号页同款
// （国服蓝 realm-tag / 国际服紫 realm-tag global）。模型 id 显示裸名（去域前缀），
// 域归属看徽标——前缀是内部路由键，对用户是噪音。
// mdViewSig 模型表的签名：目录条数 + 当前域筛选（含「重复模型」视图）。
function mdViewSig() { return mdCache.length + '|' + mdFilter + '|' + panelRealm; }

// renderModels 模型表的渲染入口（筛选条 + 表体一起画）。
function renderModels() { renderMdChips(mdCache.length); applyMdFilter(); }

function applyMdFilter() {
  const tb = $('mdBody');
  if (!tb) return;
  viewSig.models = mdViewSig(); viewDirty.models = false;
  if (mdFilter === 'dup') { renderDupRows(tb); return; }
  // 域视图 = 该域**自己的目录**（按条目所属域过滤）：重名模型在两个域各有条目（各带本域倍率
  // 与上下文窗口），所以两边都会出现一次——这正是"国服 x0.03 / 国际服免费"这类对比要看的东西。
  // 不用「该域可用」的口径：那会把国服条目也算进国际服视图，同一个模型出现两行（实测 26 → 37）。
  const inRealm = (m, r) => r === 'all' || mdRealmOf(m) === r;
  const list = mdCache.filter(m => inRealm(m, panelRealm) && inRealm(m, mdFilter));
  if (!list.length) { tb.innerHTML = '<tr><td colspan="7"><div class="empty">该域下暂无模型</div></td></tr>'; return; }
  const probes = mdProbeCache || {};
  const probeOf = id => probes[id] || probes['cn:' + id] ||
    Object.entries(probes).find(([k]) => k.endsWith(':' + id))?.[1];
  tb.innerHTML = list.map(m => {
    const realm = mdRealmOf(m);
    const bare = mdBareOf(m);
    const eff = (m.supported_efforts || []).slice();
    if (m.can_disable_thinking && eff.length && !eff.includes('off')) eff.push('off（可关）');
    const effs = eff.length ? eff.map(e => '<span class="tag warn">' + esc(e) + '</span>').join(' ')
      : '<span style="color:var(--ink-3);font-size:12.5px">' + (m.supports_reasoning ? '固定档 · 默认 ' + esc(m.default_effort || '?') : '不支持思考') + '</span>';
    return '<tr><td class="mark" aria-hidden="true"><i></i></td><td class="who"><div class="nm">' + esc(bare) + realmTag(realm) + '</div><div class="id">' + esc(m.name || '') + '</div></td>' +
      '<td class="num">' + (m.credits ? esc(m.credits) : '—') + '</td>' +
      '<td>' + (m.default_effort ? '<span class="tag ok">' + esc(m.default_effort) + '</span>' : '<span style="color:var(--ink-3)">—</span>') + '</td>' +
      '<td class="efs" style="white-space:normal">' + effs + '</td>' +
      '<td class="num">' + (m.context_length ? Math.round(m.context_length / 1000) + 'K' : '—') + '</td>' +
      outCell(m, probeOf(m.id)) + '</tr>';
  }).join('');
}

// mdProbeCache 探测结果随 loadModels 一起缓存，域筛选重绘时复用（不再二次请求）。
let mdProbeCache = null;

$('mdChips').addEventListener('click', ev => {
  const b = ev.target.closest('[data-mf]');
  if (!b) return;
  mdFilter = b.dataset.mf;
  // 与全局域开关双向同步（域 chip ↔ 开关；「重复模型」这一档不是域，开关回到「全部」）。
  setPanelRealm(mdFilter === 'cn' || mdFilter === 'global' ? mdFilter : 'all', true);
  renderMdChips(mdCache.length);
  applyMdFilter();
});

// 「重复模型」视图里的两个下拉：整体默认域 / 逐个钉死。都监听 change——自定义外观会把
// 用户的选择写回原生 select 并派发 change（pickDropdownOption 契约），所以这里读 value 即可。
$('mdBody').addEventListener('change', ev => {
  const t = ev.target;
  if (!t || t.tagName !== 'SELECT') return;
  if (t.hasAttribute('data-dup-prefer')) {
    saveModelRealm({ prefer: t.value }, '默认域已切到' + (t.value === 'global' ? '国际服' : '国服'));
    return;
  }
  const bare = t.getAttribute('data-dup-pin');
  if (!bare) return;
  const pins = Object.assign({}, mdRealmCfg.pins);
  if (t.value === 'cn' || t.value === 'global') pins[bare] = t.value;
  else delete pins[bare]; // 跟随默认：从表里删掉（服务端 pins 整体替换，删得掉）
  saveModelRealm({ pins: pins }, t.value
    ? ('已把 ' + bare + ' 钉到' + (t.value === 'global' ? '国际服' : '国服'))
    : ('已把 ' + bare + ' 改回跟随默认'));
});

$('btnModels').onclick = loadModels;

/* ── 日志（终端样式渲染）──────────────────────────────────────────────────── */
// 状态标记 → 彩色 badge 样式（对应 autotask.go / scheduler.go 里加的 [OK]/[FAIL] 等）
// 顺序敏感：先匹配更具体的（[FAIL] 比 [OK] 长？不，长度一样，按优先级排）。
const LOG_TAG = [
  { re: /\[FAIL\]/i, cls: 'fail', label: 'FAIL' },
  { re: /\[SKIP\]/i, cls: 'skip', label: 'SKIP' },
  { re: /\[WAIT\]/i, cls: 'wait', label: 'WAIT' },
  { re: /\[OK\]/i,   cls: 'ok',   label: 'OK'   },
  { re: />>/,        cls: 'run',  label: 'RUN'  },
];
// 分类标签：给每条日志自动归类（"这行属于哪块业务"），全部日志都有标签。
// 顺序敏感，实测踩过的坑都注在行尾：
//   1. 请求流水行（| #364 | … | TTFB=…）量最大，锚定行首放最前，既不被误抢也不白试后面的正则；
//   2. 启动配置行「XX已启用：」「已禁用」是配置快照不是业务动作，放业务规则之前截住
//     （不能用裸「加载」：面板「新账号已热加载」是账号操作，该归面板）；
//   3. 签到 > 旅行 > 任务：旅行领奖行「travel …: claim ok」含 claim，必须在任务规则之前；
//     「全量 签到（含猫猫旅行）」含旅行，必须在旅行规则之前；
//   4. 面板规则垫底兜住「panel: 设置别名/配置已保存」这类面板操作，但不碰「panel: 手动全量…」
//     ——那些行自带业务词（签到/旅行/验活），早已被前面的业务规则接走。
const LOG_CAT = [
  { re: /^\| #/, cls: 'c-chat', label: '对话' },
  { re: /已启用：|已禁用|schedule\.|未配置|编译|启动中/i, cls: 'c-sys', label: '系统' },
  { re: /checkin|签到|DailyCheckin/i, cls: 'c-checkin', label: '签到' },
  // 不要匹配裸 "buddy"：会命中服务名 "workbuddy2api"（实测把 listening 日志误判成旅行）
  { re: /travel|旅行|领养|adopt|depart/i, cls: 'c-travel', label: '旅行' },
  // "兑换" 不在这里：那是连登（streak）的业务词，放这会把「★ 兑换 7d 档」误判成任务
  { re: /一键完成|任务动作|accept|claim|领取|skill|expert|growth/i, cls: 'c-task', label: '任务' },
  { re: /activity|活跃/i, cls: 'c-act', label: '活跃' },
  // 死号自检（deadaudit）是保活后的号况检查，跟保活一族
  { re: /keepalive|保活|token|refresh|probe|验活|死号/i, cls: 'c-keep', label: '保活' },
  { re: /streak|连登|抽奖|补签|礼包/i, cls: 'c-streak', label: '连登' },
  { re: /blackcat|夜猫子|夜间/i, cls: 'c-night', label: '夜猫' },
  { re: /balance|user-resource|余额|积分|credits/i, cls: 'c-bal', label: '余额' },
  { re: /chat|对话|模型|prompt/i, cls: 'c-chat', label: '对话' },
  { re: /panel:/, cls: 'c-panel', label: '面板' },
];
const LOG_CAT_DEF = { cls: 'c-sys', label: '系统' };

let logFilter = 'all', logQuery = '', logLines = [];

function logCatOf(raw) {
  const ts = raw.match(/^\d{4}\/\d{2}\/\d{2} (\d{2}:\d{2}:\d{2})/);
  const text = ts ? raw.slice(ts[0].length).trim() : raw;
  for (const c of LOG_CAT) { if (c.re.test(text)) return c; }
  return LOG_CAT_DEF;
}

function logHaystack(raw) {
  return raw.replace(UID_RE, m => uidName(m) + ' ' + m);
}

function visibleLogs(lines) {
  const q = logQuery.trim().toLowerCase();
  return (lines || []).filter(raw => {
    if (logFilter !== 'all' && logCatOf(raw).label !== logFilter) return false;
    if (!q) return true;
    return logHaystack(raw).toLowerCase().includes(q);
  });
}

function renderLogChips(lines) {
  const el = $('logChips');
  if (!el) return;
  const n = {};
  for (const raw of lines) {
    const l = logCatOf(raw).label;
    n[l] = (n[l] || 0) + 1;
  }
  const keys = Object.keys(n);
  if (logFilter !== 'all' && !n[logFilter]) keys.unshift(logFilter);
  el.innerHTML = ['全部'].concat(keys).map(l => {
    const count = l === '全部' ? lines.length : (n[l] || 0);
    const on = (l === '全部' && logFilter === 'all') || logFilter === l;
    const val = l === '全部' ? 'all' : l;
    return '<button type="button" class="chip' + (on ? ' on' : '') + '" data-c="' + esc(val) + '">' +
      esc(l) + '<span class="n">' + count + '</span></button>';
  }).join('');
}

// logViewSig 日志视图的签名：数据（行数与最后一行）+ 频道筛选 + 搜索词。
function logViewSig() {
  return logLines.length + '|' + (logLines[logLines.length - 1] || '') + '|' + logFilter + '|' + logQuery;
}

function paintLogs(force) {
  const box = $('logBox');
  if (!box || box.dataset.cleared === '1') return;
  viewSig.logs = logViewSig(); viewDirty.logs = false;
  // 轮询场景下日志内容往往没变（服务安静时）——chips 与表体都跳过重建。
  // 变了才全量重画（500 行 DOM 重建 + 每行多正则，是这页最大的渲染成本）。
  const chipsKey = logLines.length + '|' + logFilter;
  if (!force && chipsKey === paintLogs._chipsKey && paintLogs._body === viewSig.logs) {
    const m0 = $('termMeta');
    if (m0) m0.textContent = logLines.length + ' 行 · 自动刷新 ' + Math.round(POLL_MS / 1000) + 's';
    return;
  }
  paintLogs._chipsKey = chipsKey;
  paintLogs._body = viewSig.logs;
  renderLogChips(logLines);
  const vis = visibleLogs(logLines);
  box.innerHTML = vis.length
    ? vis.map(fmtLogLine).join('')
    : '<span style="color:#6b7280">' + (logLines.length ? '没有符合筛选的日志' : '暂无日志（服务运行中，等任务触发）') + '</span>';
  const m = $('termMeta');
  if (m) m.textContent = vis.length === logLines.length
    ? logLines.length + ' 行 · 自动刷新 ' + Math.round(POLL_MS / 1000) + 's'
    : vis.length + ' / ' + logLines.length + ' 行';
  if ($('logFilterNote')) $('logFilterNote').textContent = vis.length === logLines.length ? '' : '已筛选';
}

// uid 在日志里是一长串 8-4-4-4-12 十六进制，满屏刷时非常难读。
// 这里统一替换成显示名（别名 > 昵称 > uid 前 8 位），完整 uid 放 title 供 hover。
const UID_RE = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi;
// acctInfo 把审计里的 account（存的是 **uid 前 8 位**）还原成可展示的身份。
//
// 显示优先级：**别名 > 手机号 > uid 前 8 位**。
// 为什么不再把 uid 缩写当主要标识：「5c805214」对人没有任何识别价值——既认不出是
// 谁，也和别处对不上。WorkBuddy 的 nickname 字段就是手机号，那才是能认的那个。
// uid 退到悬停提示里，需要核对时仍拿得到。
//
// 为什么必须兼容 8 位缩写：审计流水为省磁盘把 uid 截断了，拿缩写去按完整 uid
// 等值查别名**永远查不到**——「出口」列一直显示十六进制就是这个原因。
function acctInfo(acct) {
  const list = (overviewData && overviewData.accounts) || [];
  let a = list.find(x => x.uid === acct);
  if (!a && acct && acct.length >= 8) {
    const pre = acct.slice(0, 8);
    a = list.find(x => x.uid.slice(0, 8) === pre);
  }
  const alias = (a && a.alias) || '';
  const phone = (a && a.nickname) || '';
  return {
    name: alias || phone || acct || '—',
    sub: (alias && phone) ? phone : '', // 别名在上、手机号在下；没别名时手机号就在上
    title: a ? 'uid ' + a.uid : (acct ? 'uid 前 8 位 ' + acct + '（账号已移除？）' : ''),
  };
}

// acctCell 出口列的单元格内容：别名在上、手机号在下。
function acctCell(acct) {
  if (!acct) return '—';
  const i = acctInfo(acct);
  return '<div class="nm" title="' + esc(i.title) + '">' + esc(i.name) + '</div>' +
    (i.sub ? '<div class="id" title="' + esc(i.title) + '">' + esc(i.sub) + '</div>' : '');
}

// uidName 把 uid（完整或**前 8 位缩写**）还原成显示名：别名 > 昵称 > uid 前 8 位。
//
// 为什么兼容缩写：审计流水里存的 account 是 `uidPrefix()` 截过的前 8 位
//（为了省磁盘），拿它去按完整 uid 查别名永远查不到。这是它唯一容易出错的地方，
// 也是为什么要写成前缀匹配而不是等值匹配。
// __uidMap uidName 的查询缓存：500 行日志 × 每行 1~2 次 uid 替换，原来是每次都
// 对 23 个账号做线性 find（单次渲染 1 万+ 次字符串比较）。按 overviewData 引用失效。
let __uidMapKey = null, __uidMap = null;
function uidName(uid) {
  const list = (overviewData && overviewData.accounts) || [];
  if (__uidMapKey !== list) {
    __uidMapKey = list;
    __uidMap = new Map();
    for (const a of list) {
      if (a.uid) __uidMap.set(a.uid, a);
      if (a.uid && a.uid.length >= 8) __uidMap.set(a.uid.slice(0, 8), a);
    }
  }
  const hit = __uidMap.get(uid) || (uid && uid.length >= 8 ? __uidMap.get(uid.slice(0, 8)) : null);
  if (!hit) return (uid || '').slice(0, 8);
  return hit.alias || hit.nickname || hit.uid.slice(0, 8);
}
function uidNameSlow(uid) {
  const list = (overviewData && overviewData.accounts) || [];
  let a = list.find(x => x.uid === uid);
  if (!a && uid && uid.length >= 8) {
    const pre = uid.slice(0, 8);
    a = list.find(x => x.uid.slice(0, 8) === pre);
  }
  if (!a) return (uid || '').slice(0, 8); // 账号已移除 / overview 还没拉到
  return a.alias || a.nickname || a.uid.slice(0, 8);
}

// 终端框里每行日志的结构化渲染：
//   [HH:MM:SS]  [分类]  [状态?]  消息内容
// 时间戳从 Go log 的 "2026/09/13 18:25:37" 抽成 [18:25:37]，正文里不再重复原时间戳。
// 分类标签每行都有；状态标记（[OK]/[FAIL]/…）只在出现时追加。
function fmtLogLine(raw) {
  // 时间戳提出来单独渲染，正文裁掉原前缀（否则一行里出现两次时间）
  const ts = raw.match(/^\d{4}\/\d{2}\/\d{2} (\d{2}:\d{2}:\d{2})/);
  const tsHtml = ts ? `<span class="ln-ts">[${ts[1]}]</span>` : '';
  const text = ts ? raw.slice(ts[0].length).trim() : raw;

  const cat = logCatOf(raw);

  // 状态标记
  let st = null;
  for (const t of LOG_TAG) { if (t.re.test(text)) { st = t; break; } }

  // 正文：先转义再把 uid 换成显示名（uid 本身是 hex，转义不影响匹配）
  let body = esc(text).replace(UID_RE, m => `<span class="ln-uid" title="${esc(m)}">${esc(uidName(m))}</span>`);

  // 行级染色：已有 [FAIL] 等状态标记时不再重复标红
  let cls = '';
  if (!st) {
    if (/error|失败|错误|FATAL/i.test(text)) cls = ' e';
    else if (/warn|冷却|熔断/i.test(text)) cls = ' w';
  }
  return `<span class="ln${cls}">${tsHtml}<span class="ln-cat ${cat.cls}">${cat.label}</span>` +
         (st ? `<span class="ln-tag ${st.cls}">${st.label}</span>` : '') + body + '</span>';
}
// 日志区尺寸记忆：用户拖右下角 resize → ResizeObserver 监听变化 → 落 localStorage，
// 下次打开 logs view 时从 localStorage 恢复。initLogResize 幂等。
const LOG_H_KEY = 'wb2api.logHeight.v2';
let _logRO = null;
function initLogResize() {
  const box = $('logBox');
  // 恢复用户拖过的高度。旧键 wb2api.logHeight 存的是 480px 默认值，不再沿用。
  if (!box.style.height) {
    const h = localStorage.getItem(LOG_H_KEY);
    if (h && parseInt(h, 10) >= 280) box.style.height = h;
  }
  if (_logRO) return;
  _logRO = new ResizeObserver(entries => {
    // resize 过程会高频触发，debounce 400ms 后再落盘
    const h = Math.round(entries[0].contentRect.height);
    clearTimeout(_logRO._t);
    _logRO._t = setTimeout(() => {
      try { localStorage.setItem(LOG_H_KEY, h + 'px'); } catch {}
    }, 400);
  });
  _logRO.observe(box);
}

/* ── 一键完成：进度条 + 桌面通知 ───────────────────────────────────────
   纯前端方案，不新增后端接口：直接解析日志流里已经存在的固定格式。
     开始：panel: 一键完成 uid=<u> 开始，共 <N> 项（含批量接受）；…
     进度：panel: 一键完成 uid=<u> [<i>/<n>] [OK|FAIL|SKIP|WAIT|>>] <code> <detail>
     结束：panel: 一键完成 uid=<u> 全部结束 ✅ 共 <N> 项（…）耗时 <s>
   每次都整段重扫（ring buffer 只有数百行，代价可忽略），这样即便中途刷新页面、
   或同时跑了多个账号，也能自洽地取"最后一段"批次，不需要前端保存中间状态。
   时间戳直接从日志行里解析，剩余时间估算因此不受页面打开时机影响。            */
const TP_STEP = /一键完成 uid=(\S+) \[(\d+)\/(\d+)\]/;
const TP_START = /一键完成 uid=(\S+) 开始，共 (\d+) 项/;
const TP_DONE = /一键完成 uid=(\S+) 全部结束 ✅/;
const TP_HIDE_MS = 15000;   // 完成后再挂 15 秒（期间保持满格灰条），然后收起

const TP = { uid: '', idx: 0, total: 0, done: false, doneAt: 0, firstTs: null, lastTs: null, notifiedKey: '' };

// 从 Go log 前缀 "2026/09/13 18:25:37" 取当天秒数，用于估算速率
function tpSecOfDay(raw) {
  const m = /^\d{4}\/\d{2}\/\d{2} (\d{2}):(\d{2}):(\d{2})/.exec(raw);
  return m ? (+m[1]) * 3600 + (+m[2]) * 60 + (+m[3]) : null;
}

function updateTaskProgress(lines) {
  let uid = '', total = 0, idx = 0, seen = false, done = false;
  let firstTs = null, lastTs = null, doneRaw = '';
  for (const raw of lines) {
    const s = TP_START.exec(raw);
    if (s) { uid = s[1]; total = +s[2]; idx = 0; seen = true; done = false; firstTs = lastTs = null; doneRaw = ''; continue; }
    if (!seen) continue;
    const st = TP_STEP.exec(raw);
    if (st) {
      uid = st[1];
      if (+st[3] > 0) total = +st[3];
      if (+st[2] > idx) idx = +st[2];
      const ts = tpSecOfDay(raw);
      if (ts != null) { if (firstTs == null) firstTs = ts; lastTs = ts; }
      continue;
    }
    if (TP_DONE.test(raw)) { done = true; doneRaw = raw; }
  }
  if (!seen || total <= 0) { TP.total = 0; renderTaskProgress(); return; }

  // 上一批已完成且已过展示期 → 收起，等下一批
  if (TP.done && TP.uid === uid && TP.doneAt && Date.now() - TP.doneAt > TP_HIDE_MS) {
    TP.total = 0; renderTaskProgress(); return;
  }
  // 新批次开始 → 重置完成态
  if (!done && (uid !== TP.uid || idx < TP.idx)) { TP.doneAt = 0; TP.notifiedKey = ''; }
  TP.uid = uid; TP.total = total; TP.idx = idx;
  TP.firstTs = firstTs; TP.lastTs = lastTs;

  if (done && !TP.done) { TP.done = true; TP.doneAt = Date.now(); }
  if (!done) TP.done = false;

  renderTaskProgress();

  // 桌面通知：同一条结束日志只推一次（1.5s 轮询会反复看到同一行）
  if (done && notifyOn && doneRaw !== TP.notifiedKey) {
    TP.notifiedKey = doneRaw;
    const nOK = /共 \d+ 项（(\d+) 完成/.exec(doneRaw);
    newNotify('一键完成 ✅', uidName(uid) + ' 任务已结束' + (nOK ? '，完成 ' + nOK[1] + ' 项' : ''));
  }
}

function paintTaskDlgProgress() {
  const el = $('taskDlgProg');
  if (!el) return;
  if (!TP.total || !$('taskVeil').classList.contains('on')) { el.hidden = true; return; }
  el.hidden = false;
  const pct = TP.done ? 100 : Math.min(100, Math.round((TP.idx / TP.total) * 100));
  $('taskDlgFill').style.width = pct + '%';
  el.classList.toggle('done', TP.done);
  $('taskDlgText').textContent = $('tpText').textContent;
}
function renderTaskProgress() {
  const el = $('taskProgress');
  if (!el) return;
  if (!TP.total) { el.hidden = true; paintTaskDlgProgress(); return; }
  el.hidden = false;
  // done 时强制满格：结束时 idx 通常已等于 total，但「全部结束」日志可能先到
  // （或末项失败被跳过），此时按 idx/total 算会停在 95% 之类的假进度。
  const pct = TP.done ? 100 : Math.min(100, Math.round((TP.idx / TP.total) * 100));
  $('tpFill').style.width = pct + '%';
  el.classList.toggle('done', TP.done);
  let txt = uidName(TP.uid) + ' · 第 ' + TP.idx + '/' + TP.total + ' 项 · ' + pct + '%';
  if (!TP.done && TP.idx > 1 && TP.firstTs != null && TP.lastTs != null) {
    const per = (TP.lastTs - TP.firstTs) / (TP.idx - 1);   // 每项平均秒数
    if (per > 0) txt += ' · 预计剩余 ' + dur(per * (TP.total - TP.idx));
  }
  txt += TP.done ? ' · 已完成' : ' · 进行中…';
  $('tpText').textContent = txt;
  paintTaskDlgProgress();
}

/* ── 桌面通知开关 ─────────────────────────────────────────────────── */
const LS_NOTIFY = 'wb2api.notify';
let notifyOn = localStorage.getItem(LS_NOTIFY) === '1';

function syncNotifyBtn() {
  const b = $('btnNotify');
  if (b) b.textContent = '通知：' + (notifyOn ? '开' : '关');
}
function newNotify(title, body) {
  try {
    if (typeof Notification === 'undefined' || Notification.permission !== 'granted') return;
    const n = new Notification(title, { body, tag: 'wb2api-task', renotify: true });
    // close 在回调里，不在上面的 try 覆盖范围内——单独包一层，别让一个通知对象的
    // 实现差异（或已被用户手动关闭）冒泡成未捕获异常。
    setTimeout(() => { try { n.close(); } catch { /* 已关闭/不支持 */ } }, 8000);
  } catch { /* 通知失败不影响主流程 */ }
}
syncNotifyBtn();
$('btnNotify').onclick = async () => {
  if (notifyOn) {
    notifyOn = false;
  } else {
    if (typeof Notification === 'undefined') { toast('当前浏览器不支持桌面通知', 'err'); return; }
    let perm = Notification.permission;
    if (perm === 'default') {
      try { perm = await Notification.requestPermission(); } catch { perm = 'denied'; }
    }
    if (perm !== 'granted') {
      toast('通知权限被拒绝。仅 localhost / https 可用；可在地址栏左侧站点设置里重新允许', 'err');
      return;
    }
    notifyOn = true;
  }
  localStorage.setItem(LS_NOTIFY, notifyOn ? '1' : '0');
  syncNotifyBtn();
  toast(notifyOn ? '任务跑完时会弹桌面通知' : '已关闭桌面通知');
};

async function loadLogs() {
  // 日志区尺寸记忆：用户拖右下角 resize 后落 localStorage，下次进入恢复。
  // initLogResize 幂等，多次调用只挂一次 ResizeObserver。
  initLogResize();
  const box = $('logBox');
  // 清屏只是关掉前端视图：后端 ring buffer 还在，自动滚动再次开启时同步回来
  const cleared = box.dataset.cleared === '1';
  const atEnd = box.scrollTop + box.clientHeight >= box.scrollHeight - 24;
  try {
    const d = await api('logs');
    const lines = d.lines || [];
    // 进度条/桌面通知独立于视图：清屏后依然解析（否则清屏期间任务跑完就没通知了）
    logLines = lines;
    updateTaskProgress(lines);
    if (cleared) { renderLogChips(lines); return; }
    // 日志最多 500 行、每行要跑几个正则 + 逐行查账号名：数据没变就别重画
    //（轮询每 5 秒来一次，重画还会打断正在读的那一段）。
    if (view === 'logs') {
      if (renderIfChanged('logs', logViewSig(), paintLogs) && logPin && atEnd) box.scrollTop = box.scrollHeight;
    } else {
      markDirty('logs', logViewSig());
    }
  } catch (e) { /* 概览已提示 */ }
}
$('btnLogPin').onclick = () => {
  logPin = !logPin;
  $('btnLogPin').textContent = '自动滚动：' + (logPin ? '开' : '关');
  if (logPin) { $('logBox').dataset.cleared = '0'; loadLogs(); }
};
$('logChips').addEventListener('click', ev => {
  const b = ev.target.closest('[data-c]');
  if (!b) return;
  logFilter = b.dataset.c;
  paintLogs();
});
$('logSearch').addEventListener('input', () => { logQuery = $('logSearch').value; paintLogs(); });
$('btnLogCopy').onclick = () => {
  const vis = visibleLogs(logLines);
  const text = vis.join('\n');
  const done = () => toast(vis.length ? '已复制 ' + vis.length + ' 行' : '没有可复制的日志', 'ok');
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(done, () => toast('复制失败', 'err'));
  } else toast('当前环境不支持复制', 'err');
};
$('btnLogClear').onclick = () => {
  const box = $('logBox');
  box.dataset.cleared = '1';
  box.innerHTML = '<span style="color:#6b7280">已清屏（只清前端视图；后端 ring buffer 仍保留日志——下次打开「自动滚动」会重新同步）</span>';
};

/* ── 使用日志（请求审计流水）─────────────────────────────────────────
   数据源 /panel/api/usage?date=YYYY-MM-DD：当天明细 + 按人/按模型分桶。
   与「运行日志」的分工：那个是进程吐出来的**文本流**（服务在干什么），
   这个是每个请求的**结构化明细**（谁用的、什么模型、花了多少、快不快），
   落盘保留 7 天、重启不丢，而且能按人切开。

   筛选与汇总全在明细上做（不再打后端）：一次拉回一天的量（后端上限 2000 条），
   换筛选条件零延迟，也省得用户每点一下都等一轮网络。                      */
let useData = null;       // 最近一次 /panel/api/usage 响应
let useDay = '';          // 当前选中日期（YYYY-MM-DD）
// useDataRealm 上面那份 payload 是**按哪个域**取的。用量是服务端按域过滤的，
// 换域后旧数据不能拿来充数（会显示错域的数字），所以记下它属于哪个域。
let useDataRealm = null;
let useUser = '';         // 密钥（用户）筛选，'' = 全部
let useModel = '';        // 模型筛选
let useAccount = '';      // 出口账号筛选
let useMode = 'all';      // all / stream / sync
// 状态筛选。取值按 HTTP 段分档（与参考设计同口径），另加两个排查档：
//   ok      2xx 成功
//   client  4xx 客户端错误
//   server  5xx 服务端错误
//   other   其它非 2xx（如 3xx、上游自定义码）
//   missing 200 但上游没给 usage（上游异常的早期信号）
//   slow    首字或总耗时超阈值
let useStatus = 'all';
let useQuery = '';

// 慢请求阈值。前端判定与后端无关，纯展示层概念——写在这里而不是散在渲染里，
// 保证「筛选慢请求」和「标黄」永远是同一口径。
const USE_SLOW_TTFB_MS = 10000;
const USE_SLOW_TOTAL_MS = 30000;

// useUserName / useModelName 与后端 audit.Summarize 的回落口径必须一致：
// 后端分桶把空值映射成「默认」「未知」，前端若不一致，筛出来会是空表。
function useUserName(r) { return r.user || '默认'; }
function useModelName(r) { return (!r.model || r.model === '-') ? '未知' : r.model; }
function useAccountName(r) { return r.account || ''; }
function useBad(r) { return r.status !== 200; }
function useMiss(r) { return r.status === 200 && !r.has_usage; }
function useSlow(r) {
  return (r.ttfb_ms || 0) >= USE_SLOW_TTFB_MS || (r.total_ms || 0) >= USE_SLOW_TOTAL_MS;
}

// useStatusMatch 状态筛选的唯一口径。抽出来是为了让「筛选」和「行高亮」不会各判各的。
function useStatusMatch(r, want) {
  const s = r.status;
  switch (want) {
    case 'all': return true;
    case 'ok': return s === 200;
    case 'client': return s >= 400 && s < 500;
    case 'server': return s >= 500 && s < 600;
    case 'other': return s !== 200 && !(s >= 400 && s < 600);
    case 'missing': return useMiss(r);
    case 'slow': return useSlow(r);
    default: return true;
  }
}

// 状态筛选项：value → 文案。菜单与「当前值」回显都从这里取，不各写一份。
const USE_STATUS_OPTS = [
  { v: 'all', label: '全部' },
  { v: 'ok', label: '2xx · 成功' },
  { v: 'client', label: '4xx · 客户端错误' },
  { v: 'server', label: '5xx · 服务端错误' },
  { v: 'other', label: '其它错误' },
  { v: '_sep' },
  { v: 'missing', label: '200 但缺用量' },
  { v: 'slow', label: '慢请求（首字>10s 或 >30s）' },
];
function useStatusLabel(v) {
  const o = USE_STATUS_OPTS.filter(x => x.v === v)[0];
  return o ? o.label : '全部';
}

// fmtCredit 积分显示。上游给的是浮点（实测可为 0.01），最多保留 2 位；
// 整数不补 .00——「3」比「3.00」更像人话。
function fmtCredit(v) {
  v = Number(v) || 0;
  if (v === 0) return '0';
  const r = Math.round(v * 100) / 100;
  return Number.isInteger(r) ? String(r) : r.toFixed(2);
}

// useTime 时间列。带日期不省：复查"是不是昨天深夜那次"时，只给时分秒还得回头看。
function useTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return '—';
  const p = n => String(n).padStart(2, '0');
  return (d.getMonth() + 1) + '/' + p(d.getDate()) + ' ' +
    p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}

// useToday 本地今天的 YYYY-MM-DD（与后端 audit.Today() 同口径，都用本地时区）。
function useToday() {
  const d = new Date();
  const p = n => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate());
}

// useDayLabel 日期下拉的文案：今天/昨天更符合"我想看刚刚那批"的语感。
function useDayLabel(day) {
  const md = day.slice(5).replace('-', '/');
  if (day === useToday()) return '今天 · ' + md;
  const d = new Date();
  d.setDate(d.getDate() - 1);
  const p = n => String(n).padStart(2, '0');
  if (day === d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate())) return '昨天 · ' + md;
  return md;
}

// useRows 返回当前筛选后的明细。
function useRows() {
  const src = (useData && useData.rows) || [];
  const q = useQuery.trim().toLowerCase();
  return src.filter(r => {
    if (useUser && useUserName(r) !== useUser) return false;
    if (useModel && useModelName(r) !== useModel) return false;
    if (useAccount && useAccountName(r) !== useAccount) return false;
    if (useMode !== 'all' && r.mode !== useMode) return false;
    if (!useStatusMatch(r, useStatus)) return false;
    if (q) {
      const hay = (useModelName(r) + ' ' + (r.account || '') + ' ' + (r.client || '') + ' ' + useUserName(r)).toLowerCase();
      if (hay.indexOf(q) < 0) return false;
    }
    return true;
  });
}

// useActiveFilters 生效中的筛选项（用于按钮角标与"筛了什么"的回显）。
// 只数「筛选菜单」里的五个维度——角标显示的就是这个。
function useActiveFilters() {
  const out = [];
  if (useUser) out.push('密钥 ' + useUser);
  if (useModel) out.push('模型 ' + useModel);
  if (useAccount) out.push('账号 ' + useAccount);
  if (useMode !== 'all') out.push(useMode === 'stream' ? '流式' : '同步');
  if (useStatus !== 'all') out.push(useStatusLabel(useStatus));
  return out;
}

// useHasAnyFilter 表格当前是否被任何条件限制——比 useActiveFilters 多算一个搜索框。
// 「清除筛选」按钮要覆盖用户能施加的**全部**限制：只在搜索框里打字同样会把表筛空，
// 那种情况下没有清除按钮就只能手动删字，正是这个按钮要解决的问题。
function useHasAnyFilter() {
  return useActiveFilters().length > 0 || useQuery.trim() !== '';
}

// useAgg 在筛选后的明细上算汇总。KPI 跟着筛选走（"另一台电脑今天花了多少"就是这个数），
// 而筛选菜单里的计数是全天的——两者口径不同，所以页头会标明当前筛选状态。
function useAgg(rows) {
  const a = { n: rows.length, ok: 0, fail: 0, miss: 0, credit: 0,
    prompt: 0, completion: 0, cached: 0, reasoning: 0, ttfbSum: 0, ttfbN: 0 };
  for (const r of rows) {
    if (r.status === 200) a.ok++; else a.fail++;
    if (useMiss(r)) a.miss++;
    a.credit += Number(r.credit) || 0;
    a.prompt += r.prompt || 0;
    a.completion += r.completion || 0;
    a.cached += r.cached || 0;
    a.reasoning += r.reasoning || 0;
    if (r.ttfb_ms > 0) { a.ttfbSum += r.ttfb_ms; a.ttfbN++; }
  }
  a.ttfbAvg = a.ttfbN ? Math.round(a.ttfbSum / a.ttfbN) : 0;
  a.total = a.prompt + a.completion;
  a.okRate = a.n ? (a.ok / a.n * 100) : 0;
  a.cacheRate = a.prompt ? (a.cached / a.prompt * 100) : 0;
  return a;
}

// renderUseKpi 四张 KPI 卡片。用全站统一的 kpi()（独立卡片）而不是面板内分格：
// 参考设计这一行就是卡片样式，下面那条 token 才是半强度、再往下彻底融入背景。
function renderUseKpi(a) {
  const okCls = a.fail ? (a.fail > a.ok ? 'bad' : 'warn') : 'good';
  $('useKpi').innerHTML =
    kpi('总请求', fmtInt(a.n), '', a.n ? a.ok + ' 成功 · ' + a.fail + ' 失败' : '当前筛选下没有记录',
      '当前筛选条件下的请求条数') +
    kpi('总 Tokens', fmtTok(a.total), '', a.prompt ? '缓存命中率 ' + a.cacheRate.toFixed(1) + '%' : '',
      '输入 + 输出合计；缓存命中率 = 命中前缀缓存的输入 ÷ 输入总量') +
    kpi('成功率', a.n ? a.okRate.toFixed(1) + '%' : '—', okCls,
      a.ttfbN ? '平均首字 ' + fmtMs(a.ttfbAvg) : '没有首字节样本',
      '成功 = HTTP 200。平均首字只统计流式请求观测到的样本') +
    kpi('消耗积分', fmtCredit(a.credit), '', a.n ? '平均 ' + fmtCredit(a.credit / a.n) + ' / 次' : '',
      '上游 usage.credit 的原值累加（真实扣费，不是估算）');
}

// renderUseTokStrip token 合计细条：输入 / 输出 / 缓存 / 推理，各占一格。
// 单列出来而不是塞进 KPI：它们是同一件事的四个分量，横排一行才好对比。
function renderUseTokStrip(a) {
  const cell = (icon, label, v, tip) =>
    '<div class="cell" title="' + esc(tip) + '">' + icon +
    '<span class="lb">' + esc(label) + '</span><b>' + fmtInt(v) + '</b></div>';
  const dn = '<svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M8 3.2v9.2M4.4 8.8 8 12.4l3.6-3.6"/></svg>';
  const up = '<svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M8 12.8V3.6M4.4 7.2 8 3.6l3.6 3.6"/></svg>';
  const box = '<svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.4"><path d="M2.4 5.2 8 2.4l5.6 2.8v5.6L8 13.6l-5.6-2.8z"/><path d="M2.4 5.2 8 8l5.6-2.8M8 8v5.6"/></svg>';
  const brain = '<svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.4"><path d="M6.2 2.6a2 2 0 0 0-2 2 1.8 1.8 0 0 0-1 3.2 2 2 0 0 0 1.2 3.4 2 2 0 0 0 3.6 1.2V4.4a1.8 1.8 0 0 0-1.8-1.8z"/><path d="M9.8 2.6a2 2 0 0 1 2 2 1.8 1.8 0 0 1 1 3.2 2 2 0 0 1-1.2 3.4 2 2 0 0 1-3.6 1.2"/></svg>';
  $('useTokStrip').innerHTML =
    cell(dn, '输入', a.prompt, 'prompt_tokens 合计（含缓存命中部分）') +
    cell(up, '输出', a.completion, 'completion_tokens 合计（含推理 token）') +
    cell(box, '缓存命中', a.cached, '命中前缀缓存、不计费的输入部分') +
    cell(brain, '推理', a.reasoning, '思维链 token，是输出的一部分；上游没给时记 0');
}

// ── 筛选菜单 ────────────────────────────────────────────────────────
// 五个维度（模型/状态/模式/密钥/账号）做成级联：平铺出来是一大片，
// 而大多数维度平时都停在"全部"。
let fltOpenCat = ''; // 当前展开子菜单的分类

// fltOptions 每个分类的可选项。计数取**全天**分桶（不是筛选后），
// 这样一眼能看出"今天谁在用什么"再决定筛哪个。
function fltOptions(cat) {
  const d = useData || {};
  const total = ((d.summary || {}).requests) || 0;
  const all = { v: '', label: '全部', n: total };
  if (cat === 'model') {
    return [all].concat((d.models || []).map(b => ({ v: b.name, label: b.name, n: b.requests })));
  }
  if (cat === 'user') {
    return [all].concat((d.users || []).map(b => ({ v: b.name, label: b.name, n: b.requests })));
  }
  if (cat === 'account') {
    // 账号分桶后端没给，就从明细现算（按出口账号）。
    //
    // 注意 v 与 label 的分工：**v 必须是原始的 uid 前 8 位**（它要参与筛选比对），
    // label 才是给人看的别名。两者混用会让"筛得中但显示错"或反之——
    // 这正是「出口」列之前显示十六进制缩写的同一个坑。
    const m = {};
    for (const r of (d.rows || [])) {
      const k = useAccountName(r) || '—';
      m[k] = (m[k] || 0) + 1;
    }
    const keys = Object.keys(m).sort((a, b) => m[b] - m[a] || (a < b ? -1 : 1));
    return [all].concat(keys.map(k => ({
      v: k === '—' ? '' : k,
      // 筛选项也显示别名：这里显示 uid 缩写的话，用户根本不知道该点哪个。
      label: k === '—' ? '（未记录）' : acctInfo(k).name,
      n: m[k],
    })));
  }
  if (cat === 'mode') {
    let s = 0, y = 0;
    for (const r of (d.rows || [])) { if (r.mode === 'stream') s++; else y++; }
    return [{ v: 'all', label: '全部', n: total },
      { v: 'stream', label: '流式', n: s }, { v: 'sync', label: '同步', n: y }];
  }
  if (cat === 'status') {
    // 计数按同一套判定现算，保证"菜单里写着 2"和"筛出来是 2 条"永远一致。
    const cnt = {};
    for (const r of (d.rows || [])) {
      for (const o of USE_STATUS_OPTS) {
        if (o.v === '_sep' || o.v === 'all') continue;
        if (useStatusMatch(r, o.v)) cnt[o.v] = (cnt[o.v] || 0) + 1;
      }
    }
    return USE_STATUS_OPTS.map(o => o.v === '_sep' ? { sep: true }
      : { v: o.v, label: o.label, n: o.v === 'all' ? total : (cnt[o.v] || 0) });
  }
  return [all];
}

// fltCurrent 分类当前选中的值。
function fltCurrent(cat) {
  if (cat === 'model') return useModel;
  if (cat === 'user') return useUser;
  if (cat === 'account') return useAccount;
  if (cat === 'mode') return useMode;
  return useStatus;
}

// fltSet 写回分类选中的值。
function fltSet(cat, val) {
  if (cat === 'model') useModel = val;
  else if (cat === 'user') useUser = val;
  else if (cat === 'account') useAccount = val;
  else if (cat === 'mode') useMode = val || 'all';
  else useStatus = val || 'all';
}

const USE_FILTER_CATS = [
  { k: 'model', label: '模型' },
  { k: 'status', label: '状态' },
  { k: 'mode', label: '模式' },
  { k: 'user', label: '密钥' },
  { k: 'account', label: '账号' },
];

// renderFilterMenu 画出菜单本体。每次选择后整体重画——菜单很小，
// 重画比逐个更新 DOM 的勾选状态简单，也不会出现"勾选和实际筛选不一致"。
function renderFilterMenu() {
  const menu = $('fltMenu');
  if (!menu) return;
  // 子菜单默认向右展开；只有右边真的放不下才翻到左边。
  //
  // 判据必须用按钮的**右边缘**到窗口右边的距离，不能只看左边缘：
  // 菜单本身有宽度（≈180）+ 子菜单（≈200）+ 间隙，加起来约 400px。
  // 之前判据写反了——"靠右" 时反而往右展开，正好顶出窗口。
  // flip 由 openFilterMenu() 在打开那一刻算好（见那里的注释）：渲染路径里读布局会强制
  // 同步布局，而本函数在每次筛选变化/数据刷新时都会被调用，是可见顿挫的来源之一。
  menu.className = 'pop flt-menu' + (fltFlip ? ' flip' : '');

  menu.innerHTML = USE_FILTER_CATS.map(c => {
    const cur = fltCurrent(c.k);
    const opts = fltOptions(c.k);
    const curLabel = (opts.filter(o => !o.sep && String(o.v) === String(cur))[0] || { label: '全部' }).label;
    const isSet = cur !== '' && cur !== 'all';
    const sub = fltOpenCat === c.k
      ? '<div class="pop flt-sub">' + opts.map(o => o.sep
        ? '<div class="flt-sep"></div>'
        : '<button type="button" class="flt-opt' + (String(o.v) === String(cur) ? ' on' : '') +
          '" data-cat="' + esc(c.k) + '" data-val="' + esc(o.v) + '">' +
          '<span>' + esc(o.label) + '</span>' +
          (o.n == null ? '' : '<span class="cnt">' + fmtInt(o.n) + '</span>') +
          '<svg class="tick" viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3.2 8.4 6.4 11.6 12.8 5.2"/></svg>' +
          '</button>').join('') + '</div>'
      : '';
    return '<div class="flt-cat' + (isSet ? ' set' : '') + '" data-cat="' + esc(c.k) + '"' +
      ' aria-expanded="' + (fltOpenCat === c.k ? 'true' : 'false') + '">' +
      '<span>' + esc(c.label) + '</span>' +
      '<span class="cur">' + esc(isSet ? curLabel : '') + '</span>' +
      '<svg class="chev" viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M6.2 3.6 10.6 8l-4.4 4.4"/></svg>' +
      sub + '</div>';
  }).join('');

  // 角标：生效中的筛选条件个数，鼠标悬停列出具体是哪几个。
  const n = useActiveFilters().length;
  $('fltCount').hidden = n === 0;
  $('fltCount').textContent = String(n);
  $('btnUseFilter').classList.toggle('active', n > 0);
  // 有文字的按钮不弹浮层（见 tipWanted），所以这条信息改挂 aria-label：)r
  // 它不会触发提示，但屏幕阅读器仍读得到，也不算白写。
  $('btnUseFilter').setAttribute('aria-label', n
    ? '筛选（已生效：' + useActiveFilters().join(' · ') + '）'
    : '按模型 / 状态 / 模式 / 密钥 / 账号筛选');
  // 「清除筛选」只在真的筛了东西时出现——平时不占位置。搜索框也算（见 useHasAnyFilter）。
  if ($('btnUseClear')) $('btnUseClear').hidden = !useHasAnyFilter();
}

// clearUseFilters 一次清空全部筛选（含搜索框）。只清筛选、**不重拉数据**：
// 全部数据已经在内存里，重新渲染即可。
function clearUseFilters() {
  useUser = ''; useModel = ''; useAccount = '';
  useMode = 'all'; useStatus = 'all'; useQuery = '';
  if ($('useSearch')) $('useSearch').value = '';
  fltOpenCat = '';
  closeFilterMenu();
  renderUsage();
}

// fltFlip 子菜单往右还是往左展开。**只在打开菜单时算一次**：判据要用按钮的右边缘到窗口
// 右边的距离（菜单 ≈180 + 子菜单 ≈200 + 间隙，加起来约 400px），这需要读布局——
// 而读布局放在渲染路径里会强制同步布局（每次筛选变化都来一次），所以挪到这里。
let fltFlip = false;
function openFilterMenu() {
  const wrap = $('fltWrap');
  if (wrap && wrap.getBoundingClientRect && typeof window !== 'undefined') {
    const vw = window.innerWidth || 1200;
    fltFlip = (vw - wrap.getBoundingClientRect().right) < 400;
  }
  fltOpenCat = '';
  $('fltMenu').hidden = false;
  $('btnUseFilter').setAttribute('aria-expanded', 'true');
  renderFilterMenu();
}
function closeFilterMenu() {
  const m = $('fltMenu');
  if (!m || m.hidden) return;
  m.hidden = true;
  fltOpenCat = '';
  $('btnUseFilter').setAttribute('aria-expanded', 'false');
}
function filterMenuOpen() { const m = $('fltMenu'); return !!m && !m.hidden; }

// useRowHTML 一行明细。列序：用户 / 模型 / 出口 / 类型 / 状态 / 用量 / 响应性能 / 时间。
// 用量与性能各自是一个「标签 + 值」小网格塞在**一个**单元格里——摊成独立列会让
// 每列都窄到读不出数，还必然横向滚动。
function useRowHTML(r) {
  const bad = useBad(r), miss = useMiss(r);
  const trCls = bad ? ' class="row-bad"' : (miss ? ' class="row-miss"' : '');
  const dot = bad ? 'bad' : (miss ? 'warn' : 'ok');
  const credit = Number(r.credit) || 0;
  const tokps = (r.completion > 0 && r.total_ms > 0) ? (r.completion / (r.total_ms / 1000)) : 0;
  const usageCell = v => r.has_usage ? fmtInt(v) : '—';
  return '<tr' + trCls + '>' +
    '<td class="who"><div class="nm">' + esc(useUserName(r)) + '</div>' +
    (r.client ? '<div class="id" title="' + esc(r.client) + '">' + esc(r.client) + '</div>' : '') + '</td>' +
    '<td>' + esc(useModelName(r)) + '</td>' +
    '<td class="who">' + acctCell(r.account) + '</td>' +
    '<td>' + (r.mode === 'stream' ? '流式' : '同步') + '</td>' +
    '<td><span class="sdot ' + dot + '"></span>' + r.status + '</td>' +
    '<td><div class="ugrid">' +
    '<span class="u"><span class="k">输入</span><span class="n">' + usageCell(r.prompt) + '</span></span>' +
    '<span class="u"><span class="k">输出</span><span class="n">' + usageCell(r.completion) + '</span></span>' +
    '<span class="u"><span class="k">缓存</span><span class="n">' + usageCell(r.cached) + '</span></span>' +
    '<span class="u"><span class="k">积分</span><span class="n' + (credit > 0 ? ' hot' : '') + '">' + fmtCredit(credit) + '</span></span>' +
    '</div></td>' +
    '<td><div class="perf">' +
    '<span class="k">总耗时</span><span class="n' + (r.total_ms >= USE_SLOW_TOTAL_MS ? ' slow' : '') + '">' + fmtMs(r.total_ms) + '</span>' +
    '<span class="k">首字</span><span class="n' + ((r.ttfb_ms || 0) >= USE_SLOW_TTFB_MS ? ' slow' : '') + '">' + fmtMs(r.ttfb_ms) + '</span>' +
    '<span class="k">速度</span><span class="n">' + (tokps > 0 ? tokps.toFixed(1) + ' tok/s' : '—') + '</span>' +
    '</div></td>' +
    '<td class="num">' + esc(useTime(r.ts)) + '</td>' +
    '</tr>';
}

function renderUsage() {
  if (!useData) return;
  viewSig.usage = useViewSig(); viewDirty.usage = false;
  const days = useData.days || [];
  const opts = days.slice();
  if (useDay && opts.indexOf(useDay) < 0) opts.unshift(useDay); // 有筛选但那天没文件
  const sel = $('useDate');
  sel.innerHTML = opts.length
    ? opts.map(d => '<option value="' + esc(d) + '">' + esc(useDayLabel(d)) + '</option>').join('')
    : '<option value="' + esc(useDay) + '">' + esc(useDay) + '</option>';
  sel.value = useDay;
  syncSelect(sel); // 日期选项是动态重建的，外观要跟着刷新

  const rows = useRows();
  const agg = useAgg(rows);
  renderUseKpi(agg);
  renderUseTokStrip(agg);
  renderFilterMenu();
  $('useBody').innerHTML = rows.length
    ? rows.map(useRowHTML).join('')
    : dashEmpty(8, ((useData.summary || {}).requests || 0)
      ? '没有符合筛选的记录（换一个筛选条件试试）'
      : '这一天还没有请求记录');

  // 条数说明把"截断"和"丢失"都讲清楚：审计是尽力而为的，
  // 静默少给数据比少给本身更糟。
  const notes = [];
  notes.push(rows.length === (useData.rows || []).length
    ? rows.length + ' 条'
    : rows.length + ' / ' + (useData.rows || []).length + ' 条');
  const acts = useActiveFilters();
  if (acts.length) notes.push('筛选中');
  if (useData.truncated) notes.push('当天超过 ' + (useData.rows || []).length + ' 条，只显示最近的部分');
  if (useData.dropped) notes.push('另有 ' + useData.dropped + ' 条未写入（队列满）');
  if (useData.write_errors) notes.push(useData.write_errors + ' 次写盘失败');
  if (useData.bad_lines) notes.push('有 ' + useData.bad_lines + ' 行读不出来（当天数据不完整）');
  $('useCount').textContent = notes.join(' · ');
  // 页头把"筛选了什么"写出来：KPI 跟着筛选走，不写清楚的话数字对不上全天会觉得是 bug。
  $('useNote').textContent = '保留 ' + (useData.retention || 7) + ' 天 · ' + useDay + dataTimeSuffix() +
    (acts.length ? ' · ' + acts.join(' + ') : '');
}

// useViewSig 用量页的签名：数据（日期/条数/汇总）+ 域 + 这一页自己的全部筛选。
// 筛选必须进签名——筛选变化是原地重画（不重新请求），签名不跟着变就会被守卫跳过。
function useViewSig() {
  if (!useData) return '';
  return [panelRealm, useData.date || '', (useData.rows || []).length,
    JSON.stringify(useData.summary || {}), useUser, useModel, useAccount, useMode,
    useStatus, useQuery].join('|');
}

async function loadUsage(day) {
  if (day) useDay = day;
  // 域开关跟随：审计的汇总与明细都由后端按域过滤（口径一致，不会出现合计与明细对不上）
  const q = (useDay ? ('?date=' + encodeURIComponent(useDay)) : '?') +
    (panelRealm === 'all' ? '' : (useDay ? '&' : '') + 'realm=' + panelRealm);
  $('useNote').textContent = '读取中…';
  try {
    const d = await api('usage' + q);
    useData = d;
    useDay = d.date;
    useDataRealm = panelRealm;   // 记下这份数据属于哪个域（切域时据此判断能不能直接用）
    // 换天后旧的筛选可能指向不存在的东西，会显示"空表但不知道为什么"，重置。
    useUser = ''; useModel = ''; useAccount = ''; useMode = 'all'; useStatus = 'all';
    fltOpenCat = '';
    closeFilterMenu();
    renderIfChanged('usage', useViewSig(), renderUsage);
  } catch (e) {
    $('useNote').textContent = '读取失败';
    $('useCount').textContent = '';
    $('useBody').innerHTML = dashEmpty(8, '读取失败：' + e.message);
  }
}

// useCSV 把当前筛选后的明细拼成 CSV。带 UTF-8 BOM，否则 Excel 打开中文是乱码。
function useCSV(rows) {
  const cell = v => {
    const s = String(v == null ? '' : v);
    return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
  };
  const head = ['时间', '用户', '模型', '出口账号', '类型', '状态',
    '输入token', '输出token', '缓存token', '消耗积分', '首字ms', '总耗时ms', '客户端'];
  const lines = [head.join(',')];
  for (const r of rows) {
    lines.push([
      useTime(r.ts), useUserName(r), useModelName(r), r.account || '',
      r.mode === 'stream' ? '流式' : '同步', r.status,
      r.has_usage ? r.prompt : '', r.has_usage ? r.completion : '', r.has_usage ? r.cached : '',
      Number(r.credit) || 0, r.ttfb_ms || 0, r.total_ms || 0, r.client || '',
    ].map(cell).join(','));
  }
  return '\ufeff' + lines.join('\r\n') + '\r\n';
}

$('useDate').addEventListener('change', () => { loadUsage($('useDate').value); });
$('useSearch').addEventListener('input', () => { useQuery = $('useSearch').value; renderUsage(); });

// 筛选菜单的交互：
//   点按钮 → 开/关；悬停某个分类 → 展开子菜单（与参考设计一致，比"点开再点"少一步）；
//   点选项 → 写回并关掉整个菜单。点别处或 Esc 也关。
$('btnUseFilter').onclick = ev => {
  ev.stopPropagation();
  if (filterMenuOpen()) closeFilterMenu(); else openFilterMenu();
};
$('fltMenu').addEventListener('mouseover', ev => {
  const cat = ev.target.closest && ev.target.closest('.flt-cat');
  if (!cat || cat.dataset.cat === fltOpenCat) return;
  fltOpenCat = cat.dataset.cat;
  renderFilterMenu();
});
$('fltMenu').addEventListener('click', ev => {
  // 选项：生效并关闭
  const opt = ev.target.closest && ev.target.closest('.flt-opt');
  if (opt) {
    fltSet(opt.dataset.cat, opt.dataset.val);
    closeFilterMenu();
    renderUsage();
    return;
  }
  // 分类行：点一下也能展开（触屏与"悬停不灵"的兜底）
  const cat = ev.target.closest && ev.target.closest('.flt-cat');
  if (cat) {
    fltOpenCat = fltOpenCat === cat.dataset.cat ? '' : cat.dataset.cat;
    renderFilterMenu();
  }
  ev.stopPropagation();
});
document.addEventListener('click', ev => {
  if (!filterMenuOpen()) return;
  const w = $('fltWrap');
  if (w && w.contains && w.contains(ev.target)) return;
  closeFilterMenu();
});

$('btnUseRefresh').onclick = () => loadUsage();
$('btnUseClear').onclick = clearUseFilters;
$('btnUseCsv').onclick = () => {
  const rows = useRows();
  if (!rows.length) { toast('没有可导出的记录', 'err'); return; }
  const csv = useCSV(rows);
  const name = 'workbuddy-usage-' + useDay + '.csv';
  // 优先真下载；CSP 或环境不允许时退回剪贴板，绝不静默失败。
  try {
    const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }));
    const a = document.createElement('a');
    a.href = url; a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 4000);
    toast('已导出 ' + rows.length + ' 条到 ' + name, 'ok');
  } catch (e) {
    copyText(csv, '浏览器不允许下载，已复制 ' + rows.length + ' 条到剪贴板（可粘进 Excel）');
  }
};

/* ── 密钥（额外密钥的增删改）─────────────────────────────────────────
   与「设置」页的分工：默认密钥兼面板登录密码，留在设置里改；这里只管
   发给别人的那些——新建 / 改名 / 删除。

   「最近 7 天」这一列来自「请求审计」，是这一页真正的价值所在：
   删密钥之前唯一该问的问题是"还有没有人在用"，而这个答案只有审计流水知道。 */
let keysData = null;
let keysRealm = null;     // keysData 属于哪个域（「最近 7 天」用量列是服务端按域算的）
let keyDlgMode = 'new'; // 'new' | 'edit' | 'done'
let keyDlgID = '';      // edit/连接脚本/删除 的目标 id
let keyDlgKey = '';     // done 态要复制的完整密钥
let keyDlgBusy = false;
let keyScope = 'all';   // all | selected
let keyPolicyModels = [];

async function loadKeys() {
  // 域开关跟随：密钥列表的「最近 7 天」用量列按域统计（密钥本身与域无关，用量有域之分）
  const keyRealmQ = panelRealm === 'all' ? '' : ('?realm=' + panelRealm);
  $('keysNote').textContent = '读取中…';
  try {
    const d = await api('keys' + keyRealmQ);
    keysData = d;
    keysRealm = panelRealm;
    renderIfChanged('keys', keyViewSig(), renderKeys);
  } catch (e) {
    $('keysNote').textContent = '';
    $('keysBody').innerHTML = dashEmpty(5, '读取失败：' + e.message);
  }
}

// keyUseCell 「最近 7 天」列。没人用过要明说「没人用过」而不是留白——
// 留白会被读成"加载失败"，而这一格的结论直接决定能不能删。
function keyUseCell(k) {
  if (!k.requests) return '<td class="use none">没人用过</td>';
  const bits = [];
  if (k.credit) bits.push(fmtCredit(k.credit) + ' 积分');
  if (k.failed) bits.push('<span style="color:var(--bad)">' + k.failed + ' 次失败</span>');
  return '<td class="use">' + fmtInt(k.requests) + ' 次' +
    (bits.length ? '<div class="sub">' + bits.join(' · ') + '</div>' : '') + '</td>';
}

function keyRowHTML(k) {
  const own = !!k.owner;
  const modelScope = (k.models || []).length ? (k.models.length + ' 个模型') : '全部模型';
  const concurrency = k.max_concurrency ? ('并发 ' + k.max_concurrency) : '并发不限';
  const acts = own
    ? '<span style="color:var(--ink-3);font-size:12px">在「设置」里改</span>'
    : '<div class="btn-row">' +
      '<button type="button" class="xs" data-act="copy" data-id="' + esc(k.id) + '">复制</button>' +
      '<button type="button" class="xs primary" data-act="share" data-id="' + esc(k.id) + '">连接脚本</button>' +
      '<button type="button" class="xs" data-act="edit" data-id="' + esc(k.id) + '">编辑</button>' +
      '<button type="button" class="xs danger" data-act="del" data-id="' + esc(k.id) + '">删除</button>' +
      '</div>';
  return '<tr' + (own ? ' class="own"' : '') + '>' +
    '<td class="kname">' + esc(k.name) +
    '<div class="sub">' + (own ? '默认密钥 · 兼面板密码' : '额外密钥') + '</div></td>' +
    '<td class="kk" title="点右侧「复制」拿完整密钥">' + esc(k.masked) + '</td>' +
    '<td>' + (own ? '调 /v1 + 登录面板' : esc(modelScope + ' · ' + concurrency)) + '</td>' +
    keyUseCell(k) +
    '<td class="acts">' + acts + '</td>' +
    '</tr>';
}

// keyViewSig 密钥表的签名：域 + 密钥列表（「最近 7 天」用量列是服务端按域算的）。
function keyViewSig() {
  return panelRealm + '|' + JSON.stringify((keysData && keysData.keys) || []);
}

function renderKeys() {
  if (!keysData) return;
  viewSig.keys = keyViewSig(); viewDirty.keys = false;
  const ks = keysData.keys || [];
  $('keysBody').innerHTML = ks.length
    ? ks.map(keyRowHTML).join('')
    : dashEmpty(5, '还没有密钥');
  const extra = ks.filter(k => !k.owner).length;
  $('keysNote').textContent = '额外密钥 ' + extra + ' 把 · 用量取最近 ' + (keysData.stat_days || 7) + ' 天';
}

$('keysBody').addEventListener('click', ev => {
  const b = ev.target.closest('[data-act]');
  if (!b || !keysData) return;
  const k = (keysData.keys || []).filter(x => x.id === b.dataset.id)[0];
  if (!k) return;
  if (b.dataset.act === 'copy') copyText(k.key, '密钥已复制，发给对方即可');
  else if (b.dataset.act === 'share') downloadKeyShare(k);
  else if (b.dataset.act === 'edit') openKeyDlg('edit', k);
  else if (b.dataset.act === 'del') delKey(k);
});

async function downloadKeyShare(k) {
  const h = {};
  const ownerKey = localStorage.getItem(LS_KEY);
  if (ownerKey) h.Authorization = 'Bearer ' + ownerKey;
  try {
    const r = await fetch('/panel/api/keys/' + encodeURIComponent(k.id) + '/share', { method: 'POST', headers: h });
    if (!r.ok) {
      const d = await r.json().catch(() => ({}));
      throw new Error(d.error || ('HTTP ' + r.status));
    }
    const url = URL.createObjectURL(await r.blob());
    const a = document.createElement('a');
    a.href = url;
    a.download = 'WorkBuddy-' + (k.name || 'device').replace(/[\\/:*?"<>|]/g, '_') + '-连接.bat';
    document.body.appendChild(a);
    a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 4000);
    toast('连接脚本已下载（拷到另一台电脑双击即用）；重新下载会让旧脚本失效', 'ok');
  } catch (e) { toast('下载失败：' + e.message, 'err'); }
}

// delKey 二次确认里把"还有没有人在用"写在最显眼的位置：
// 这是唯一一个能让别人突然断掉的操作，而用户此刻最容易忽略的就是这件事。
async function delKey(k) {
  const used = k.requests
    ? '注意：这把密钥最近 ' + (keysData.stat_days || 7) + ' 天被用过 ' + k.requests + ' 次，删了他会立刻断掉。'
    : '这把密钥最近没有被使用过，删掉是安全的。';
  const ok = await ask('删除密钥「' + k.name + '」？\n\n' + used + '\n\n删除后对方立刻无法再调用 /v1 接口。',
    { title: '删除密钥', ok: '删除', danger: true });
  if (!ok) return;
  try {
    await api('keys/' + encodeURIComponent(k.id) + '/delete', { method: 'POST' });
    toast('已删除密钥「' + k.name + '」', 'ok');
    loadKeys();
  } catch (e) { toast('删除失败：' + e.message, 'err'); }
}

function openKeyDlg(mode, k) {
  keyDlgMode = mode;
  keyDlgID = k ? k.id : '';
  keyDlgKey = '';
  $('keyDlgErr').hidden = true;
  $('keyNameRow').hidden = false;
  $('keyDoneRow').hidden = true;
  $('keyPolicyRows').hidden = mode !== 'edit';
  $('btnKeyShare').hidden = true;
  $('btnKeySubmit').textContent = mode === 'new' ? '生成' : '保存';
  $('keyDlgTitle').textContent = mode === 'new' ? '新建密钥' : '编辑密钥';
  $('keyDlgHint').textContent = mode === 'new'
    ? '名字会出现在「请求审计」里，用来分辨是谁在用。'
    : '限制只作用于这把密钥；修改后立即生效。历史请求审计仍保留当时的名字。';
  $('keyNameInput').value = k ? k.name : '';
  if (mode === 'edit') {
    $('keyConcurrencyInput').value = k.max_concurrency || 0;
    keyScope = (k.models || []).length ? 'selected' : 'all';
    paintKeyScope();
    loadKeyPolicyModels(k.models || []);
  }
  $('newKeyVeil').classList.add('on');
  setTimeout(() => $('keyNameInput').focus(), 60);
}
function closeKeyDlg() { $('newKeyVeil').classList.remove('on'); }

function paintKeyScope() {
  $('keyScopeChips').querySelectorAll('[data-key-scope]').forEach(b => b.classList.toggle('on', b.dataset.keyScope === keyScope));
  const sel = keyScope === 'selected';
  $('keyModelList').hidden = !sel;
  $('keyModelState').hidden = !sel;
  $('keyModelTools').hidden = !sel; // 搜索 + 域筛选只在「指定模型」下有意义
}

$('keyScopeChips').addEventListener('click', ev => {
  const b = ev.target.closest('[data-key-scope]');
  if (!b) return;
  keyScope = b.dataset.keyScope;
  paintKeyScope();
  // 新建密钥时列表从未加载过（loadKeyPolicyModels 只在编辑分支被调用），此前切到
  // 「指定模型」会得到一张空表 → 一个模型都勾不上，保存被"至少选择一个模型"挡下。
  // 这里补一次按需加载；编辑分支已加载过（有子节点）则不重复请求。
  if (keyScope === 'selected' && !$('keyModelList').children.length) loadKeyPolicyModels([]);
});

async function loadKeyPolicyModels(selected) {
  $('keyModelState').hidden = false;
  $('keyModelState').textContent = '正在同步服务器模型与倍率…';
  $('keyModelList').innerHTML = '';
  try {
    const d = await api('models');
    keyPolicyModels = d.models || [];
    // 白名单按**裸名**展示与保存：/v1/models 下发给客户端的调用值就是裸名，域只影响路由，
    // 用徽标标出即可。历史白名单里带 "cn:"/"global:" 的条目按裸名归并——否则同一模型会
    // 出现两条（一条勾着一条没勾），看着像没保存上（网关侧白名单本就按裸名比对）。
    const chosen = new Set(selected.map(id => bareModelName(id)));
    // 行按**裸名**归并，并记录它在哪些域可用（重名模型两域都标）：白名单只认名字、与域无关，
    // 所以这里要回答的是「这个模型在国服/国际服可不可用」——只保留先出现那一域会让
    // 「按国际服筛」漏掉实际可用的重名模型（如 glm-5.3 两域都有）。
    const byBare = new Map();
    for (const m of keyPolicyModels) {
      const bare = bareModelName(m.id);
      let r = byBare.get(bare);
      if (!r) { r = { bare: bare, realms: {}, credits: {} }; byBare.set(bare, r); }
      const rl = mdRealmOf(m);
      r.realms[rl] = true;
      r.credits[rl] = m.credits || '';
    }
    for (const bare of chosen) {
      if (!byBare.has(bare)) byBare.set(bare, { bare: bare, realms: {}, credits: {} });
    }
    keyModelRows = [...byBare.values()].sort((a, b) => (a.bare < b.bare ? -1 : a.bare > b.bare ? 1 : 0));
    keyModelChosen = chosen;
    keyModelQuery = '';
    keyModelRealm = 'all';
    $('keyModelSearch').value = '';
    renderKeyModelRealmChips();
    renderKeyModelRows();
    paintKeyScope();
  } catch (e) {
    // 同步失败：按已选条目原样回显（同样归一到裸名，与保存口径一致）。
    const rows = [...new Set(selected.map(id => bareModelName(id)))].sort();
    keyModelRows = rows.map(bare => ({ bare: bare, realms: {}, credits: {} }));
    keyModelChosen = new Set(rows);
    keyModelQuery = '';
    keyModelRealm = 'all';
    $('keyModelSearch').value = '';
    renderKeyModelRealmChips();
    renderKeyModelRows();
    $('keyModelState').textContent = '模型同步失败：' + e.message;
    paintKeyScope();
  }
}

// keyModelRows 全量行（裸名/域/倍率）；keyModelChosen 勾选集合；两个筛选条件都只在视图层
// 生效——**勾选状态存在集合里而不是 DOM 里**，否则筛选/搜索一重绘，被隐藏条目的勾选就丢了
//（模型 34 个、默认列表还要滚动，搜索几乎是必用功能）。
let keyModelRows = [];
let keyModelChosen = new Set();
let keyModelQuery = '';
let keyModelRealm = 'all';

// renderKeyModelRealmChips 域筛选条（带计数）：全部 / 国服 / 国际服。
// 计数是「该域可用的模型数」——重名模型两域各计一次（可用性是事实，两个数加起来会大于
// 总数，这是对的）。
function renderKeyModelRealmChips() {
  const el = $('keyModelRealmChips');
  if (!el) return;
  const n = { all: keyModelRows.length, cn: 0, global: 0 };
  for (const r of keyModelRows) {
    if (r.realms.cn) n.cn++;
    if (r.realms.global) n.global++;
  }
  el.innerHTML = [['all', '全部'], ['cn', '国服'], ['global', '国际服']].map(([k, l]) =>
    '<button type="button" class="chip' + (keyModelRealm === k ? ' on' : '') + '" data-key-realm="' + k + '">' +
    l + '<span class="n">' + n[k] + '</span></button>').join('');
}

// renderKeyModelRows 按「搜索词 + 域」过滤后渲染列表，并刷新已选计数。
// 域筛选口径：模型在所选域**可用**即命中（重名模型在国服、国际服两侧都会出现）。
function renderKeyModelRows() {
  const box = $('keyModelList');
  if (!box) return;
  const q = keyModelQuery.toLowerCase();
  const list = keyModelRows.filter(r =>
    (keyModelRealm === 'all' || r.realms[keyModelRealm]) &&
    (!q || r.bare.toLowerCase().includes(q)));
  box.innerHTML = list.length ? list.map(r => {
    const realms = ['cn', 'global'].filter(x => r.realms[x]);
    const badges = realms.map(x => realmTag(x)).join('');
    // 倍率：单域直接给值（徽标已表明是哪个域）；两域并列（同名模型两域倍率可能不同）。
    const rate = realms.map(x => (realms.length > 1 ? (x === 'global' ? '国际服 ' : '国服 ') : '') +
      (r.credits[x] || '未知')).join(' · ');
    return '<label><input type="checkbox" value="' + esc(r.bare) + '"' +
      (keyModelChosen.has(r.bare) ? ' checked' : '') + '>' +
      '<span class="rname">' + esc(r.bare) + badges + '</span>' +
      '<span class="rate">' + esc(rate) + '</span></label>';
  }).join('') : '<div class="hint" style="padding:6px">没有匹配的模型</div>';
  const shown = list.length === keyModelRows.length ? '' : '（当前显示 ' + list.length + ' 个）';
  $('keyModelState').textContent = '已选 ' + keyModelChosen.size + ' 个 · 共 ' + keyModelRows.length + ' 个模型 · 倍率来自服务器当前列表' + shown;
}

// 勾选：改的是集合（同一份数据可反复筛选重绘而不丢选择）。
$('keyModelList').addEventListener('change', ev => {
  const t = ev.target;
  if (!t || t.type !== 'checkbox') return;
  if (t.checked) keyModelChosen.add(t.value); else keyModelChosen.delete(t.value);
  renderKeyModelRows();
});

$('keyModelSearch').addEventListener('input', ev => {
  keyModelQuery = ev.target.value.trim();
  renderKeyModelRows();
});

$('keyModelRealmChips').addEventListener('click', ev => {
  const b = ev.target.closest('[data-key-realm]');
  if (!b) return;
  keyModelRealm = b.dataset.keyRealm;
  renderKeyModelRealmChips();
  renderKeyModelRows();
});

$('btnKeyNew').onclick = () => openKeyDlg('new');
$('btnKeysRefresh').onclick = loadKeys;
$('btnKeyCancel').onclick = closeKeyDlg;
$('btnKeyShare').onclick = () => {
  const k = keysData && (keysData.keys || []).filter(x => x.id === keyDlgID)[0];
  downloadKeyShare(k || { id: keyDlgID, name: $('keyNameInput').value.trim() || 'device' });
};
$('keyNameInput').addEventListener('keydown', ev => {
  if (ev.key === 'Enter') { ev.preventDefault(); $('btnKeySubmit').click(); }
});
document.addEventListener('keydown', ev => {
  if (ev.key === 'Escape' && $('newKeyVeil').classList.contains('on')) closeKeyDlg();
});

$('btnKeySubmit').onclick = async () => {
  if (keyDlgBusy) return;
  // 生成后按钮变成「复制密钥」：这时不关弹窗，因为下一步必然是把它复制走。
  if (keyDlgMode === 'done') { copyText(keyDlgKey, '密钥已复制'); return; }

  const name = $('keyNameInput').value.trim();
  keyDlgBusy = true;
  $('btnKeySubmit').disabled = true;
  try {
    if (keyDlgMode === 'new') {
      const r = await api('keys', { method: 'POST', body: JSON.stringify({ name: name }) });
      keyDlgKey = r.key.key;
      keyDlgID = r.key.id;
      $('keyDoneValue').textContent = r.key.key;
      $('keyNameRow').hidden = true;
      $('keyDoneRow').hidden = false;
      $('keyDlgTitle').textContent = '密钥已生成';
      $('keyDlgHint').textContent = '把这串复制给对方，填到他的客户端里。';
      $('btnKeySubmit').textContent = '复制密钥';
      $('btnKeyShare').hidden = false;
      keyDlgMode = 'done';
      loadKeys();
    } else {
      // 从**勾选集合**取，而不是从 DOM 取：列表可能被搜索/域筛选过滤过，DOM 里只剩可见的那几行。
      const models = keyScope === 'selected' ? [...keyModelChosen].sort() : [];
      if (keyScope === 'selected' && !models.length) throw new Error('指定模型模式至少选择一个模型');
      const concurrency = Number($('keyConcurrencyInput').value || 0);
      if (!Number.isInteger(concurrency) || concurrency < 0 || concurrency > 100) throw new Error('并发限制需为 0-100 的整数');
      await api('keys/' + encodeURIComponent(keyDlgID) + '/rename',
        { method: 'POST', body: JSON.stringify({ name: name, models: models, max_concurrency: concurrency }) });
      toast('密钥设置已保存', 'ok');
      closeKeyDlg();
      loadKeys();
    }
  } catch (e) {
    $('keyDlgErr').textContent = e.message;
    $('keyDlgErr').hidden = false;
  } finally {
    keyDlgBusy = false;
    $('btnKeySubmit').disabled = false;
  }
};

/* ── 配置 ─────────────────────────────────────────────────────────── */
const CFG_MAP = {
  listen: ['listen'], api_key: ['api_key'], api_key_name: ['api_key_name'],
  checkin_hours: ['schedule', 'checkin_hours'], checkin_enabled: ['schedule', 'checkin_enabled'],
  travel_hours: ['schedule', 'travel_hours'], travel_enabled: ['schedule', 'travel_enabled'],
  activity_hours: ['schedule', 'activity_hours'], activity_enabled: ['schedule', 'activity_enabled'],
  keepalive_hours: ['schedule', 'keepalive_hours'], keepalive_enabled: ['schedule', 'keepalive_enabled'],
  blackcat_hours: ['schedule', 'blackcat_hours'], blackcat_enabled: ['schedule', 'blackcat_enabled'],
  balance_refresh_enabled: ['schedule', 'balance_refresh_enabled'], balance_refresh_minutes: ['schedule', 'balance_refresh_minutes'],
  probe_enabled: ['probe', 'enabled'], probe_interval_minutes: ['probe', 'interval_minutes'],
  probe_model: ['probe', 'model'], probe_prompt: ['probe', 'prompt'],
  probe_fail_threshold: ['probe', 'fail_threshold'],
  max_in_flight: ['pool', 'max_in_flight'], breaker_threshold: ['pool', 'breaker_threshold'],
  soft_rate: ['cooldown', 'soft_rate'], soft_rate_max: ['cooldown', 'soft_rate_max'],
  breaker_cooldown: ['pool', 'breaker_cooldown'], breaker_cooldown_max: ['pool', 'breaker_cooldown_max'],
  idle_weight_per_hour: ['pool', 'idle_weight_per_hour'], idle_weight_max: ['pool', 'idle_weight_max'],
  ttl: ['session_sticky', 'ttl'],
  timeout_seconds: ['upstream', 'timeout_seconds'], header_timeout_seconds: ['upstream', 'header_timeout_seconds'],
  idle_timeout_seconds: ['upstream', 'idle_timeout_seconds'], user_agent: ['upstream', 'user_agent'],
  prompt_mode: ['prompt', 'mode'], prompt_file: ['prompt', 'file'],
  sanitize_blacklist_fingerprints: ['features', 'sanitize_blacklist_fingerprints'],
  session_sticky_enabled: ['session_sticky', 'enabled'],
};
function dig(obj, path) { return path.reduce((o, k) => (o == null ? undefined : o[k]), obj); }
function put(obj, path, val) {
  let o = obj;
  for (let i = 0; i < path.length - 1; i++) { if (typeof o[path[i]] !== 'object' || o[path[i]] === null) o[path[i]] = {}; o = o[path[i]]; }
  o[path[path.length - 1]] = val;
}

async function loadConfig() {
  try {
    const d = await api('config');
    cfgLoaded = d.config;
    $('cfgPath').textContent = d.path || '';
    const f = $('cfgForm');
    for (const [name, path] of Object.entries(CFG_MAP)) {
      const el = f.elements[name];
      if (!el) continue;
      const v = dig(cfgLoaded, path);
      if (el.type === 'checkbox') el.checked = !!v;
      else if (Array.isArray(v)) el.value = v.join(', ');
      else el.value = v == null ? '' : v;
    }
    $('cfgNote').textContent = '';
    syncAllControls(); // 自定义外观跟着刚灌进去的值刷新（不重建，只改显示）
  } catch (e) { toast('读取配置失败：' + e.message, 'err'); }
}

function collectConfig() {
  const f = $('cfgForm'), out = {};
  for (const [name, path] of Object.entries(CFG_MAP)) {
    const el = f.elements[name];
    if (!el) continue;
    let v;
    if (el.type === 'checkbox') v = el.checked;
    else if (el.type === 'number') { v = el.value.trim() === '' ? undefined : Number(el.value); }
    else {
      const raw = el.value.trim();
      if (raw === '') v = undefined;
      else if (name.endsWith('_hours')) v = raw.split(/[,，\s]+/).filter(Boolean).map(Number);
      else v = raw;
    }
    if (v !== undefined) put(out, path, v);
  }
  // 额外密钥（api_keys）不在这里提交：它由「密钥」页专门管理，
  // 整份配置保存绝不碰它，免得"改个端口"顺手把密钥表洗掉。
  return out;
}
$('btnEye').onclick = () => {
  const el = $('cfgKey');
  const show = el.type === 'password';
  el.type = show ? 'text' : 'password';
  $('btnEye').textContent = show ? '隐藏' : '显示';
};
$('btnCfgReload').onclick = loadConfig;
$('btnProbeNow').onclick = async () => {
  const ok = await ask('现在对所有可用账号验活一次？\n\n每个账号会真的发起一次极小请求（默认走免费档 hy3，不花积分）。\n这轮不等下一个周期，结果看「账号」页的验活列。',
    { title: '立即验活', ok: '开始' });
  if (!ok) return;
  try {
    await api('probe_all', { method: 'POST' });
    toast('已开始验活，稍后看「账号」页的验活列', 'ok');
  } catch (e) { toast('触发失败：' + e.message, 'err'); }
};
$('cfgForm').onsubmit = async ev => {
  ev.preventDefault();
  const btn = $('btnCfgSave');
  btn.disabled = true; btn.textContent = '保存中…';
  try {
    const r = await api('config', { method: 'POST', body: JSON.stringify(collectConfig()) });
    const n = (r.restart_required || []).length;
    toast(n ? '配置已保存，其中 ' + n + ' 项需重启进程生效' : '配置已保存并立即生效', 'ok');
    // 密钥可能已改：本次会话沿用新值，避免下一次轮询被 401。
    const k = $('cfgKey').value.trim();
    if (k) localStorage.setItem(LS_KEY, k);
    loadConfig();
    loadOverview(true);
    fillCheckinSchedule();
  } catch (e) { toast('保存失败：' + e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '保存配置'; }
};

/* ── 添加账号 ─────────────────────────────────────────────────────── */
// 两步：先选域（国内版/国际版）→ 点「获取授权链接」。不再打开即用默认 CN 直发：
// 国际版走 workbuddy.ai 端点，落盘 auth.realm=global，登录后网关自动激活并领 trial；
// 选错域拿到的授权链接是另一个域的，账号类型也就错了——所以必须让用户显式选。
function openAdd() {
  $('addVeil').classList.add('on');
  // 重置到选域态：选域可见、加载/就绪/完成/错误全收，起始按钮亮起。
  $('addPick').hidden = false;
  $('addLoad').hidden = true; $('addReady').hidden = true;
  $('addDone').hidden = true; $('addErr').hidden = true;
  $('btnCopyUrl').hidden = true; $('btnOpenUrl').hidden = true;
  $('btnStartLogin').hidden = false; $('btnStartLogin').disabled = false;
  // radio 也要重置回默认域：不然「上次选了国际版 → 这次打开就想加个国内号」
  // 会沿用上次的勾选（radio 的 checked 是持久 DOM 状态，不随弹窗开关清零），
  // 用户没细看就点「获取授权链接」→ 拿到另一个域的链接、账号类型也跟着错。
  //
  // 默认值跟随顶部的**域视图开关**：你正在看国际服，打开弹窗就是国际版默认勾选
  //（少点一次、也不容易加错域）；开关在「全部」时回落国内版（与历史行为一致）。
  const want = panelRealm === 'global' ? 'global' : 'cn';
  const defRealm = document.querySelector('input[name="addRealm"][value="' + want + '"]');
  if (defRealm) defRealm.checked = true;
  stopPoll();
}
function startAddLogin() {
  const realm = (document.querySelector('input[name="addRealm"]:checked') || {}).value || 'cn';
  $('btnStartLogin').disabled = true;
  $('addLoad').hidden = false; $('addErr').hidden = true;
  api('login/start', { method: 'POST', body: JSON.stringify({ realm }) }).then(r => {
    loginState = r.state;
    $('addUrl').textContent = r.url;
    $('addPick').hidden = true; // 选域锁定（会话已按该域发起）
    $('addLoad').hidden = true; $('addReady').hidden = false;
    $('btnStartLogin').hidden = true;
    $('btnCopyUrl').hidden = false; $('btnOpenUrl').hidden = false;
    loginTimer = setInterval(pollLogin, 3000);
  }).catch(e => {
    $('addLoad').hidden = true;
    $('btnStartLogin').disabled = false;
    $('addErr').hidden = false;
    $('addErr').textContent = e.message;
  });
}
function stopPoll() { if (loginTimer) { clearInterval(loginTimer); loginTimer = null; } }
async function pollLogin() {
  if (!loginState) return;
  try {
    const r = await api('login/poll?state=' + encodeURIComponent(loginState));
    if (r.done) {
      stopPoll();
      $('addReady').hidden = true;
      $('addDone').hidden = false;
      $('addDone').textContent = '已添加 ' + (r.nickname || r.uid) + (r.realm === 'global' ? '（国际版）' : '') + (r.credits >= 0 ? ' · 积分 ' + r.credits : '') + '，账号已载入池中';
      setTimeout(() => { closeAdd(); loadOverview(true); }, 1600);
    }
  } catch (e) {
    stopPoll();
    $('addReady').hidden = true;
    $('addErr').hidden = false;
    $('addErr').textContent = e.message + '（关闭后重新添加）';
  }
}
function closeAdd() { stopPoll(); loginState = null; $('addVeil').classList.remove('on'); }
$('btnCloseAdd').onclick = closeAdd;
$('btnStartLogin').onclick = startAddLogin;
$('btnOpenUrl').onclick = () => open($('addUrl').textContent, '_blank');
$('btnCopyUrl').onclick = () => navigator.clipboard.writeText($('addUrl').textContent)
  .then(() => toast('链接已复制', 'ok'), () => toast('复制失败，请手动选择复制', 'err'));

/* ── 顶部动作 ─────────────────────────────────────────────────────── */
$('btnAdd').onclick = openAdd;
$('btnRefresh').onclick = async () => {
  const b = $('btnRefresh');
  b.disabled = true; b.textContent = '刷新中…';
  try {
    await api('balance_all', { method: 'POST' });
    await loadOverview(true);
    toast('余额已从上游刷新', 'ok');
  } catch (e) { toast('刷新失败：' + e.message, 'err'); await loadOverview(true); }
  finally { b.disabled = false; b.textContent = '刷新'; }
  if (view === 'logs') loadLogs();
  if (view === 'dashboard') { loadStats(); loadTrend(true); }
};
$('btnRestart').onclick = async () => {
  const okRestart = await ask('黑窗口不用关。源码有更新会先重新编译，大约几秒到十几秒，期间面板会短暂打不开。', { title: '重启服务？', ok: '重启' });
  if (!okRestart) return;
  const b = $('btnRestart');
  b.disabled = true; b.textContent = '重启中…';
  try {
    await api('restart', { method: 'POST' });
    toast('正在重启，请稍候…', 'ok');
    const started = Date.now();
    let sawDown = false;
    const ping = async () => {
      try {
        await api('overview');
        if (sawDown) { location.reload(); return; }
      } catch (e) { sawDown = true; }
      if (Date.now() - started > 90000) {
        b.disabled = false; b.textContent = '重启';
        toast('等了太久还没起来，请看黑窗口里的报错', 'err');
        return;
      }
      setTimeout(ping, 800);
    };
    setTimeout(ping, 600);
  } catch (e) {
    b.disabled = false; b.textContent = '重启';
    toast('重启失败：' + e.message, 'err');
  }
};

/* ── 轮询 ─────────────────────────────────────────────────────────── */
function refreshVisible() {
  // 标签页在后台时不刷：看不见的轮询只是白耗电、白占上游连接，回来时补一次就够了
  //（visibilitychange 里立即刷一次）。
  if (document.hidden) return;
  // accounts / checkin 的 loadOverview 内部已经刷过全局状态，别重复请求。
  if (view === 'accounts' || view === 'checkin') loadOverview(true);
  else {
    // 其余视图都单独刷一次全局状态：侧边栏连接指示、顶栏运行时长、别名映射不属于
    // 任何单一视图，只有 applyStatus 会更新它们。少了这句，停在仪表盘 / 模型与档位 /
    // 配置页时侧边栏会冻在首屏那一刻的状态（原本就是如此，顺手一起修）。
    loadStatus(true);
    if (view === 'dashboard') loadStats();
    else if (view === 'logs') loadLogs();
  }
  // 任务弹窗开着时也拉日志，好把一键完成进度画在弹窗里（不必切到运行日志）。
  if (view !== 'logs' && $('taskVeil') && $('taskVeil').classList.contains('on')) loadLogs();
}
function start() {
  loadOverview(true);
  if (refTimer) clearInterval(refTimer);
  refTimer = setInterval(refreshVisible, POLL_MS);
  checkAuthGate();
}
// 从后台切回前台时立刻补一次（否则最多要等一个 POLL_MS 才看到新数据，
// 而用户刚回到页面时正是最需要「现在是新的」的时刻）。
document.addEventListener('visibilitychange', () => { if (!document.hidden) refreshVisible(); });
async function checkAuthGate() {
  try { await api('overview'); }
  catch (e) { if (String(e.message).includes('密钥') || String(e.message).includes('api_key')) return; }
}
document.addEventListener('keydown', e => {
  if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
  const tag = ((e.target && e.target.tagName) || '').toLowerCase();
  if (tag === 'input' || tag === 'textarea' || tag === 'select' || (e.target && e.target.isContentEditable)) return;
  const box = view === 'logs' ? $('logSearch') : view === 'accounts' ? $('accSearch') : view === 'checkin' ? $('ckSearch') : null;
  if (!box) return;
  e.preventDefault();
  box.focus();
  if (box.select) box.select();
});

/* ── 积分任务 ─────────────────────────────────────────────────────── */
let taskUID = null, taskFilter = 'todo', taskList = [];

function taskKind(t) {
  if (t.claimed) return 'done';
  if (t.claimable) return 'claim';
  if (t.auto) return 'auto';
  if (t.needs_adaptation) return 'new';
  if (t.locked) return 'locked';
  return 'manual';
}
function visibleTasks(list) {
  return (list || []).filter(t => taskFilter === 'all' || taskKind(t) === taskFilter);
}
function renderTaskChips(list) {
  const el = $('taskChips');
  if (!el) return;
  const n = { all: list.length, claim: 0, auto: 0, new: 0, manual: 0, done: 0, locked: 0 };
  for (const t of list) n[taskKind(t)]++;
  el.innerHTML = [
    ['todo', '待办', n.claim + n.auto + n.manual],
    ['claim', '可领取', n.claim],
    ['auto', '可自动', n.auto],
    ['new', '新任务', n.new],
    ['manual', '需手动', n.manual],
    ['done', '已领取', n.done],
    ['all', '全部', n.all]
  ].map(([k, l, c]) =>
    '<button type="button" class="chip' + (taskFilter === k ? ' on' : '') + '" data-tf="' + k + '">' +
    l + '<span class="n">' + c + '</span></button>').join('');
}

function openTasks(uid) {
  taskUID = uid;
  taskFilter = 'todo';
  $('taskWho').textContent = uidName(uid);
  $('taskVeil').classList.add('on');
  $('btnTaskReload').hidden = false;
  paintTaskDlgProgress();
  loadTasks();
}
function closeTasks() { $('taskVeil').classList.remove('on'); taskUID = null; paintTaskDlgProgress(); }
$('btnCloseTask').onclick = closeTasks;
$('btnTaskReload').onclick = loadTasks;
$('taskChips').addEventListener('click', ev => {
  const b = ev.target.closest('[data-tf]');
  if (!b) return;
  taskFilter = b.dataset.tf;
  paintTaskTable();
});

$('btnTaskAcceptAll').onclick = async () => {
  if (!taskUID) return;
  const btn = $('btnTaskAcceptAll');
  btn.disabled = true; btn.textContent = '接受中…';
  try {
    const r = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks/accept_all', { method: 'POST' });
    const n = r.accepted || 0;
    if (r.failed && r.failed.length) {
      toast(`已接受 ${n} 个，${r.failed.length} 个被上游拒绝（可重试）`, 'err');
    } else {
      toast(n ? `已接受 ${n} 个任务` : (r.message || '所有任务均已接受'), 'ok');
    }
  } catch (e) { toast(e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '全部接受'; loadTasks(); }
};

$('btnTaskClaimAll').onclick = async () => {
  if (!taskUID) return;
  const btn = $('btnTaskClaimAll');
  btn.disabled = true; btn.textContent = '领取中…';
  try {
    const r = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks/claim_all', { method: 'POST' });
    const n = r.claimed || 0;
    if (n) toast('已领取 ' + n + ' 项' + (r.credit ? '，+' + r.credit + ' 分' : ''), 'ok');
    else toast(r.message || '没有可领取的奖励', 'ok');
    if ((r.failed || []).length) toast(r.failed.length + ' 项领取失败，可重试', 'err');
  } catch (e) { toast(e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '全部领取'; loadTasks(); loadOverview(true); }
};

$('btnTaskAutoAll').onclick = async () => {
  if (!taskUID) return;
  const btn = $('btnTaskAutoAll');
  const okOne = await ask('只对「当前这个账号」执行，不是全部账号。\n大约 1–2 分钟，进度会显示在本弹窗顶部。',
    { title: '给这个账号做任务？', ok: '开始' });
  if (!okOne) return;
  btn.disabled = true; btn.textContent = '执行中…';
  try {
    const r = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks/auto_all', { method: 'POST' });
    const okN = (r.results || []).filter(x => x.status === 'done').length;
    const skipN = (r.results || []).filter(x => x.status === 'skipped').length;
    const errN = (r.results || []).filter(x => x.status === 'error').length;
    toast(`执行完成：成功 ${okN} 项，跳过 ${skipN} 项${errN ? '，失败 ' + errN + ' 项' : ''}`, errN ? 'err' : 'ok');
  } catch (e) { toast(e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '一键完成可自动任务'; loadTasks(); }
};

function paintTaskTable() {
  const tb = $('taskBody');
  if (!taskList.length) return;
  let vis;
  if (taskFilter === 'todo') vis = taskList.filter(t => { const k = taskKind(t); return k === 'claim' || k === 'auto' || k === 'new' || k === 'manual'; });
  else vis = visibleTasks(taskList);
  if ($('taskCount')) $('taskCount').textContent = vis.length === taskList.length ? taskList.length + ' 项' : vis.length + ' / ' + taskList.length;
  if (!vis.length) {
    tb.innerHTML = '<tr><td colspan="6"><div class="empty"><div class="big">这一类没有任务</div>换个筛选看看</div></td></tr>';
    return;
  }
  tb.innerHTML = vis.map(t => {
    const cur = t.current ?? 0, tgt = t.target ?? 0;
    const pct = tgt > 0 ? Math.min(100, Math.round(cur / tgt * 100)) : (t.claimed || t.claimable ? 100 : 0);
    const prog = tgt ? cur + ' / ' + tgt : (tgt === 0 && cur > 0 ? String(cur) : '—');
    const barCls = t.claimed || t.claimable ? 'ok' : (pct > 0 ? 'warn' : '');
    const parts = [];
    if (t.credit) parts.push('+' + t.credit + ' 分');
    if (t.energy) parts.push('+' + t.energy + ' 能');
    if (t.reward_buddy) parts.push('Buddy');
    const reward = parts.length ? parts.join(' ') : '—';
    const badge = t.claimed ? '<span class="tag ok">已领取</span>'
      : t.claimable ? '<span class="tag warn">可领取</span>'
      : t.auto ? '<span class="tag ok">可自动</span>'
      : t.needs_adaptation ? '<span class="tag warn">新任务 · 待适配</span>'
      : t.locked ? '<span class="tag mute">未解锁</span>'
      : t.accept_status === 'accepted' ? '<span class="tag mute">进行中</span>'
      : '<span class="tag mute">需手动</span>';
    const acted = t.claimed || t.locked ? ''
      : t.claimable ? '<button class="xs primary" data-t="claim" data-c="' + esc(t.task_code) + '">领取</button>'
      : t.auto ? '<button class="xs primary" data-t="auto" data-c="' + esc(t.task_code) + '" title="' + esc(t.auto_desc || '') + '">一键完成</button>'
      : t.accept_status === 'accepted' ? ''
      : '<button class="xs" data-t="accept" data-c="' + esc(t.task_code) + '">接受</button>';
    const tip = [t.title, t.task_desc || t.description, t.auto_desc,
      t.needs_adaptation ? '官方新任务：当前可接受、领取，自动完成待适配' : '',
      t.jump_url ? '跳转：' + t.jump_url : ''].filter(Boolean).join('\n');
    return '<tr title="' + esc(tip) + '"><td class="mark" aria-hidden="true"><i></i></td>' +
      '<td class="who"><div class="nm">' + esc(t.title || t.task_code) + '</div><div class="id">' + esc(t.task_code) + (t.tag ? ' · ' + esc(t.tag) : '') + '</div></td>' +
      '<td class="tprog"><div class="n">' + esc(prog) + '</div><div class="bar"><i class="' + barCls + '" style="width:' + pct + '%"></i></div></td>' +
      '<td class="num">' + esc(reward) + '</td>' +
      '<td>' + badge + '</td>' +
      '<td class="acts"><div class="btn-row">' + acted + '</div></td></tr>';
  }).join('');
}

async function loadTasks() {
  if (!taskUID) return;
  const st = $('taskState'), tb = $('taskTable'), bar = $('taskToolbar');
  st.hidden = false;
  st.className = 'state';
  st.innerHTML = '<span class="dots">查询中</span>';
  tb.hidden = true;
  if (bar) bar.hidden = true;
  try {
    const d = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks');
    const list = d.tasks || [];
    if (!list.length) {
      st.className = 'state';
      st.textContent = '该账号暂无任务（以官方列表为准）';
      return;
    }
    list.sort((a, b) => (a.claimed - b.claimed) || (b.claimable - a.claimable) ||
      (Number(b.needs_adaptation) - Number(a.needs_adaptation)) || String(a.task_code).localeCompare(String(b.task_code)));
    taskList = list;
    renderTaskChips(list);
    paintTaskTable();
    st.hidden = true;
    tb.hidden = false;
    if (bar) bar.hidden = false;
  } catch (e) {
    st.className = 'state err';
    st.textContent = e.message;
  }
}

$('taskBody').addEventListener('click', async ev => {
  const b = ev.target.closest('button[data-t]');
  if (!b || !taskUID) return;
  const kind = b.dataset.t, code = b.dataset.c;
  b.disabled = true;
  try {
    if (kind === 'auto') {
      // 一键完成：后端执行动作 → 回读进度 → 汇报（耗时可到分钟级，含真实对话）
      b.textContent = '执行中…';
      const r = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks/auto', {
        method: 'POST', body: JSON.stringify({ task_code: code })
      });
      if (r.skipped) {
        toast(r.message || '已跳过', 'ok');
      } else {
        const advanced = r.progress_before !== r.progress_after;
        let msg = r.message || '已执行';
        if (r.progress_after) msg += `（进度 ${r.progress_before} → ${r.progress_after}）`;
        if (r.claimed) msg += '，奖励已自动到账';
        else if (r.claimable) msg += r.claim_error ? '，可点「领取」重试' : '';
        else if (r.attempt && !advanced) msg += '；进度未动，该任务可能需要官方客户端';
        toast(msg, (r.claimed || advanced) ? 'ok' : 'err');
      }
      loadOverview(true);
    } else {
      const path = 'accounts/' + encodeURIComponent(taskUID) + '/tasks/' + (kind === 'claim' ? 'claim' : 'accept');
      const body = kind === 'claim' ? { task_code: code } : { task_codes: [code] };
      await api(path, { method: 'POST', body: JSON.stringify(body) });
      toast(kind === 'claim' ? '已领取奖励' : '已接受任务', 'ok');
      if (kind === 'claim') loadOverview(true);
    }
  } catch (e) { toast(e.message, 'err'); }
  finally { loadTasks(); }
});

function syncAllControls() {
  document.querySelectorAll('select').forEach(syncSelect);
  document.querySelectorAll('input[type=checkbox]').forEach(cb => {
    if (!cb.dataset.swDone) return;
    const sw = cb.nextElementSibling;
    if (sw && sw.classList.contains('tgl')) {
      sw.classList.toggle('on', !!cb.checked);
      sw.setAttribute('aria-checked', cb.checked ? 'true' : 'false');
    }
  });
}

/* ── 收尾：把原生控件套上自定义外观 ────────────────────────────────
   必须放在**所有业务初始化之后**：配置表单要先按原生控件把自己灌满
    （loadConfig 往 el.checked / el.value 写），再套外观去读最终状态。
   反过来的话，外观会停在初始态、和真实值不一致。 */

/* ── 启动入口 ──────────────────────────────────────────────────────
   ⚠️ 必须在**文件末尾**调用，不能提到前面去。

   go() 会按 hash 触发 loadStats / loadOverview / loadUsage / loadLogs / loadKeys …
   而这些函数体里读的 let/const（useDay、useData、LOG_H_KEY、fltOpenCat、taskUID …）
   声明在本文件后半段。提前调用就会命中 JavaScript 的「暂时性死区」：
       ReferenceError: Cannot access 'useDay' before initialization
   后果是首屏**当场崩在那一行**：视图空白、轮询没起来，整页像卡死。
   而手动点一次导航就恢复正常——那时整个脚本早已求值完毕。

   踩过一次：原先写在 go() 定义的正下方（约 1141 行），导致「请求审计」与
   「运行日志」两个视图**刷新必卡、再点一下才好**，排查了很久才定位到。

   start() 一起挪过来：它启动的 5 秒轮询会走 refreshVisible → 各视图的 load*，
   同样不能早于这些声明。 */
/* ── 任务中心：开学季 + 全账号扫描/队列 ──────────────────────────── */
const SCHOOL_META = [
  ['share_invite', '分享'],
  ['desktop_chat_1_time', '桌面'],
  ['chat_3_times', '对话×3'],
  ['expert_use', '专家'],
  ['task_student_verify', '认证'],
];
// 开学季任务单元：✓ 已领（绿）｜◐ x/y 进行中（琥珀）｜○ 未做（灰）
function staskHTML(t) {
  if (!t) return '<span class="stask todo"><span class="mark">·</span>—</span>';
  if (t.status === 'claimed') return '<span class="stask ok"><span class="mark">✓</span>已领</span>';
  if (t.status === 'completed') return '<span class="stask warn"><span class="mark">◆</span>可领</span>';
  if (t.status === 'in_progress') {
    const fr = t.target_count ? '<span class="fr">' + t.progress + '/' + t.target_count + '</span>' : '';
    return '<span class="stask warn"><span class="mark">◐</span>' + fr + '</span>';
  }
  return '<span class="stask todo"><span class="mark">○</span>未做</span>';
}
const LUCK_SVG = '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4"><path d="M3.2 5.2 5 1.8l3 2.4 3-2.4 1.8 3.4-1.4 2.6 1.4 2.6-3.4 2.2H6l-3.4-2.2 1.4-2.6z" opacity=".9"/><circle cx="8" cy="9" r="1.1" fill="currentColor" stroke="none"/></svg>';
async function loadSchoolStatus(quiet) {
  const st = $('schoolState'), list = $('schoolList');
  if (!quiet) { st.hidden = false; st.className = 'state'; st.innerHTML = '<span class="dots">查询中</span>'; list.innerHTML = ''; }
  try {
    const d = await api('school/status');
    const arr = d.accounts || [];
    if (!arr.length) {
      st.hidden = false; st.className = 'state'; st.textContent = '暂无可用账号';
      list.innerHTML = ''; return;
    }
    let allDone = 0;
    const head = '<div class="shead"><div class="who">账号</div><div class="stasks">' +
      SCHOOL_META.map(([, name]) => '<span>' + esc(name) + '</span>').join('') +
      '</div><div class="luck">剩余抽奖</div></div>';
    list.innerHTML = head + arr.map(v => {
      const by = {};
      (v.tasks || []).forEach(t => by[t.task_code] = t);
      const cells = SCHOOL_META.map(([code]) => {
        const t = by[code];
        const html = code === 'task_student_verify'
          ? '<span class="stask todo"><span class="mark">—</span>不做</span>'
          : staskHTML(t);
        return '<span title="' + esc(SCHOOL_TITLES[code] || code) + '">' + html + '</span>';
      }).join('');
      const done = SCHOOL_META.filter(([code]) => code !== 'task_student_verify' && by[code] && by[code].status === 'claimed').length;
      allDone += done === 4 ? 1 : 0;
      return '<div class="srow">' +
        '<div class="who"><div class="nm" title="' + esc(v.nickname || '') + '">' + esc(v.nickname || '未命名') + '</div><div class="id">' + esc(v.uid) + '</div></div>' +
        '<div class="stasks">' + cells + '</div>' +
        '<div class="luck" title="剩余抽奖次数">' + LUCK_SVG + (v.chances == null ? '—' : v.chances) + '</div>' +
        (v.error ? '<div class="err">' + esc(v.error) + '</div>' : '') +
        '</div>';
    }).join('');
    $('schoolSummary').textContent = allDone === arr.length ? '今日全部完成 🎉' : allDone + '/' + arr.length + ' 个账号今日全部完成';
    st.hidden = true;
  } catch (e) {
    st.hidden = false; st.className = 'state err'; st.textContent = e.message;
  }
}
const SCHOOL_TITLES = {
  share_invite: '分享活动 +100c', desktop_chat_1_time: '桌面端体验 +100c（单次）',
  chat_3_times: '和 AI 对话 3 次 +50c', expert_use: '召唤开学季专家 +50c',
  task_student_verify: '学生认证 +100c（需真实认证，不做）',
};
$('btnSchoolRefresh').onclick = () => loadSchoolStatus(false);
$('btnSchoolRunAll').onclick = async () => {
  if (!(await ask('将对全部账号执行开学季闭环（分享/桌面/对话/专家 + 抽奖），约 1-2 分钟。', { title: '执行开学季闭环', ok: '开始' }))) return;
  try {
    await api('school/run_all', { method: 'POST' });
    toast('开学季闭环已开始，结果看任务日志', 'ok');
    setTimeout(() => loadSchoolStatus(true), 15000);
  } catch (e) { toast(e.message, 'err'); }
};

/* ── 精简 QR 编码器（券码二维码用）────────────────────────────────────
   规格子集：byte 模式、ECC L、版本 1-5（全部单纠错块，免块交织）、固定掩码 0。
   完整性：规范允许任选掩码（解码器按格式信息位自行去掩码），固定掩码不影响
   可扫描性；已用 python qrcode 库对多输入多版本做逐像素交叉验证（强制 byte
   模式 + mask 0，5/5 全部 diff=0）。面板 CSP 只允许 self，外链 QR 服务不可用。 */
// qr_gen.js —— 精简 QR 编码器（浏览器用 + node 可跑交叉验证）
// 规格子集：byte 模式、ECC L、版本 1-5（全部单纠错块，免块交织）、固定掩码 0。
// 完整性说明：规范允许编码器任选掩码（解码器按格式信息位自行去掩码），
// 固定掩码不影响可扫描性；券码为短文本，v1-5（26 字节起）绰绰有余。

// GF(256) 对数/指数表（本原多项式 0x11d）
const QR_EXP = new Array(512), QR_LOG = new Array(256);
(() => {
  let x = 1;
  for (let i = 0; i < 255; i++) { QR_EXP[i] = x; QR_LOG[x] = i; x <<= 1; if (x & 0x100) x ^= 0x11d; }
  for (let i = 255; i < 512; i++) QR_EXP[i] = QR_EXP[i - 255];
})();
const gmul = (a, b) => (a && b) ? QR_EXP[QR_LOG[a] + QR_LOG[b]] : 0;

// 各版本参数（下标 = 版本-1）：[数据码字数, 纠错码字数]，ECC L 单块
const QR_V = [[19, 7], [34, 10], [55, 15], [80, 20], [108, 26]];
// 对齐图案中心坐标（v2+；与定位图案重叠的位置在放置时跳过）
const QR_ALIGN = [[], [6, 18], [6, 22], [6, 26], [6, 30]];
const QR_MASK = (r, c) => (r + c) % 2 === 0; // 掩码模式 0

// 生成多项式（最高次系数在前，g[0] 恒为 1）
function qrGenPoly(deg) {
  let g = [1];
  for (let i = 0; i < deg; i++) {
    const a = QR_EXP[i], ng = new Array(g.length + 1).fill(0);
    ng[0] = g[0];
    for (let j = 1; j < g.length; j++) ng[j] = g[j] ^ gmul(a, g[j - 1]);
    ng[g.length] = gmul(a, g[g.length - 1]);
    g = ng;
  }
  return g;
}

// Reed-Solomon 求余（综合除法），返回 deg 个纠错码字
function rsRem(data, deg) {
  const g = qrGenPoly(deg);
  const res = data.concat(new Array(deg).fill(0));
  for (let i = 0; i < data.length; i++) {
    const f = res[i];
    if (f) for (let j = 0; j < g.length; j++) res[i + j] ^= gmul(g[j], f);
  }
  return res.slice(data.length);
}

// 文本 → 码字流（byte 模式：0100 + 8 位计数 + 数据 + 终止符 + 0xEC/0x11 填充）
function qrDataCodewords(text, dataCap) {
  const bytes = Array.from(new TextEncoder().encode(text));
  const bits = [];
  const push = (val, n) => { for (let i = n - 1; i >= 0; i--) bits.push((val >> i) & 1); };
  push(4, 4);            // byte 模式
  push(bytes.length, 8); // v1-9 计数 8 位
  for (const b of bytes) push(b, 8);
  const cap = dataCap * 8;
  push(0, Math.min(4, cap - bits.length));   // 终止符
  while (bits.length % 8) bits.push(0);
  const out = [];
  for (let i = 0; i < bits.length; i += 8) {
    let v = 0; for (const b of bits.slice(i, i + 8)) v = (v << 1) | b;
    out.push(v);
  }
  for (let p = 0; out.length < dataCap; p ^= 1) out.push(p ? 0x11 : 0xEC);
  return out;
}

// 主入口：text → 布尔矩阵（true=深色模块）
function qrMatrix(text) {
  const bytes = Array.from(new TextEncoder().encode(text));
  // 版本选择：需求 ≈ 2 码字头 + 文本长度，取首个放得下的版本
  let ver = 0;
  for (let v = 0; v < QR_V.length; v++) { if (bytes.length + 2 <= QR_V[v][0]) { ver = v + 1; break; } }
  if (!ver) throw new Error('QR: text too long (>' + QR_V[4][0] + ' bytes)');
  const [dataCap, ecCap] = QR_V[ver - 1];
  const n = 17 + 4 * ver;

  const M = Array.from({ length: n }, () => new Array(n).fill(false));
  const F = Array.from({ length: n }, () => new Array(n).fill(false)); // 功能模块占位

  const setF = (r, c, v) => { M[r][c] = v; F[r][c] = true; };
  // 定位图案 + 分隔带
  const finder = (r0, c0) => {
    for (let r = -1; r <= 7; r++) for (let c = -1; c <= 7; c++) {
      const rr = r0 + r, cc = c0 + c;
      if (rr < 0 || cc < 0 || rr >= n || cc >= n) continue;
      const dark = r >= 0 && r <= 6 && c >= 0 && c <= 6 && (r === 0 || r === 6 || c === 0 || c === 6 || (r >= 2 && r <= 4 && c >= 2 && c <= 4));
      setF(rr, cc, dark);
    }
  };
  finder(0, 0); finder(0, n - 7); finder(n - 7, 0);
  // 校正图形（仅贯穿两定位图案之间：8..n-9，不得覆盖定位图案本体）
  for (let r = 8; r <= n - 9; r++) setF(r, 6, r % 2 === 0);
  for (let c = 8; c <= n - 9; c++) setF(6, c, c % 2 === 0);
  // 对齐图案（v2+，跳过与定位重叠处）
  const align = QR_ALIGN[ver - 1] || [];
  for (const ar of align) for (const ac of align) {
    if (F[ar][ac]) continue;
    for (let r = -2; r <= 2; r++) for (let c = -2; c <= 2; c++)
      setF(ar + r, ac + c, Math.max(Math.abs(r), Math.abs(c)) !== 1);
  }
  // 暗模块 + 格式信息（ECC L=01，掩码 0）——BCH(15,5) + 0x5412 异或。
  // 位序遵循规范（与 python qrcode 逐位对齐验证）：bit i 从 LSB 起数，
  // 副本一走左上角 L 形、副本二走右下 L 形。
  let fmt = (1 << 3) | 0; // L<<3 | mask
  let rem = fmt << 10;
  for (let i = 14; i >= 10; i--) if ((rem >> i) & 1) rem ^= 0x537 << (i - 10);
  fmt = ((fmt << 10) | rem) ^ 0x5412; // 15 位
  const fb = i => (fmt >> i) & 1;
  // 副本一（左上）：位 0..5 → (i,8)；6 → (7,8)；7 → (8,8)
  for (let i = 0; i <= 5; i++) setF(i, 8, !!fb(i));
  setF(7, 8, !!fb(6)); setF(8, 8, !!fb(7));
  // 副本一续 + 副本二（右下）：位 8..14 → (n-15+i, 8)；位 0..7 → (8, n-1-i)；8 → (8,7)；9..14 → (8,14-i)
  for (let i = 8; i <= 14; i++) setF(n - 15 + i, 8, !!fb(i));
  for (let i = 0; i <= 7; i++) setF(8, n - 1 - i, !!fb(i));
  setF(8, 7, !!fb(8));
  for (let i = 9; i <= 14; i++) setF(8, 14 - i, !!fb(i));
  // 暗模块（恒为深色，位于副本一垂直段末端）
  setF(n - 8, 8, true);


  // 数据码字 + 纠错码字 → 位流
  const dcw = qrDataCodewords(text, dataCap);
  const cw = dcw.concat(rsRem(dcw, ecCap));
  const bits = [];
  for (const b of cw) for (let i = 7; i >= 0; i--) bits.push((b >> i) & 1);

  // 蛇形放置（成对列，从右向左，跳过第 6 列），写数据时直接异或掩码
  let bi = 0, up = true;
  for (let x = n - 1; x > 0; x -= 2) {
    if (x === 6) x--;
    for (let i = 0; i < n; i++) {
      const r = up ? n - 1 - i : i;
      for (const c of [x, x - 1]) {
        if (F[r][c]) continue;
        const bit = bi < bits.length ? bits[bi++] : 0;
        M[r][c] = bit ? !QR_MASK(r, c) : QR_MASK(r, c);
      }
    }
    up = !up;
  }
  return M;
}

// 矩阵 → SVG（quiet zone 4 模块）
function qrSVG(M, px) {
  const n = M.length, q = 4, total = n + q * 2;
  let s = '<svg viewBox="0 0 ' + total + ' ' + total + '" width="' + px + '" height="' + px + '" shape-rendering="crispEdges" role="img" style="background:#fff">';
  for (let r = 0; r < n; r++) for (let c = 0; c < n; c++)
    if (M[r][c]) s += '<rect x="' + (c + q) + '" y="' + (r + q) + '" width="1" height="1"/>';
  return s + '</svg>';
}

/* ── 开学季券码查询（弹窗，仿活动页 #/prizes?tab=vouchers）──────────── */
/* copyText：clipboard API 只在 secure context（https/localhost）可用，
   远程 http 面板会拿不到 navigator.clipboard → 降级 execCommand。 */
function copyToClipboard(text) {
  if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
  return new Promise((resolve, reject) => {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.cssText = 'position:fixed;opacity:0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy') ? resolve() : reject(new Error('copy failed')); }
    catch (e) { reject(e); }
    finally { ta.remove(); }
  });
}

function vcCard(v) {
  const expired = v.valid_to && new Date(v.valid_to) < new Date();
  return '<div class="vc' + (expired ? ' expired' : '') + '">' +
    '<div class="hd"><span class="nm">' + esc(v.prize_name || v.sku_code || '券') + '</span>' +
    (expired ? '<span class="tag bad">已过期</span>' : '<span class="tag ok">可使用</span>') + '</div>' +
    '<div class="meta">' +
      (v.valid_to ? '有效期至 ' + esc(v.valid_to) : '长期有效') +
      (v.granted_at ? ' · ' + esc(v.granted_at.slice(0, 10)) + ' 抽中' : '') +
    '</div>' +
    '<div class="sep"></div>' +
    '<div class="ft"><span class="lab">券码</span><code>' + esc(v.code || '-') + '</code>' +
    '<span class="acts">' +
      (v.code ? '<button class="xs ghost" data-qr="' + esc(v.code) + '">二维码</button>' : '') +
      '<button class="xs ghost" data-copy="' + esc(v.code || '') + '">复制</button>' +
    '</span></div>' +
    '</div>';
}

async function loadSchoolVouchers() {
  const body = $('vcBody');
  $('vcVeil').classList.add('on');
  body.innerHTML = '<div class="state"><span class="dots">查询中</span></div>';
  $('vcNote').textContent = '';
  try {
    const d = await api('school/vouchers');
    const arr = d.accounts || [];
    const ok = arr.filter(a => !a.error);
    const total = ok.reduce((n, a) => n + (a.vouchers || []).length, 0);
    body.innerHTML = ok.filter(a => (a.vouchers || []).length).map(a =>
      '<div class="vc-acct"><span class="nm">' + esc(pkNick(a.nickname) || (a.uid || '').slice(0, 8)) + '</span>' +
      '<span>' + a.vouchers.length + ' 张</span></div>' +
      a.vouchers.map(vcCard).join('')
    ).join('') || '<div class="empty"><div class="big">🎟️</div>还没有抽到券</div>';
    $('vcNote').textContent = total ? total + ' 张券 · ' + ok.filter(a => !(a.vouchers || []).length).length + ' 个账号未抽中' : '';
    const errs = arr.filter(a => a.error);
    if (errs.length) {
      body.insertAdjacentHTML('beforeend', '<div class="note" style="color:var(--warn);margin-top:8px">查询失败：' +
        errs.map(a => esc(pkNick(a.nickname) || (a.uid || '').slice(0, 8)) + '（' + esc(a.error) + '）').join('、') + '</div>');
    }
    body.querySelectorAll('button[data-copy]').forEach(b => b.onclick = async () => {
      try { await copyToClipboard(b.dataset.copy); toast('券码已复制', 'ok'); }
      catch (e) { toast('复制失败，请手动选择券码', 'err'); }
    });
    // 二维码：券码本体编码为 QR（到店出示扫描），点击切换显示/隐藏
    body.querySelectorAll('button[data-qr]').forEach(b => b.onclick = () => {
      const card = b.closest('.vc');
      const old = card.querySelector('.vc-qr');
      if (old) { old.remove(); return; }
      const box = document.createElement('div');
      box.className = 'vc-qr';
      try { box.innerHTML = qrSVG(qrMatrix(b.dataset.qr), 148); }
      catch (e) { box.innerHTML = '<span class="note">二维码生成失败：' + esc(e.message) + '</span>'; }
      card.appendChild(box);
    });
  } catch (e) {
    body.innerHTML = '<div class="state err">' + esc(e.message) + '</div>';
  }
}
$('btnSchoolVouchers').onclick = loadSchoolVouchers;
$('btnVcClose').onclick = () => $('vcVeil').classList.remove('on');
$('btnVcRefresh').onclick = loadSchoolVouchers;

/* 成长任务队列。lastQueueSeq 记录本页启动过的队列代次：执行结束后的残留 items
   （running=false 但 seq 停在旧值）不再回写视图——否则扫描结果 3 秒后被上一轮
   队列状态覆盖。 */
let queueTimer = null, lastQueueSeq = 0;
const GROWTH_TITLES = {}; // code → 展示名（扫描时从任务列表带出）
$('btnScanAll').onclick = async () => {
  const b = $('btnScanAll');
  b.disabled = true; b.textContent = '扫描中…';
  try {
    const d = await api('tasks/scan_all', { method: 'POST' });
    renderQueue(groupItems(d), null, '没有待办任务 🎉', '全部账号的成长任务与开学季活动都已完成，明日再来。');
  } catch (e) { toast(e.message, 'err'); }
  finally { b.disabled = false; b.textContent = '扫描待办'; }
};
$('btnRunQueue').onclick = async () => {
  const conc = Number($('qcConc').value) || 1;
  if (!(await ask('扫描全部账号待办并排队执行（账号并发 ' + conc + '，账号内串行）。含真实对话的任务耗时较长。', { title: '执行全部待办', ok: '开始' }))) return;
  const b = $('btnRunQueue');
  b.disabled = true; b.textContent = '启动中…';
  try {
    const r = await api('tasks/run_queue', { method: 'POST', body: JSON.stringify({ concurrency: conc }) });
    if (!r.started) { toast(r.message || '没有待办任务', 'ok'); return; }
    lastQueueSeq = r.seq || 0;
    toast('队列已启动：' + r.total + ' 项（并发 ' + conc + '）', 'ok');
    startQueuePolling();
  } catch (e) { toast(e.message, 'err'); }
  finally { b.disabled = false; b.textContent = '执行全部待办'; }
};
// 扫描结果 → 分组条目（无执行状态）
function groupItems(d) {
  const groups = [];
  for (const a of (d.accounts || [])) {
    const rows = [];
    for (const t of (a.growth || [])) {
      GROWTH_TITLES[t.task_code] = t.title || t.task_code;
      rows.push({ kind: 'growth', code: t.task_code, prog: t.target ? t.current + '/' + t.target : '—', status: 'scan' });
    }
    for (const t of (a.school || [])) {
      if (t.task_code === 'task_student_verify') continue; // 需真实认证，永不出现在待办
      rows.push({ kind: 'school', code: t.task_code, prog: t.target_count ? t.progress + '/' + t.target_count : '—', status: 'scan' });
    }
    if (rows.length) groups.push({ uid: a.uid, nick: a.nickname, rows });
  }
  return groups;
}
const ST_WORDS = { done: '完成', running: '执行中', error: '失败', skipped: '跳过', pending: '排队', scan: '待执行' };
function qrowHTML(it) {
  const isSchool = it.kind === 'school';
  const title = isSchool ? '开学季闭环' : (GROWTH_TITLES[it.code] || it.code);
  const dotCls = it.status === 'scan' ? 'wait' : it.status === 'running' ? 'run' : it.status === 'error' ? 'err' : it.status === 'skipped' ? 'skip' : it.status === 'done' ? 'done' : 'wait';
  const stWord = it.status === 'scan' ? '待执行' : (ST_WORDS[it.status] || it.status);
  return '<div class="qrow" title="' + esc(it.message || '') + '">' +
    '<span class="code">' + esc(it.code) + '</span>' +
    '<span class="name"><span class="t">' + esc(title) + '</span>' + (isSchool ? '<span class="tag mute">开学季</span>' : '') + '</span>' +
    '<span class="prog">' + esc(it.prog || '') + '</span>' +
    '<span class="st"><span class="qdot ' + dotCls + '"></span>' + stWord + '</span>' +
    '<span class="msg">' + esc(it.message || '') + '</span>' +
    '</div>';
}
function renderQueue(groups, progress, emptyTitle, emptyDesc) {
  const empty = $('tcEmpty'), list = $('qcList');
  if (!groups.length) {
    empty.style.display = '';
    if (emptyTitle) empty.querySelector('.t').textContent = emptyTitle;
    if (emptyDesc) empty.querySelector('.d').textContent = emptyDesc;
    list.innerHTML = '';
    $('qProg').hidden = true; $('qcSummary').textContent = '';
    return;
  }
  empty.style.display = 'none';
  empty.style.display = 'none';
  let total = 0;
  list.innerHTML = groups.map(g => {
    total += g.rows.length;
    return '<div class="qgroup"><header><span class="nm">' + esc(g.nick || g.uid.slice(0, 12)) + '</span><span class="cnt">' + g.rows.length + ' 项待办</span></header>' +
      g.rows.map(qrowHTML).join('') + '</div>';
  }).join('');
  $('qcSummary').textContent = total + ' 项';
  updateProgress(progress);
}
function updateProgress(q) {
  if (!q || !q.items) { $('qProg').hidden = true; return; }
  const total = q.items.length;
  const done = q.items.filter(it => it.status === 'done' || it.status === 'error' || it.status === 'skipped').length;
  $('qProg').hidden = false;
  $('qBarFill').style.width = (total ? Math.round(done / total * 100) : 0) + '%';
  $('qProgText').textContent = (q.running ? '执行中 ' : '已结束 ') + done + ' / ' + total;
}
// 队列状态 → 分组（执行时轮询）
function groupsFromQueue(items) {
  const by = new Map();
  for (const it of items) {
    if (!by.has(it.uid)) by.set(it.uid, { uid: it.uid, nick: it.nickname, rows: [] });
    by.get(it.uid).rows.push({
      kind: it.kind, code: it.code,
      prog: it.kind === 'school' ? '—' : '',
      status: it.status, message: it.message,
    });
  }
  return Array.from(by.values());
}
async function pollQueueOnce() {
  try {
    const q = await api('tasks/queue');
    if (!q.started) return;
    // 只渲染本页启动过的那轮队列（q.running 时也要同代次——刷新页面后不再接管旧队列）。
    if (lastQueueSeq && q.seq !== lastQueueSeq) return;
    renderQueue(groupsFromQueue(q.items || []), q);
  } catch (e) { /* 静默 */ }
}
function startQueuePolling() {
  if (queueTimer) clearInterval(queueTimer);
  queueTimer = setInterval(async () => {
    await pollQueueOnce();
    try {
      const q = await api('tasks/queue');
      if (!q.running) {
        clearInterval(queueTimer); queueTimer = null;
        toast('任务队列执行结束', 'ok');
        loadSchoolStatus(true);
      }
    } catch (e) { /* 忽略 */ }
  }, 3000);
}

/* ── 积分构成 ─────────────────────────────────────────────────────── */
/* 一个账号的余额是若干积分包之和。包按来源命名（「国内运营裂变包」「拉新权益包」
   「个人体验版」…），面额从 6 到 1500 不等，且**按次发放**。所以两个任务完成度
   完全一致的账号，余额可能差上千——差别只在包里。这里把逐包明细摊开，并给每个
   包名一个稳定配色，跨账号对比时同色即同类。 */

// pkData 最近一次积分构成的响应。数据本身不分域，所以切域只需换一批账号重画，
// 不必重查上游（那是逐账号查询，面板里最慢的一条路）。
let pkData = null;
const PK_COLORS = ['#4f8cff', '#25b08b', '#e8a33d', '#c96bd6', '#e2607a',
                   '#5aa9e6', '#8fbf3f', '#b58b5a', '#7d8fa8', '#d4785c'];

function pkColor(i) { return PK_COLORS[i % PK_COLORS.length]; }

/* pkBySource 把包按名称归并，得到「来源 → 面额/余额/个数」。这是对比的关键视图：
   两个号的差异一定体现在某几个来源的面额上。 */
function pkBySource(packs) {
  const m = new Map();
  for (const p of packs) {
    // 分组键用 code + name，而不是只 name：上游给「首登赠送」和普通活动包用了
    // **同一个 PackageName 和同一个 PackageCode**，只按 name 会把两类混成一类，
    // 那正是当初「两个号为何差 1500」看不出来的原因。这里至少把 code 带进键里，
    // 并在卡片上显示最早的发放时间。
    const k = (p.package_code || '') + '|' + (p.name || '(未命名)');
    const e = m.get(k) || {
      key: k, name: p.name || '(未命名)', code: p.package_code || '',
      n: 0, remain: 0, size: 0, used: 0, minEnd: '', minCreated: '',
    };
    e.n += 1;
    e.remain += Number(p.remain || 0);
    e.size += Number(p.size || 0);
    e.used += Number(p.used || 0);
    const t = (p.end_time || '').slice(0, 10);
    if (t && (!e.minEnd || t < e.minEnd)) e.minEnd = t;
    const c = (p.created_at || '').slice(0, 10);
    if (c && (!e.minCreated || c < e.minCreated)) e.minCreated = c;
    m.set(k, e);
  }
  return [...m.values()].sort((a, b) => b.size - a.size);
}

// pkTitle 卡片标题：昵称可用则显示昵称，否则显示 uid8。
//
// 为什么要这个：上游 auth 文件里的 nickname 并非总是手机号——实测有账号是空串、
// 甚至有 "ok" 这种误填值（OAuth 流程里昵称字段缺省/被别的值占了）。直接显示会出现
// 「卡片没名字」或「名字叫 ok」，比 uid 还难认人。
//
// 规则：昵称非空、且不是常见的占位垃圾值 → 用它（副标题给 uid8 供核对）；
// 否则主标题用 uid8，副标题留空（不再附注原始昵称——那是调试信息，不是给人看的）。
//
// 又一个坑：**trim() 去不掉控制字符**。实测有个账号的昵称是 4 个 U+007F(DEL)，
// trim 后长度仍是 4，正则也匹配不上——卡片标题就渲染成一片空白。
// 所以先剥掉所有不可见字符（C0/C1 控制符、零宽、BOM），再判断。
const PK_BAD_NICKS = /^(ok|test|null|undefined|none|nil|-+)$/i;
function pkNick(s) {
  return String(s == null ? '' : s)
    .replace(/[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u2028\u2029\ufeff]/g, '')
    .trim();
}
function pkTitle(a) {
  const uid8 = (a.uid || '').slice(0, 8);
  const n = pkNick(a && a.nickname);
  if (n && !PK_BAD_NICKS.test(n)) {
    return { main: n, sub: uid8 };
  }
  return { main: uid8, sub: '' };
}

function renderPackages(d) {
  // 域开关生效时只画该域的号（明细与汇总一起过滤；空态说清是"该域没有号"而不是"没有账号"）。
  const all = (d.accounts || []);
  const list = all.filter(a => panelRealm === 'all' || realmOf(a) === panelRealm);
  if (!list.length) {
    $('pkSummary').innerHTML = '<div class="empty">' + (all.length
      ? (panelRealm === 'global' ? '国际服没有账号' : '国服没有账号')
      : '没有账号') + '</div>';
    $('pkDetail').innerHTML = '';
    return;
  }

  // 包名 → 稳定色号（跨账号一致，方便肉眼对齐）
  const names = [];
  for (const a of list) for (const s of pkBySource(a.packages || [])) {
    if (!names.includes(s.key)) names.push(s.key);
  }
  names.sort((x, y) => {
    const sz = n => Math.max(...list.map(a => {
      const f = pkBySource(a.packages || []).find(s => s.key === n);
      return f ? f.size : 0;
    }));
    return sz(y) - sz(x);
  });
  const colorOf = n => pkColor(names.indexOf(n));
  // 键 → 展示名，供卡片与明细表共用（同一来源必然同色同名）。
  const labelOf = {};
  for (const a of list) for (const s of pkBySource(a.packages || [])) labelOf[s.key] = s;

  const maxRemain = Math.max(1, ...list.map(a => Number(a.remain || 0)));
  // 按剩余积分降序排列卡片本身（排名与顺序一致，扫一眼就是「谁最富」）。
  // 明细表仍按原始账号顺序（那边是逐包核对，顺序稳定更重要）。
  const ranked = list.map(a => ({ a, remain: Number(a.remain || 0) }))
    .sort((x, y) => y.remain - x.remain);
  const rankByUID = {};
  ranked.forEach((r, idx) => rankByUID[r.a.uid] = idx + 1);
  const ordered = ranked.map(r => r.a);

  $('pkSummary').innerHTML = ordered.map((a) => {
    const t = pkTitle(a);
    if (a.error) {
      return '<div class="pk-card"><div class="who"><span class="nm">' +
        esc(t.main) + '</span>' +
        '<span class="realm">' + esc(a.realm || '') + '</span></div>' +
        '<div class="err">查询失败：' + esc(a.error) + '</div></div>';
    }
    const srcs = pkBySource(a.packages || []);
    const size = Math.max(1, Number(a.size || 0));
    const remain = Number(a.remain || 0);
    const used = Math.max(0, size - remain);
    const usedPct = (used / size * 100);
    // 来源构成堆叠条（按面额占比）
    const bar = srcs.map(s =>
      '<i style="width:' + (s.size / size * 100).toFixed(2) + '%;background:' +
      colorOf(s.key) + '" title="' + esc(s.name) + ' ' + fmtTok(s.size) + '"></i>'
    ).join('');
    const legend = srcs.map(s =>
      '<span><i style="background:' + colorOf(s.key) + '"></i>' +
      esc(s.name.replace(/^CodeBuddy/, '')) + ' <span class="lg-n">x' + s.n + '</span>' +
      (s.minCreated ? ' <span class="lg-n">' + esc(s.minCreated.slice(5)) + '</span>' : '') +
      (s.minEnd ? ' <span class="lg-n">到期 ' + esc(s.minEnd.slice(5)) + '</span>' : '') +
      '<span class="lg-v">' + fmtTok(s.size) + '</span></span>'
    ).join('');
    return '<div class="pk-card">' +
      '<div class="who"><span class="nm">' + esc(t.main) + '</span>' +
      (t.sub ? '<span class="lg-n">' + esc(t.sub) + '</span>' : '') +
      '<span class="realm">' + esc(a.realm || '') + '</span>' +
      '<span class="rank">#' + (rankByUID[a.uid] || '') + '</span></div>' +
      // 主数字：剩余积分
      '<div class="hero"><span class="big">' + fmtTok(remain) + '</span>' +
      '<span class="unit">剩余</span></div>' +
      // 消耗条：已用（实心）+ 剩余（浅色）——消耗比例一眼可见
      '<div class="usage-bar" title="已用 ' + fmtTok(used) + ' / 总额 ' + fmtTok(size) +
        '（' + usedPct.toFixed(1) + '%）">' +
        '<i class="used" style="width:' + usedPct.toFixed(2) + '%"></i>' +
        '<i class="left" style="width:' + (100 - usedPct).toFixed(2) + '%"></i></div>' +
      '<div class="usage-meta"><span>已用 <b>' + fmtTok(used) + '</b> / ' + fmtTok(size) +
        '</span><span>' + usedPct.toFixed(1) + '%</span></div>' +
      '<div class="sub">' + (a.packages || []).length + ' 个包 · 占最高 ' +
        (remain / maxRemain * 100).toFixed(0) + '%</div>' +
      // 来源构成
      '<div class="src-title">来源构成</div>' +
      '<div class="mixbar">' + bar + '</div>' +
      '<div class="pk-legend">' + legend + '</div>' +
      '</div>';
  }).join('');

  $('pkNote').textContent = list.length + ' 个账号 · 缓存 60s' + dataTimeSuffix();

  // 逐包明细：每个账号一个表，包的**面额**列是重点
  $('pkDetail').innerHTML = list.map(a => {
    if (a.error) return '';
    const packs = (a.packages || []);
    const rows = packs.map(p => {
      const k = (p.package_code || '') + '|' + (p.name || '(未命名)');
      const sub = (p.sub_product_code || '').replace(/^sp_tcaca_codebuddyide_?/, '') ||
                  (p.package_code || '').replace(/^TCACA_/, '');
      return '<tr><td class="mark" aria-hidden="true"><i style="background:' +
        colorOf(k) + '"></i></td>' +
      '<td>' + esc(p.name || '(未命名)') +
        (sub ? '<div class="note">' + esc(sub) + '</div>' : '') + '</td>' +
      '<td class="num">' + fmtTok(p.size) + '</td>' +
      '<td class="num">' + fmtTok(p.remain) + '</td>' +
      '<td class="num">' + fmtTok(p.used) + '</td>' +
      '<td class="num">' + esc((p.created_at || '').slice(0, 16).replace('T', ' ') || '—') + '</td>' +
      '<td class="num">' + esc((p.end_time || '').slice(0, 10) || '—') + '</td>' +
      '</tr>';
    }).join('');
    return '<div class="box"><header><h3>' +
      esc(pkTitle(a).main || (a.uid || '').slice(0, 8)) + ' · ' + esc(a.realm || '') +
      '</h3><span class="grow"></span><span class="note">余额 ' + fmtTok(a.remain) +
      ' / 总额 ' + fmtTok(a.size) + ' · ' + packs.length + ' 个包（按面额降序）</span>' +
      '</header><div class="tbl-wrap"><table class="acc"><thead><tr>' +
      '<th class="mark" aria-hidden="true"></th><th>包名 / 来源</th>' +
      '<th class="num">面额</th><th class="num">剩余</th><th class="num">已用</th>' +
      '<th class="num">发放</th><th class="num">到期</th>' +
      '</tr></thead><tbody>' + rows + '</tbody></table></div></div>';
  }).join('');
}

async function loadPackages(force) {
  // 手里已有结果时**不清空**：清空再等一轮上游查询是最没必要的闪烁（切域时数据本身没变，
  // 只是换一批账号来看）。首次进入、或点了「重新查询」才显示加载态。
  if (force || !pkData) {
    $('pkSummary').innerHTML = '<div class="empty">查询中…（逐账号向上游实时查询）</div>';
    $('pkDetail').innerHTML = '';
  }
  try {
    const d = await api('packages');
    pkData = d;
    renderPackages(d);
  } catch (e) {
    $('pkSummary').innerHTML = '<div class="empty">读取失败：' + esc(e.message) + '</div>';
  }
}

if ($('btnPkgReload')) $('btnPkgReload').onclick = () => loadPackages(true);
function boot() {
  applyAppName();
  const hash = (location.hash || '#dashboard').slice(1);
  go(hash in TITLES ? hash : 'dashboard');
  start();
  enhanceControls();
}
boot();
