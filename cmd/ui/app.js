;(function () {
'use strict';

/* ---------- 页面结构总览（一体化改造后） ----------
   本页不再有「查看模式 / 编辑模式」两个互斥状态：单元格常态就是输入框，
   复选框、拖拽手柄、状态列、操作列常驻，用户改动先落在内存里，
   只有点保存才写盘。由此衍生三件事，都在下面的代码里对应实现：
   1) 脏值计算与展示：computeDiff() 对比 data 与 originData，refreshDirtyUI() 只做局部刷新
   2) 显式保存：顶部 #saveActions 仅在存在未保存改动时出现
   3) 外部冲突：SSE 携带的 rev 与 knownRev 不同即代表页面外被改动（唯一例外是本页正在
      保存的窗口——那段窗口里的 rev 变化多半就是自己这次写盘引起的，只暂存不弹条），
      有未保存改动时弹 #conflictBar 交给用户决定，绝不静默覆盖 */

/* ---------- i18n ----------
   WebUI 文案不走 Go 源码提取管线（该管线只扫描 .go），因此把英文源串作为 key。
   翻译表 MSG 定义在 config.html 末尾的独立 <script>（全局变量）——l10n 扫描器只认
   HTML 里的 var MSG 表，这是它不能搬进本文件的原因，详见那份文件里的对应注释；
   Go 端仅把当前语言注入 window.__FLK_LANG__（head 引导脚本）。
   tr() 取译文（无译文时回退英文源串），trf() 再替换 {name} 占位符，
   applyI18N() 负责把 data-i18n / placeholder / title 三类静态文案一次翻译完，
   冲突提示条的文案同样写在元素的 data-i18n 上，随 applyI18N 一起翻译 */
var LANG = (window.__FLK_LANG__ || 'en');

function tr(s) { var d = MSG[LANG]; return (d && d[s]) || s; }
function trf(s, vars) {
  return tr(s).replace(/\{(\w+)\}/g, function (_, k) {
    return (vars && vars[k] != null) ? vars[k] : '{' + k + '}';
  });
}
function applyI18N() {
  document.title = tr('flk config');
  document.querySelectorAll('[data-i18n]').forEach(function (el) { el.textContent = tr(el.getAttribute('data-i18n')); });
  document.querySelectorAll('[data-i18n-placeholder]').forEach(function (el) { el.setAttribute('placeholder', tr(el.getAttribute('data-i18n-placeholder'))); });
  document.querySelectorAll('[data-i18n-title]').forEach(function (el) { el.setAttribute('title', tr(el.getAttribute('data-i18n-title'))); });
}

/* ---------- Constants ---------- */
var STORE_API = '/api/config';
var META_API = '/api/meta';
var EVENT_API = '/api/events';
var CHECK_API = '/api/check';
var REPAIR_API = '/api/repair';
var UNLINK_API = '/api/unlink';
var LANGUAGE_API = '/api/language';

/* ---------- token ----------
   首屏地址由 Go 端生成，形如 http://<host>:8999/?token=<随机串>：webui 的门禁只保护 "/" 与
   "/api/" 前缀（静态资源一律放行），所以页面能正常加载，但页面里每个 API 请求都必须自带凭据，
   否则全部 401、页面看起来像白屏。
   首屏只能靠查询参数——浏览器地址栏发起的导航请求无法附加请求头；此后统一改用 X-WebUI-Token 头。
   刻意不把 token 从地址栏清掉：用户明确选了「token 留在 URL 里」这种最简方案，刷新还能直接复用 */
var TOKEN = new URLSearchParams(location.search).get('token') || '';

/* apiFetch 是所有 API 请求的唯一出口：token 在这里统一塞进请求头，
   调用方不必各自记得这件事（漏一处就是一处静默 401）。
   用 Object.assign 复制一份 headers：调用方传进来的对象可能还在别处使用，就地改写会留下意外副作用 */
function apiFetch(url, options) {
  var opts = options || {};
  var headers = Object.assign({}, opts.headers);
  headers['X-WebUI-Token'] = TOKEN;
  opts.headers = headers;
  return fetch(url, opts);
}

/* apiURL 把 token 放进查询参数，专供无法自定义请求头的场合。
   目前唯一的调用点是 SSE：EventSource 不支持自定义请求头，token 只能走查询参数
   （webui 的门禁同时接受查询参数与请求头，因此这条可行） */
function apiURL(url) {
  return url + (url.indexOf('?') === -1 ? '?' : '&') + 'token=' + encodeURIComponent(TOKEN);
}

/* 每种链接类型的字段定义 */
var TYPE_FIELDS = {
  symlink: [
    { key: 'real', label: 'Real path' },
    { key: 'fake', label: 'Link path' }
  ],
  hardlink: [
    { key: 'prim', label: 'Primary file' },
    { key: 'seco', label: 'Secondary file' }
  ],
  copy: [
    { key: 'src', label: 'Source file' },
    { key: 'dst', label: 'Destination file' }
  ]
};

var TYPE_NAMES = ['symlink', 'hardlink', 'copy'];

/* ---------- DOM ---------- */
var statusDot     = document.getElementById('statusDot');
var statusText    = document.getElementById('statusText');
var langSelect    = document.getElementById('langSelect');
var errorDiv      = document.getElementById('error');
var successDiv    = document.getElementById('success');
var fileInfo      = document.getElementById('fileInfo');
var checkInfoEl   = document.getElementById('checkInfo');
var updateInfo    = document.getElementById('updateInfo');
var versionInfo   = document.getElementById('versionInfo');
var storePathEl   = document.getElementById('storePath');
var recheckBtn    = document.getElementById('recheckBtn');
var saveActions   = document.getElementById('saveActions');
var dirtyCount    = document.getElementById('dirtyCount');
var saveBtn       = document.getElementById('saveBtn');
var discardBtn    = document.getElementById('discardBtn');
var conflictBar   = document.getElementById('conflictBar');
var conflictReloadBtn = document.getElementById('conflictReloadBtn');
var conflictKeepBtn   = document.getElementById('conflictKeepBtn');
var loading       = document.getElementById('loading');
var panel         = document.getElementById('panel');
var platformTabs  = document.getElementById('platformTabs');
var deviceTabs    = document.getElementById('deviceTabs');
var typeTabs      = document.getElementById('typeTabs');
var tableHead     = document.getElementById('tableHead');
var tableBody     = document.getElementById('tableBody');
var migrateBtn    = document.getElementById('migrateBtn');
var repairBtn     = document.getElementById('repairBtn');
var emptyState    = document.getElementById('emptyState');
var addEntryBtn   = document.getElementById('addEntryBtn');
var addDeviceArea    = document.getElementById('addDeviceArea');
var newDeviceInput   = document.getElementById('newDeviceInput');
var addDeviceBtn     = document.getElementById('addDeviceBtn');
var cancelAddDeviceBtn = document.getElementById('cancelAddDeviceBtn');

/* ---------- Migration DOM ---------- */
var migrateModal       = document.getElementById('migrateModal');
var migrateCount       = document.getElementById('migrateCount');
var migrateTargetPlat  = document.getElementById('migrateTargetPlat');
var migrateTargetDev   = document.getElementById('migrateTargetDev');
var migrateTargetType  = document.getElementById('migrateTargetType');
var migrateConfirmBtn  = document.getElementById('migrateConfirmBtn');
var migrateCancelBtn   = document.getElementById('migrateCancelBtn');
var migrateActionRadios = document.querySelectorAll('input[name="migrateAction"]');

/* ---------- Repair DOM ---------- */
var repairModal      = document.getElementById('repairModal');
var repairTitle      = document.getElementById('repairTitle');
var repairCount      = document.getElementById('repairCount');
var repairList       = document.getElementById('repairList');
var repairOutput     = document.getElementById('repairOutput');
var repairConfirmBtn = document.getElementById('repairConfirmBtn');
var repairCancelBtn  = document.getElementById('repairCancelBtn');
var repairCloseBtn   = document.getElementById('repairCloseBtn');

/* ---------- Unlink DOM ---------- */
var unlinkModal      = document.getElementById('unlinkModal');
var unlinkTitle      = document.getElementById('unlinkTitle');
var unlinkList       = document.getElementById('unlinkList');
var unlinkNoTrash    = document.getElementById('unlinkNoTrash');
var unlinkTrashHint  = document.getElementById('unlinkTrashHint');
var unlinkForcedHint = document.getElementById('unlinkForcedHint');
var unlinkOutput     = document.getElementById('unlinkOutput');
var unlinkConfirmBtn = document.getElementById('unlinkConfirmBtn');
var unlinkCancelBtn  = document.getElementById('unlinkCancelBtn');
var unlinkCloseBtn   = document.getElementById('unlinkCloseBtn');

/* ---------- Theme DOM ---------- */
var themeToggle = document.getElementById('themeToggle');
var themeButtons = themeToggle ? themeToggle.querySelectorAll('.theme-btn') : [];

/* ---------- Filter DOM ---------- */
var filterInput = document.getElementById('filterInput');
var filterClear = document.getElementById('filterClear');
var filterCountEl = document.getElementById('filterCount');
var filterBox = document.getElementById('filterBox');

/* ---------- 主题 ----------
   三态模式（light / dark / system）持久化在 localStorage（flk-theme-mode）；
   解析后的实际主题写 <html data-theme>，由 <head> 里的引导脚本在首帧前完成首次写入（防闪烁），
   本模块负责切换按钮的交互、高亮同步，以及「跟随系统」时的实时响应
   引导脚本与本模块共享同一个 localStorage key 与解析规则，两边改动必须一起看 */
var THEME_KEY = 'flk-theme-mode';
var themeMode = 'system';
try { themeMode = localStorage.getItem(THEME_KEY) || 'system'; } catch (e) { /* 隐私模式等场景下静默回退 system */ }

function resolveTheme(mode) {
  var dark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
  return mode === 'system' ? (dark ? 'dark' : 'light') : mode;
}

/* 应用一个模式：写 storage、更新 <html data-theme>、同步切换按钮高亮 */
function applyThemeMode(mode) {
  themeMode = mode;
  try { localStorage.setItem(THEME_KEY, mode); } catch (e) { /* 存不进去也不影响本次会话生效 */ }
  document.documentElement.setAttribute('data-theme', resolveTheme(mode));
  syncThemeButtons();
}

/* 同步三态按钮的高亮（active 类）：模式即按钮 data-theme-mode */
function syncThemeButtons() {
  themeButtons.forEach(function (btn) {
    btn.classList.toggle('active', btn.dataset.themeMode === themeMode);
  });
}

/* 跟随系统时监听系统主题变化：用户在系统设置里切换亮暗，页面不刷新也要跟上
   只在 system 模式下响应；其它模式用户已显式表态，系统变化不应覆盖 */
if (window.matchMedia) {
  var themeMq = window.matchMedia('(prefers-color-scheme: dark)');
  var onSystemThemeChange = function () {
    if (themeMode === 'system') {
      document.documentElement.setAttribute('data-theme', resolveTheme('system'));
    }
  };
  if (themeMq.addEventListener) themeMq.addEventListener('change', onSystemThemeChange);
  else if (themeMq.addListener) themeMq.addListener(onSystemThemeChange); /* 旧 Safari 兜底 */
}

/* ---------- 路径过滤 ----------
   纯客户端子串匹配（大小写不敏感），作用域是当前「平台+设备+类型」视图的全部路径字段；
   renderTable 消费 filterText 决定渲染哪些行（见 renderTable 的命中掩码） */
var filterText = '';

/* 单条目是否命中搜索词：q 已是小写、已去首尾空白 */
function entryMatchesFilter(entry, fields, q) {
  var hit = false;
  fields.forEach(function (f) {
    var v = (entry && entry[f.key]) || '';
    if (String(v).toLowerCase().indexOf(q) >= 0) hit = true;
  });
  return hit;
}

/* 更新工具栏计数与清除按钮的可用态：无搜索词时计数隐藏、清除按钮不占位 */
function updateFilterCount(visible, total) {
  if (!filterCountEl) return;
  if (!filterText.trim() || total === 0) {
    filterCountEl.textContent = '';
  } else {
    filterCountEl.textContent = trf('{n} of {total} shown', { n: visible, total: total });
  }
  if (filterBox) filterBox.classList.toggle('has-text', !!filterText.trim());
}

/* 设置搜索词并重渲染：输入框事件与清除按钮共用这一个入口 */
function setFilter(text) {
  filterText = text;
  if (filterInput && filterInput.value !== text) filterInput.value = text;
  renderTable();
  /* 焦点保持：重渲染后把光标还给输入框，连续输入不被打断（renderTable 重建的是 tbody） */
  if (filterInput) filterInput.focus();
}

/* ---------- State ---------- */
var data       = null;    /* 完整配置数据，用户输入直接原地修改它，因此保存时无需整体替换；整份替换只出现在「加载」与「撤销改动」两条路径上 */
var originData = null;    /* 最近一次「加载成功 / 保存成功」时的深拷贝，用于算脏值与撤销 */
var knownRev   = '';      /* 本页已知的 store 文件版本，SSE 收到相同 rev 即判定为本页自己保存引起 */
var originRev  = '';      /* originData 快照对应的文件版本，用来判断本地快照是否已经落后于磁盘 */
var pendingRev = '';      /* 冲突提示条对应的外部版本，「重新加载」与「保留我的改动」两个出口都用它推进 knownRev */
var saving     = false;   /* 本页正在保存：服务端在写响应体之前就广播 SSE，这段窗口里的 rev 变化不能当成外部冲突 */
var revDuringSave = '';   /* 保存期间最后一次观察到的 SSE 版本：窗口内多次变更只留最后一次，与响应里的版本不同才说明外部确实也改过文件 */
var currentPlat = '';
var currentDev  = '';
var currentType = 'symlink';
var hostPlatform = '';  /* 服务端上报的宿主平台（如 linux-amd64 的平台族 linux）：首次加载时用它选默认页签，见 pickDefaultPlatform */
var dragSrcIdx  = -1;     /* 拖拽起始行索引 */
var dragOriginInInput = false; /* 本次鼠标手势是否从输入框内按下：整行可拖拽会把输入框里的拖动劫持成拖动整行 */
var selectedIndices = {}; /* 当前选中的行索引，用于批量迁移 */
var checkPlatform = '';   /* 可用性检测结果所属平台（仅该平台页签下展示状态） */
var checkMap      = {};   /* key: device|type|字段值… → {valid, error} */
var checkResults  = [];   /* /api/check 的原始结果数组：修复需要 device/type 与字段值来定位记录，拼串后的 checkMap 反解不出来 */
var checkLoaded   = false;/* 是否已成功完成过一次检测（区分「未检测」与「无匹配」） */
var repairTargets = [];   /* 当前弹窗待修复的记录，直接来自 checkResults 的无效项 */
var repairAll     = false;/* 本次修复是全量还是单条：决定 POST /api/repair 的请求体形态 */
var unlinkTarget  = null; /* 当前解除弹窗的目标记录（来自 checkResults 的有效项）；单条端点，故只需一个 */
var serverNoTrash = false;/* 服务端是否以 --no-trash 启动（来自 /api/meta）：为真时解除一律真实删除，
                             弹窗里的复选框只能勾选不能取消，避免给出一个点了也不生效的开关 */
var beforeUnloadAttached = false; /* beforeunload 是否已注册，避免重复挂载或漏摘 */

/* ---------- UI Helpers ---------- */
function setOnline(online) {
  statusDot.className = 'dot ' + (online ? 'online' : 'offline');
  statusText.textContent = online ? tr('Connected') : tr('Disconnected');
}

function showError(msg) {
  errorDiv.textContent = msg; errorDiv.style.display = 'block';
}

function hideError() { errorDiv.style.display = 'none'; }

function showSuccess(msg) {
  successDiv.textContent = msg; successDiv.style.display = 'block';
  setTimeout(function () { successDiv.style.display = 'none'; }, 3000);
}

/* ---------- Data access helpers ---------- */
function ensurePath(plat, dev) {
  if (!data[plat]) data[plat] = {};
  if (!data[plat][dev]) data[plat][dev] = {};
  TYPE_NAMES.forEach(function (t) {
    if (!data[plat][dev][t]) data[plat][dev][t] = [];
  });
}

function getEntries() {
  if (!data || !data[currentPlat]) return [];
  var devData = data[currentPlat][currentDev];
  if (!devData) return [];
  return devData[currentType] || [];
}

function setEntries(arr) {
  if (!data[currentPlat]) data[currentPlat] = {};
  if (!data[currentPlat][currentDev]) data[currentPlat][currentDev] = {};
  data[currentPlat][currentDev][currentType] = arr;
}

/* ---------- Platforms / Devices discovery ---------- */
function getPlatforms() {
  if (!data) return [];
  return Object.keys(data).sort();
}

/* 默认平台的选择规则：宿主平台优先，其次才回退到字母序第一个
   修正的 UX 缺陷：改造前一律取 plats[0]，多平台清单按字母序往往落在 darwin，
   而「检测/修复/解除」全部只作用于宿主平台——linux 用户每次打开都要手动切一次页签，
   徽标还全程显示「—」（平台不匹配），像功能坏了 */
function pickDefaultPlatform(plats) {
  if (hostPlatform && plats.indexOf(hostPlatform) >= 0) return hostPlatform;
  return plats[0];
}

function getDevices(plat) {
  if (!data || !data[plat]) return [];
  return Object.keys(data[plat]).sort();
}

/* ---------- 脏值计算 ----------
   一体化后表格不再区分模式，必须随时知道「哪些格子与磁盘上的版本不同」，
   算法刻意不用引用比较（保存后对象会被整体替换，引用比较会整表误标），
   也不用 O(n²) 的 LCS：先用规范串做贪心前后缀对齐，再针对未对齐区段细算 */

/* 行规范串：按 TYPE_FIELDS 顺序取字段值拼成 JSON 数组，与对象 key 顺序无关。
   缺失字段统一归一成空串，避免 undefined / '' / null 造成假脏 */
function canonicalRow(type, entry) {
  var fields = TYPE_FIELDS[type] || [];
  return JSON.stringify(fields.map(function (f) {
    var v = entry ? entry[f.key] : '';
    return v == null ? '' : v;
  }));
}

/* 收集 data 与 originData 中出现过的所有 plat|dev|type 路径。
   两侧都要扫：撤销可能让某个平台/设备重新出现，而当前数据里未必还有它 */
function collectPaths() {
  var paths = {};
  [data, originData].forEach(function (root) {
    if (!root) return;
    Object.keys(root).forEach(function (plat) {
      Object.keys(root[plat] || {}).forEach(function (dev) {
        TYPE_NAMES.forEach(function (t) {
          paths[plat + '|' + dev + '|' + t] = { plat: plat, dev: dev, type: t };
        });
      });
    });
  });
  return paths;
}

/* computeDiff 返回 { cells, newRows, dirtyRows, count }
   cells     key = plat|dev|type|idx|field
   newRows   key = plat|dev|type|idx
   dirtyRows key = plat|dev|type|idx（字段被改或整行新增）
   count 语义：字段级变更数 + 新增行数 + 删除行数 */
function computeDiff() {
  var res = { cells: {}, newRows: {}, dirtyRows: {}, count: 0 };
  if (!data || !originData) return res;
  var paths = collectPaths();
  Object.keys(paths).forEach(function (p) {
    var seg = paths[p];
    var cur = ((data[seg.plat] || {})[seg.dev] || {})[seg.type] || [];
    var old = ((originData[seg.plat] || {})[seg.dev] || {})[seg.type] || [];
    var n = cur.length, m = old.length;

    /* 贪心前后缀对齐：先吃掉完全相同的头部，再吃掉完全相同的尾部，
       剩下的未对齐区段才是「真正发生变化的区域」，这样单点修改不会被放大 */
    var i = 0;
    while (i < n && i < m && canonicalRow(seg.type, cur[i]) === canonicalRow(seg.type, old[i])) i++;
    var j = 0;
    while (j < n - i && j < m - i && canonicalRow(seg.type, cur[n - 1 - j]) === canonicalRow(seg.type, old[m - 1 - j])) j++;

    var curSegLen = n - i - j;
    var oldSegLen = m - i - j;
    var overlap = Math.min(curSegLen, oldSegLen);

    /* 重叠部分逐字段对位比较，精确标出脏单元格 */
    var fields = TYPE_FIELDS[seg.type] || [];
    for (var k = 0; k < overlap; k++) {
      var rowIdx = i + k;
      for (var fi = 0; fi < fields.length; fi++) {
        var fk = fields[fi].key;
        var cv = cur[rowIdx][fk] == null ? '' : cur[rowIdx][fk];
        var ov = old[rowIdx][fk] == null ? '' : old[rowIdx][fk];
        if (cv !== ov) {
          res.cells[p + '|' + rowIdx + '|' + fk] = true;
          res.dirtyRows[p + '|' + rowIdx] = true;
          res.count++;
        }
      }
    }
    /* 当前侧多出的行视为新增：整行脏、整行计入 count */
    for (var x = overlap; x < curSegLen; x++) {
      var newIdx = i + x;
      res.newRows[p + '|' + newIdx] = true;
      res.dirtyRows[p + '|' + newIdx] = true;
      res.count++;
    }
    /* 旧侧多出的行视为删除：表格里没有对应行可高亮，只计数 */
    if (oldSegLen > curSegLen) res.count += oldSegLen - curSegLen;
  });
  return res;
}

/* ---------- 脏值 UI 刷新 ----------
   只做局部更新（改 class、改徽标、改文本），绝不重建表格：
   用户在格子里打字时若重建 tbody，光标和选区会立刻丢失 */
function refreshDirtyUI() {
  var diff = computeDiff();
  var hasDirty = diff.count > 0;

  saveActions.classList.toggle('hidden', !hasDirty);
  dirtyCount.textContent = trf('Unsaved changes: {n}', { n: diff.count });

  var prefix = currentPlat + '|' + currentDev + '|' + currentType;
  /* 单元格脏标记 */
  tableBody.querySelectorAll('.cell-input').forEach(function (inp) {
    var tr = inp.closest('tr');
    if (!tr) return;
    inp.classList.toggle('dirty', !!diff.cells[prefix + '|' + tr.dataset.idx + '|' + inp.dataset.key]);
  });
  /* 状态列：脏行降级为「待重检」，干净行回落到 checkMap 的真实结果 */
  var entries = getEntries();
  tableBody.querySelectorAll('tr').forEach(function (tr) {
    var cell = tr.querySelector('.health-col');
    if (!cell) return;
    var idx = tr.dataset.idx;
    if (diff.dirtyRows[prefix + '|' + idx]) {
      cell.innerHTML = pendingBadge();
      return;
    }
    var entry = entries[parseInt(idx)];
    cell.innerHTML = entry ? healthBadge(entry) : '';
  });

  updateBeforeUnload(hasDirty);
}

/* 待重检徽标：该行与已保存版本不一致，旧的检测结论已经不可信 */
function pendingBadge() {
  return '<span class="health-badge health-pending">' + tr('Pending re-check') + '</span>';
}

/* ---------- 离开拦截 ----------
   有未保存改动时注册 beforeunload，避免用户关页/刷新时无声丢失输入 */
function onBeforeUnload(e) {
  e.preventDefault();
  e.returnValue = tr('You have unsaved changes');
  return e.returnValue;
}

function updateBeforeUnload(hasDirty) {
  if (hasDirty && !beforeUnloadAttached) {
    window.addEventListener('beforeunload', onBeforeUnload);
    beforeUnloadAttached = true;
  } else if (!hasDirty && beforeUnloadAttached) {
    window.removeEventListener('beforeunload', onBeforeUnload);
    beforeUnloadAttached = false;
  }
}

/* ---------- Tab rendering ---------- */
function renderPlatformTabs() {
  var plats = getPlatforms();
  /* 先修正 currentPlat 再渲染：撤销/重新加载可能让当前平台不复存在，
     若先渲染后修正，页签高亮会和表格数据错位；回退顺序与首次加载一致（宿主平台优先） */
  if (plats.indexOf(currentPlat) < 0 && plats.length > 0) {
    currentPlat = pickDefaultPlatform(plats);
  }
  platformTabs.innerHTML = plats.map(function (p) {
    return '<button class="tab' + (p === currentPlat ? ' active' : '') + '" data-plat="' + attrEsc(p) + '">' + escHtml(p) + '</button>';
  }).join('');
  platformTabs.querySelectorAll('.tab').forEach(function (el) {
    el.addEventListener('click', function () {
      currentPlat = el.dataset.plat;
      currentDev = getDevices(currentPlat)[0] || '';
      renderAll();
    });
  });
}

function renderDeviceTabs() {
  var devs = getDevices(currentPlat);
  /* 同上：先兜底 currentDev，设备删除后页签高亮才不会指向不存在的设备 */
  if (devs.indexOf(currentDev) < 0 && devs.length > 0) {
    currentDev = devs[0];
  }
  /* 设备页签：浏览器页签形态——文字与删除 × 分居左右，× 常态半隐、悬停页签时显现
     （CSS 见 .tab-dev / .btn-dev-del）；「+ 新建设备」是虚线幽灵页签，与普通页签区分动作与值 */
  var html = devs.map(function (d) {
    var active = d === currentDev ? ' active' : '';
    return '<span class="tab tab-dev' + active + '" data-dev="' + attrEsc(d) + '">' +
      '<span class="tab-label">' + escHtml(d) + '</span>' +
      '<button class="btn-dev-del" data-dev="' + attrEsc(d) + '" title="' + attrEsc(trf('Delete device {dev}', {dev: d})) + '">&times;</button>' +
      '</span>';
  }).join('');
  html += '<button class="tab tab-new" id="showAddDeviceBtn">' + tr('+ New device') + '</button>';
  deviceTabs.innerHTML = html;
  deviceTabs.querySelectorAll('.tab[data-dev]').forEach(function (el) {
    el.addEventListener('click', function () {
      currentDev = el.dataset.dev;
      renderAll();
    });
  });
  /* 设备删除按钮 */
  deviceTabs.querySelectorAll('.btn-dev-del').forEach(function (btn) {
    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      var dev = btn.dataset.dev;
      if (!confirm(trf('Delete device "{dev}" and all its entries?', {dev: dev}))) return;
      delete data[currentPlat][dev];
      if (currentDev === dev) {
        var remain = getDevices(currentPlat);
        currentDev = remain.length > 0 ? remain[0] : '';
      }
      renderAll();
    });
  });
  var showAddBtn = document.getElementById('showAddDeviceBtn');
  if (showAddBtn) {
    showAddBtn.addEventListener('click', function () {
      addDeviceArea.classList.remove('hidden');
      newDeviceInput.focus();
    });
  }
}

function renderTypeTabs() {
  typeTabs.innerHTML = TYPE_NAMES.map(function (t) {
    return '<button class="tab' + (t === currentType ? ' active' : '') + '" data-type="' + t + '">' + t + '</button>';
  }).join('');
  typeTabs.querySelectorAll('.tab').forEach(function (el) {
    el.addEventListener('click', function () {
      currentType = el.dataset.type;
      renderAll();
    });
  });
}

/* ---------- 焦点保持 ----------
   renderTable 会重建整个 tbody，若不记录并恢复，
   「正在输入时 Re-check 完成 / SSE 自动重载」都会把光标踢走 */
function captureFocus() {
  var el = document.activeElement;
  if (!el || !el.classList || !el.classList.contains('cell-input')) return null;
  var tr = el.closest('tr');
  if (!tr) return null;
  return {
    idx: tr.dataset.idx,
    key: el.dataset.key,
    start: el.selectionStart,
    end: el.selectionEnd
  };
}

function restoreFocus(snap) {
  if (!snap) return;
  var el = tableBody.querySelector('tr[data-idx="' + snap.idx + '"] .cell-input[data-key="' + snap.key + '"]');
  if (!el) return; /* 原焦点行可能已被删除，此时无需恢复 */
  el.focus();
  try {
    el.setSelectionRange(snap.start, snap.end);
  } catch (_) {
    /* 个别输入类型不支持选区操作，忽略即可 */
  }
}

/* ---------- Table rendering ---------- */
function renderTable() {
  var entries = getEntries();
  var fields = TYPE_FIELDS[currentType];
  var focus = captureFocus();

  /* 修剪越界的选中标记：删除条目后旧索引可能指向不存在的行 */
  var pruned = {};
  Object.keys(selectedIndices).forEach(function (k) {
    if (selectedIndices[k] && parseInt(k) < entries.length) pruned[k] = true;
  });
  selectedIndices = pruned;

  /* 路径过滤的命中掩码：搜索词为空时全部命中
     data-idx 始终用原始索引——脏值计算（computeDiff）、行内按钮的回查（findCheckResult）
     都以原始索引定位条目，过滤只是「不渲染该行」，绝不能删数据或重排索引 */
  var q = filterText.trim().toLowerCase();
  var matchFlags = entries.map(function (entry) { return !q || entryMatchesFilter(entry, fields, q); });
  /* 全选的判定与勾选动作都只作用于可见行：过滤态下全选「看不见的行」会让人以为按钮坏了 */
  var hasVisible = matchFlags.some(function (m) { return m; });
  var allSelected = hasVisible && entries.every(function (_, i) { return !matchFlags[i] || selectedIndices[i]; });

  /* Head：复选框 / 拖拽手柄 / 序号 / 字段 / 状态 / 操作 六类列常驻，
     表格结构在页面生命周期内保持稳定，不再随模式增删 */
  var headHtml = '<th class="cb-col"><input type="checkbox" class="cb-all"' + (allSelected ? ' checked' : '') + ' id="cbAll"></th>';
  headHtml += '<th class="drag-handle"></th>';
  headHtml += '<th class="num-col">#</th>';
  fields.forEach(function (f) { headHtml += '<th>' + tr(f.label) + '</th>'; });
  headHtml += '<th class="health-col">' + tr('Status') + '</th>';
  headHtml += '<th class="action-col">' + tr('Actions') + '</th>';
  tableHead.innerHTML = headHtml;

  /* 全选：常驻绑定，只勾选/取消当前可见（未被过滤掉）的行 */
  var cbAll = document.getElementById('cbAll');
  if (cbAll) {
    cbAll.addEventListener('change', function () {
      entries.forEach(function (_, i) {
        if (matchFlags[i]) selectedIndices[i] = cbAll.checked;
      });
      renderTable();
    });
  }

  if (entries.length === 0) {
    /* 无条目时只显示空状态提示，不在这里再放新增按钮：
       常驻的操作条已经是「+ 新增条目」的唯一入口，同一功能不留两个入口 */
    tableBody.innerHTML = '';
    emptyState.classList.remove('hidden');
    updateFilterCount(0, 0);
  } else {
    emptyState.classList.add('hidden');

    var rowsHtml = '';
    entries.forEach(function (entry, idx) {
      if (!matchFlags[idx]) return;
      rowsHtml += '<tr data-idx="' + idx + '" draggable="true">';
      rowsHtml += '<td class="cb-col"><input type="checkbox" class="cb-row"' + (selectedIndices[idx] ? ' checked' : '') + ' data-idx="' + idx + '"></td>';
      rowsHtml += '<td class="drag-handle" draggable="true">⣿</td>';
      rowsHtml += '<td class="num-col">' + (idx + 1) + '</td>';
      fields.forEach(function (f) {
        var val = entry[f.key] || '';
        rowsHtml += '<td><input class="cell-input" data-key="' + attrEsc(f.key) + '" value="' + attrEsc(val) + '"></td>';
      });
      rowsHtml += '<td class="health-col">' + healthBadge(entry) + '</td>';
      rowsHtml += '<td class="action-col">' + rowRepairBtn(entry, idx) + rowUnlinkBtn(entry, idx) + '<button class="btn btn-sm btn-dell" data-idx="' + idx + '">' + tr('Delete') + '</button></td>';
      rowsHtml += '</tr>';
    });
    /* 有条目但全部被过滤掉：给出「无匹配」占位行，避免表格只剩表头的空壳；
       colspan 覆盖全部六类列 + 字段列，行内不渲染任何可交互元素 */
    if (rowsHtml === '') {
      rowsHtml = '<tr><td colspan="' + (fields.length + 5) + '" class="filter-empty">' + escHtml(tr('No matching entries')) + '</td></tr>';
    }
    tableBody.innerHTML = rowsHtml;
    updateFilterCount(matchFlags.filter(Boolean).length, entries.length);

    /* 行复选框 */
    tableBody.querySelectorAll('.cb-row').forEach(function (cb) {
      cb.addEventListener('change', function () {
        selectedIndices[parseInt(cb.dataset.idx)] = cb.checked ? true : false;
        renderTable();
      });
    });
    /* Drag-and-drop events：常驻绑定；mousedown 先于 dragstart 记录手势起点 */
    tableBody.querySelectorAll('tr').forEach(function (tr) {
      tr.addEventListener('mousedown', onRowMouseDown);
      tr.addEventListener('dragstart', onDragStart);
      tr.addEventListener('dragover',  onDragOver);
      tr.addEventListener('dragleave', onDragLeave);
      tr.addEventListener('drop',      onDrop);
      tr.addEventListener('dragend',   onDragEnd);
    });
    /* Delete buttons */
    tableBody.querySelectorAll('.btn-dell').forEach(function (btn) {
      btn.addEventListener('click', function () { deleteRow(parseInt(btn.dataset.idx)); });
    });
    /* 行内修复：按行索引回查检测结果，找不到就放弃（结果可能已被新一轮检测替换） */
    tableBody.querySelectorAll('.btn-repair').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var entry = getEntries()[parseInt(btn.dataset.idx)];
        var target = entry ? findCheckResult(entry) : null;
        if (target) openRepairModal([target], false);
      });
    });
    /* 行内解除：与修复同样的回查方式，找不到结果就放弃（目标可能已被新一轮检测替换） */
    tableBody.querySelectorAll('.btn-unlink').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var entry = getEntries()[parseInt(btn.dataset.idx)];
        var target = entry ? findCheckResult(entry) : null;
        if (target) openUnlinkModal(target);
      });
    });
    /* 单元格输入：用 input 事件（而非 change/blur）做到即时同步，
       回调里只写回 data 并局部刷新脏值 UI，绝不重建表格 */
    tableBody.querySelectorAll('.cell-input').forEach(function (inp) {
      inp.addEventListener('input', onCellInput);
    });
  }

  updateMigrateBtn();
  updateRepairBtn();
  restoreFocus(focus);
  refreshDirtyUI();
}

function updateMigrateBtn() {
  var count = Object.keys(selectedIndices).filter(function (k) { return selectedIndices[k]; }).length;
  if (migrateBtn) {
    migrateBtn.classList.toggle('hidden', count === 0);
  }
  var selLabel = document.getElementById('selCount');
  if (selLabel) { selLabel.textContent = trf('Selected {n} items', {n: count}); }
}

/* ---------- HTML 转义 ----------
   平台名与设备名由用户命名，拼进 innerHTML 前必须转义，否则含引号或尖括号的名字会破坏属性或渲染
   只保留一份映射实现：文本走 escHtml，属性值在其基础上补一个引号转义
   这样两处转义不会各自演化，也不会出现「已删除的函数」与注释不一致 */
function escHtml(s) {
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
function attrEsc(s) { return escHtml(s).replace(/"/g, '&quot;'); }

/* ---------- 可用性检测 ---------- */
/* 检测状态匹配 key：device|type|各字段值
   后端 CheckResult 的 JSON 字段名（real/fake/prim/seco/src/dst）与条目 key 天然一致，
   故同一个函数对配置条目和检测结果都适用 */
function buildCheckKey(device, type, entry) {
  var parts = [device, type];
  (TYPE_FIELDS[type] || []).forEach(function (f) { parts.push(entry[f.key] || ''); });
  return parts.join('|');
}

/* 状态列单元格：有效/无效/未知三态，无效时 tooltip 展示后端返回的错误详情。
   脏行的「待重检」由 refreshDirtyUI 覆盖，不走这里 */
function healthBadge(entry) {
  if (!checkLoaded || currentPlat !== checkPlatform) {
    return '<span class="health-badge health-none">—</span>';
  }
  var st = checkMap[buildCheckKey(currentDev, currentType, entry)];
  if (!st) return '<span class="health-badge health-none">—</span>';
  if (st.valid) return '<span class="health-badge health-ok">' + tr('Valid') + '</span>';
  return '<span class="health-badge health-bad" title="' + attrEsc(st.error) + '">' + tr('Invalid') + '</span>';
}

/* 调用 /api/check 全量检测当前平台，完成后刷新状态列
   检测涉及文件系统操作可能较慢，异步执行不阻塞主渲染；
   renderTable 自带焦点恢复，所以即使用户正在输入也不会丢光标 */
async function loadCheckStatus() {
  checkInfoEl.textContent = tr('Checking…');
  try {
    var resp = await apiFetch(CHECK_API);
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    var payload = await resp.json();
    checkPlatform = payload.platform || '';
    checkMap = {};
    checkResults = payload.results || [];
    checkResults.forEach(function (r) {
      checkMap[buildCheckKey(r.device, r.type, r)] = { valid: r.valid, error: r.error || '' };
    });
    checkLoaded = true;
    checkInfoEl.textContent = trf('Checked at: {time}', {time: new Date().toLocaleTimeString()});
  } catch (err) {
    checkLoaded = false;
    /* 检测失败时一并清空原始结果：留着上一次的旧结论会让「修复无效项」按钮继续可用，
       而它列出的路径可能早已变化，属于用过期数据做破坏性操作 */
    checkResults = [];
    checkInfoEl.textContent = trf('Availability check failed: {msg}', {msg: err.message});
  }
  renderTable();
}

/* ---------- 一键修复 ----------
   把清单里「检查为无效」的记录真正变成磁盘上的链接，消除「网页加完记录还得回终端跑 flk fix」的断点。
   数据源固定为 /api/check 的原始结果：修复需要 device/type 与字段值去定位 store 里的那条记录，
   而 checkMap 的 key 是这些字段拼出来的字符串，反解既脆弱又没有必要 */

/* 无效条目（即待修复项）。与徽标展示口径一致：只在检测结果所属平台上成立 */
function invalidResults() {
  return checkResults.filter(function (r) { return !r.valid; });
}

/* 一条结果的可读路径串，与后端 recordDisplayPaths 的「权威副本 → 派生位置」口径保持一致 */
function resultPaths(r) {
  if (r.type === 'symlink') return (r.real || '') + ' → ' + (r.fake || '');
  if (r.type === 'hardlink') return (r.prim || '') + ' → ' + (r.seco || '');
  if (r.type === 'copy') return (r.src || '') + ' → ' + (r.dst || '');
  return '';
}

/* 单条修复的定位字段：直接取结果里与 store 同名的那几个字段，后端按逐字比对定位记录 */
function resultFields(r) {
  var out = {};
  (TYPE_FIELDS[r.type] || []).forEach(function (f) { out[f.key] = r[f.key] || ''; });
  return out;
}

/* 修复按钮的显隐与计数：存在无效项且当前正浏览检测结果所属平台时才可用
   跨平台页签下不显示，是因为后端只检测、也只能修复它自己运行所在的平台 */
function updateRepairBtn() {
  if (!repairBtn) return;
  var n = (checkLoaded && currentPlat === checkPlatform) ? invalidResults().length : 0;
  repairBtn.classList.toggle('hidden', n === 0);
  repairBtn.textContent = tr('Repair invalid entries') + (n > 0 ? ' (' + n + ')' : '');
}

/* 行内修复按钮：只给「本行确实无效」的行渲染，其余行保持原样，避免满屏无效按钮
   点击时再按当前行索引回查原始结果（见 findCheckResult），不在 DOM 上缓存整份结果对象 */
function rowRepairBtn(entry, idx) {
  if (!checkLoaded || currentPlat !== checkPlatform) return '';
  var st = checkMap[buildCheckKey(currentDev, currentType, entry)];
  if (!st || st.valid) return '';
  return '<button class="btn btn-sm btn-save btn-repair" data-idx="' + idx + '">' + tr('Repair') + '</button> ';
}

/* 行内解除按钮：只给「本行确实有效」的行渲染，方向与修复按钮互补
   为什么限制在有效行：解除的对象是磁盘上真实存在的链接，无效记录在文件系统层面已无链接可解
   （CLI 的语义也是「无效记录交给 fix 处理」），若照样渲染按钮，用户点下去只会得到
   「找不到要解除的条目」——把必然失败的入口摆在眼前比不摆更糟
   未完成检测或浏览的是其它平台时不渲染：那时页面根本不知道这行是否有效 */
function rowUnlinkBtn(entry, idx) {
  if (!checkLoaded || currentPlat !== checkPlatform) return '';
  var st = checkMap[buildCheckKey(currentDev, currentType, entry)];
  if (!st || !st.valid) return '';
  return '<button class="btn btn-sm btn-unlink" data-idx="' + idx + '">' + tr('Unlink') + '</button> ';
}

/* 按「设备|类型|字段值」在当前检测结果里找回对应的原始结果，供行内单条修复使用
   找不到时返回 null（例如检测结果已被新一轮检测替换），调用方据此放弃本次操作 */
function findCheckResult(entry) {
  var key = buildCheckKey(currentDev, currentType, entry);
  for (var i = 0; i < checkResults.length; i++) {
    var r = checkResults[i];
    if (buildCheckKey(r.device, r.type, r) === key) return r;
  }
  return null;
}

/* 打开修复弹窗：targets 为待修复结果列表，isAll 决定 POST 的请求体形态
   有未保存改动时直接拒绝：修复针对的是磁盘上的清单，而页面里未保存的改动会让
   「列表里显示的路径」与「实际会被修的记录」对不上，属于最容易造成误解的场景 */
function openRepairModal(targets, isAll) {
  if (!targets || targets.length === 0) return;
  if (computeDiff().count > 0) {
    showError(tr('Save or discard your unsaved changes before repairing'));
    return;
  }
  repairTargets = targets;
  repairAll = isAll;

  repairTitle.textContent = tr('Repair invalid entries');
  repairCount.textContent = trf('{n} invalid entries will be repaired', {n: targets.length});
  repairList.innerHTML = targets.map(function (r) {
    return '<div class="repair-item"><span class="repair-type">' + escHtml(r.type) + '</span> ' +
      escHtml(r.device) + '<br>' + escHtml(resultPaths(r)) + '</div>';
  }).join('');

  /* 每次打开都回到「确认」阶段：上一轮的输出与按钮状态不能残留 */
  repairList.classList.remove('hidden');
  repairOutput.classList.add('hidden');
  repairOutput.classList.remove('failed');
  repairOutput.textContent = '';
  repairConfirmBtn.classList.remove('hidden');
  repairConfirmBtn.disabled = false;
  repairCancelBtn.classList.remove('hidden');
  repairCloseBtn.classList.add('hidden');

  repairModal.classList.remove('hidden');
}

/* 执行修复：POST /api/repair，然后把服务端返回的整段输出留在弹窗里直到用户关闭
   输出里既有删除计划（pterm 在非终端 writer 下会退化成纯文本）也有逐条结果，
   用 pre 原样展示比塞进 3 秒自动消失的成功提示更实用 */
async function doRepair() {
  var targets = repairTargets;
  /* 单条模式下没有目标就什么都不做：目标只可能由 openRepairModal 写入，这里只是防御性兜底 */
  if (!repairAll && (!targets || targets.length === 0)) return;
  var body = repairAll
    ? { all: true }
    : { device: targets[0].device, type: targets[0].type, fields: resultFields(targets[0]) };

  repairConfirmBtn.disabled = true;
  repairCount.textContent = tr('Repairing…');

  try {
    var resp = await apiFetch(REPAIR_API, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    });
    if (!resp.ok) { var txt = await resp.text(); throw new Error(txt || 'HTTP ' + resp.status); }
    var payload = await resp.json();

    /* 服务端在写响应之前就广播了 repaired 事件，页面可能已经刷过一轮状态；
       这里再刷一次，确保徽标反映的是「修复完成之后」的最终状态 */
    await loadCheckStatus();

    repairTitle.textContent = tr('Repair result');
    repairCount.textContent = payload.success ? tr('Repaired successfully') : tr('Repair failed');
    repairList.classList.add('hidden');
    repairOutput.textContent = payload.output || '';
    repairOutput.classList.toggle('failed', !payload.success);
    repairOutput.classList.remove('hidden');
    repairConfirmBtn.classList.add('hidden');
    repairCancelBtn.classList.add('hidden');
    repairCloseBtn.classList.remove('hidden');
    if (payload.success) showSuccess(tr('Repaired successfully')); else showError(tr('Repair failed'));
  } catch (err) {
    repairCount.textContent = trf('Repair failed: {msg}', {msg: err.message});
    repairConfirmBtn.disabled = false;
  }
}

/* ---------- 解除链接 ----------
   与修复相反的方向：把派生位置上的链接换成一份来自权威源的真实副本，再从清单里删掉这条记录。
   前端只负责「确认 + 逐次选择删除策略」，真正的文件系统操作与清单维护都在服务端完成：
   服务端把文件系统操作放在破坏性操作锁里、清单改动放在清单写锁里，顺序由它保证 */

/* 打开解除弹窗：target 为待解除的那条检测结果（必定是有效项，见 rowUnlinkBtn）
   有未保存改动时直接拒绝，理由与修复一致：解除针对的是磁盘上的清单，
   而页面里未保存的改动会让「列表里显示的路径」与「实际会被解除的记录」对不上 */
function openUnlinkModal(target) {
  if (!target) return;
  if (computeDiff().count > 0) {
    showError(tr('Save or discard your unsaved changes before unlinking'));
    return;
  }
  unlinkTarget = target;

  unlinkTitle.textContent = tr('Unlink');
  /* 逐字符列出要解除哪一条：设备、类型与「权威源 → 派生位置」的路径对，
     用户据此核对自己点的是不是想解除的那条记录 */
  unlinkList.innerHTML = '<div class="repair-item"><span class="repair-type">' + escHtml(target.type) + '</span> ' +
    escHtml(target.device) + '<br>' + escHtml(resultPaths(target)) + '</div>';

  /* 每次打开都重新决定复选框的状态，绝不沿用上一次的勾选：真实删除不可恢复。
     默认复位成「不勾」；但服务端以 --no-trash 启动时它是底线——那种情况下删除一律永久执行，
     因此强制勾上并禁用，并换成说明缘由的提示，避免给出一个取消后仍会永久删除的假开关 */
  unlinkNoTrash.checked = serverNoTrash;
  unlinkNoTrash.disabled = serverNoTrash;
  unlinkTrashHint.classList.toggle('hidden', serverNoTrash);
  unlinkForcedHint.classList.toggle('hidden', !serverNoTrash);

  /* 回到「确认」阶段：上一轮的结果输出与按钮状态不能残留 */
  unlinkList.classList.remove('hidden');
  unlinkOutput.classList.add('hidden');
  unlinkOutput.classList.remove('failed');
  unlinkOutput.textContent = '';
  unlinkConfirmBtn.classList.remove('hidden');
  unlinkConfirmBtn.disabled = false;
  unlinkCancelBtn.classList.remove('hidden');
  unlinkCloseBtn.classList.add('hidden');

  unlinkModal.classList.remove('hidden');
}

/* 执行解除：POST /api/unlink，请求体带 noTrash（复选框取值），
   然后把服务端返回的整段输出留在弹窗里直到用户关闭 */
async function doUnlink() {
  var target = unlinkTarget;
  if (!target) return;
  var body = {
    device: target.device,
    type: target.type,
    fields: resultFields(target),
    noTrash: unlinkNoTrash.checked
  };

  unlinkConfirmBtn.disabled = true;
  unlinkTitle.textContent = tr('Unlinking…');

  try {
    var resp = await apiFetch(UNLINK_API, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    });
    if (!resp.ok) { var txt = await resp.text(); throw new Error(txt || 'HTTP ' + resp.status); }
    var payload = await resp.json();

    /* 解除会改清单：重新拉一次配置，让那条记录从表格里消失、fs 状态也跟着重检。
       这一步不能省——服务端虽然广播了 updated，但那个事件在响应回来前就发出，
       本页的 rev 比对可能把它当成「本页自己的保存」而忽略，页面上就会留着一条已经删掉的记录 */
    await loadConfig();

    unlinkTitle.textContent = tr('Unlink result');
    unlinkList.classList.add('hidden');
    unlinkOutput.textContent = payload.output || '';
    unlinkOutput.classList.toggle('failed', !payload.success);
    unlinkOutput.classList.remove('hidden');
    unlinkConfirmBtn.classList.add('hidden');
    unlinkCancelBtn.classList.add('hidden');
    unlinkCloseBtn.classList.remove('hidden');
    if (payload.success) showSuccess(tr('Unlinked successfully')); else showError(tr('Unlink failed'));
  } catch (err) {
    /* 失败时把标题恢复成可识别的名字并把按钮解锁，用户可以直接重试或取消 */
    unlinkTitle.textContent = tr('Unlink');
    showError(trf('Unlink failed: {msg}', {msg: err.message}));
    unlinkConfirmBtn.disabled = false;
  }
}

/* 输入即写回：data 原地修改，不复制整份数组，避免每敲一个键就产生一份垃圾 */
function onCellInput(e) {
  var inp = e.target;
  var tr = inp.closest('tr');
  if (!tr) return;
  var entry = getEntries()[parseInt(tr.dataset.idx)];
  if (!entry) return;
  entry[inp.dataset.key] = inp.value;
  refreshDirtyUI();
}

/* 删除一行：结构性操作，与新增/拖拽/迁移一样走 renderTable 重建 */
function deleteRow(idx) {
  var arr = getEntries().slice();
  if (idx < 0 || idx >= arr.length) return;
  arr.splice(idx, 1);
  setEntries(arr);
  /* 清理被删条目的选中状态：删除位之后的下标整体前移 */
  var newSel = {};
  Object.keys(selectedIndices).forEach(function (k) {
    var ki = parseInt(k);
    if (ki < idx) newSel[ki] = selectedIndices[ki];
    if (ki > idx) newSel[ki - 1] = selectedIndices[ki];
  });
  selectedIndices = newSel;
  renderTable();
}

/* Drag & Drop */
/* 按下时先记住这次手势的起点，供 onDragStart 判断拖拽意图：
   行本身是 draggable 的，用户在输入框里按下拖动时，浏览器会把这次手势劫持成「拖动整行」——
   于是文字既选不中，行序还会被顺手改掉。这种情况下 dragstart 的 target 是 TR 而不是 INPUT，
   只看 e.target 会漏判，所以必须借助 mousedown 的落点 */
function onRowMouseDown(e) {
  dragOriginInInput = !!e.target.closest('.cell-input');
}

function onDragStart(e) {
  /* 输入框内的拖拽一律不是换序意图：既包括「在输入框里按下往外拖」，
     也包括「双击选中文字后从选中文字往外拖」（后者 dragstart 的 target 是 INPUT） */
  if (dragOriginInInput || e.target.closest('.cell-input')) {
    e.preventDefault();
    return;
  }
  var tr = e.target.closest('tr');
  if (!tr) return;
  dragSrcIdx = parseInt(tr.dataset.idx);
  tr.classList.add('dragging');
  e.dataTransfer.effectAllowed = 'move';
  e.dataTransfer.setData('text/plain', dragSrcIdx);
}

function onDragOver(e) {
  e.preventDefault();
  e.dataTransfer.dropEffect = 'move';
  var tr = e.target.closest('tr');
  if (!tr || parseInt(tr.dataset.idx) === dragSrcIdx) return;
  tr.classList.add('drag-over');
}

function onDragLeave(e) {
  var tr = e.target.closest('tr');
  if (tr) tr.classList.remove('drag-over');
}

function onDrop(e) {
  e.preventDefault();
  /* 没有经过认可的拖拽起点（例如被拦下的输入框拖拽）绝不能重排：
     dragSrcIdx 为 -1 时 splice(-1) 会把整份数据最后一条挪到目标行，静默改坏配置 */
  if (dragSrcIdx < 0) return;
  var tr = e.target.closest('tr');
  if (!tr) return;
  var targetIdx = parseInt(tr.dataset.idx);
  if (dragSrcIdx === targetIdx) return;
  var arr = getEntries().slice();
  var item = arr.splice(dragSrcIdx, 1)[0];
  arr.splice(targetIdx, 0, item);
  setEntries(arr);
  renderTable();
}

function onDragEnd(e) {
  var tr = e.target.closest('tr');
  if (tr) tr.classList.remove('dragging');
  tableBody.querySelectorAll('tr').forEach(function (t) { t.classList.remove('drag-over'); });
  dragSrcIdx = -1;
}

/* ---------- Add entry ---------- */
function addEntry() {
  var entry = {};
  (TYPE_FIELDS[currentType] || []).forEach(function (f) { entry[f.key] = ''; });
  var arr = getEntries().slice();
  arr.push(entry);
  setEntries(arr);
  /* 过滤态下新增：新行字段为空串，几乎不可能命中现有搜索词，
     若保留过滤用户会以为「新增没生效」——先清空过滤再渲染，保证新行可见 */
  if (filterText.trim()) setFilter('');
  renderTable();
  /* 滚动到底部 */
  var lastRow = tableBody.lastElementChild;
  if (lastRow) lastRow.scrollIntoView({ block: 'nearest' });
}

/* ---------- Add device ---------- */
function addDevice() {
  var name = newDeviceInput.value.trim();
  if (!name) { showError(tr('Device name must not be empty')); return; }
  if (name.indexOf(',') >= 0 || name.indexOf(' ') >= 0) {
    showError(tr('Device name must not contain commas or spaces')); return;
  }
  ensurePath(currentPlat, name);
  currentDev = name;
  newDeviceInput.value = '';
  addDeviceArea.classList.add('hidden');
  renderAll();
}

/* ---------- Migration ---------- */
function openMigrateModal() {
  var count = Object.keys(selectedIndices).filter(function (k) { return selectedIndices[k]; }).length;
  if (count === 0) { showError(tr('Select the entries to migrate first')); return; }
  migrateCount.textContent = trf('Selected {n} entries', {n: count});

  /* 填充目标平台列表 */
  var plats = getPlatforms();
  migrateTargetPlat.innerHTML = plats.map(function (p) {
    return '<option value="' + attrEsc(p) + '"' + (p === currentPlat ? ' selected' : '') + '>' + escHtml(p) + '</option>';
  }).join('');

  /* 填充目标类型列表 */
  migrateTargetType.innerHTML = TYPE_NAMES.map(function (t) {
    return '<option value="' + t + '"' + (t === currentType ? ' selected' : '') + '>' + t + '</option>';
  }).join('');

  /* 填充目标设备列表（根据当前选中的平台动态变化） */
  fillTargetDevs();

  migrateTargetPlat.onchange = fillTargetDevs;

  migrateModal.classList.remove('hidden');
}

function fillTargetDevs() {
  var plat = migrateTargetPlat.value;
  var devs = getDevices(plat);
  migrateTargetDev.innerHTML = devs.map(function (d) {
    return '<option value="' + attrEsc(d) + '">' + escHtml(d) + '</option>';
  }).join('');
  if (devs.length === 0) {
    /* 目标平台没有任何设备：只渲染一个空值占位项，提示用户先去目标平台创建设备，
       这里刻意不隐式造一个设备——设备名与初始类型都需要用户确认，
       悄悄造出来的设备既不符合用户意图，用户也无从得知它是谁在何时建的
       该占位项只承载提示文案、不携带有效取值，因此在 doMigrate 里一定会被
       targetDev 空值校验拦下并提示「请选择目标设备」，迁移流程不会在半途产生半成品数据
       若日后想让这里真正创建设备，必须同步考虑该占位项与 doMigrate 校验的分工，
       否则会出现「建了设备却仍提示未选择目标设备」的矛盾状态 */
    migrateTargetDev.innerHTML = '<option value="">' + tr('-- Create a device first --') + '</option>';
  }
}

function doMigrate() {
  var targetPlat = migrateTargetPlat.value;
  var targetDev  = migrateTargetDev.value;
  var targetType = migrateTargetType.value;
  var isMove = false;
  migrateActionRadios.forEach(function (r) { if (r.checked && r.value === 'move') isMove = true; });

  if (!targetDev) { showError(tr('Please select a target device')); return; }

  /* 确保目标路径存在 */
  ensurePath(targetPlat, targetDev);

  /* 收集选中的条目：data 里的值始终是最新的（输入即写回），无需再从 DOM 收集 */
  var sourceEntries = getEntries();
  var toMigrate = [];
  Object.keys(selectedIndices).sort(function (a, b) { return a - b; }).forEach(function (k) {
    if (selectedIndices[k] && sourceEntries[parseInt(k)]) {
      toMigrate.push(JSON.parse(JSON.stringify(sourceEntries[parseInt(k)])));
    }
  });

  if (toMigrate.length === 0) { showError(tr('No entries selected')); return; }

  /* 追加到目标 */
  var targetArr = data[targetPlat][targetDev][targetType] || [];
  toMigrate.forEach(function (e) { targetArr.push(e); });
  data[targetPlat][targetDev][targetType] = targetArr;

  /* 移动则从源删除 */
  if (isMove) {
    var remaining = [];
    sourceEntries.forEach(function (e, i) {
      if (!selectedIndices[i]) remaining.push(e);
    });
    setEntries(remaining);
    selectedIndices = {};
  }

  migrateModal.classList.add('hidden');
  renderTable();
  showSuccess(trf('Migration complete: {n} entries', {n: toMigrate.length}));
}

/* ---------- Navigation ---------- */
function renderAll() {
  renderPlatformTabs();
  renderDeviceTabs();
  renderTypeTabs();
  renderTable();
}

/* ---------- 冲突处理 ----------
   knownRev 与 SSE 事件里的 rev 相同 ⇒ 这次变更就是本页保存引起的，忽略；
   不同 ⇒ 页面外有人改了文件：有未保存改动就交给用户裁决，没有就直接重载 */
function showConflict(rev) {
  if (rev) pendingRev = rev;
  conflictBar.classList.remove('hidden');
}

function hideConflict() {
  conflictBar.classList.add('hidden');
  pendingRev = '';
}

function resolveConflict(reload) {
  var rev = pendingRev;
  conflictBar.classList.add('hidden');
  pendingRev = '';
  /* 无论选哪个出口，都要把 knownRev 推进到外部版本：
     选「保留我的改动」时不重载，但后续保存会整体覆盖磁盘内容，
     若不同步 knownRev，下一次轮询通知还会重复弹同一个提示条 */
  if (rev) knownRev = rev;
  if (reload) loadConfig(true);
}

/* ---------- Load / Save ---------- */
/* 读取文件元信息并同步 knownRev */
async function loadMeta() {
  try {
    var meta = await apiFetch(META_API).then(function (r) { return r.json(); });
    if (meta.storePath) storePathEl.textContent = trf('Path: {path}', {path: meta.storePath});
    var info = '';
    if (meta.modTime) info += trf('Modified: {time}', {time: meta.modTime});
    if (meta.fileSize) info += trf(' | Size: {size}', {size: meta.fileSize});
    fileInfo.textContent = info;
    /* 版本行不参与 i18n：品牌名与版本号没有可翻译的自然语言，缺字段时保持空白而不是显示半截文案 */
    if (meta.version) {
      versionInfo.textContent = 'flk ' + meta.version + (meta.platform ? ' · ' + meta.platform : '');
    }
    if (meta.rev) knownRev = meta.rev;
    /* 宿主平台族（linux-amd64 → linux）：仅用于默认平台页签的选择，
     取第一个连字符前的段——平台的命名约定就是 <family>-<arch>，见 pickDefaultPlatform */
    if (meta.platform) hostPlatform = String(meta.platform).split('-')[0];
    /* 服务端以 --no-trash 启动时为 'true'：解除弹窗据此把复选框置为勾选且禁用，
       让「本次会话一律真实删除」这条约定在界面上可见（缺失该字段时按 false 处理，
       即沿用旧行为——服务端与页面版本不一致时不该因此卡住解除操作） */
    if (meta.noTrash === 'true') serverNoTrash = true;
  } catch (_) {
    /* 元信息读取失败不影响配置展示，静默忽略 */
  }
}

/* 加载配置。force=true 用于「用户明确选择丢弃本地改动」的场景（冲突条的重新加载）
   非 force 时若存在未保存改动，绝不覆盖用户输入，改为弹出冲突提示条 */
async function loadConfig(force) {
  try {
    var resp = await apiFetch(STORE_API);
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    var fetched = await resp.json();

    if (!force && computeDiff().count > 0) {
      showConflict('');
      return;
    }

    data = fetched;
    /* 加载成功即视为「与磁盘一致」，快照既用于脏值计算也用于撤销 */
    originData = JSON.parse(JSON.stringify(data));
    hideConflict();
    await loadMeta();
    /* 快照版本与已知版本在此刻对齐，之后两者的差异就代表「磁盘又前进了一步」 */
    originRev = knownRev;

    /* 初始化导航到第一个有数据的平台/设备 */
    var plats = getPlatforms();
    if (plats.length > 0) {
      if (plats.indexOf(currentPlat) < 0) currentPlat = pickDefaultPlatform(plats);
      var devs = getDevices(currentPlat);
      if (devs.length > 0 && devs.indexOf(currentDev) < 0) currentDev = devs[0];
    }
    loading.classList.add('hidden');
    panel.classList.remove('hidden');
    renderAll();
    hideError();
    /* 这里刻意不再设置连接状态：绿点必须反映 SSE 的真实连接与否，
       由 connectSSE 的 onopen/onerror 独占负责，否则配置加载成功会造出一个假的「已连接」 */
    /* 配置加载完成后异步检测可用性，不阻塞主渲染；
       这也使 SSE updated 触发的 loadConfig 自动带动状态刷新，形成自动刷新闭环 */
    loadCheckStatus();
  } catch (err) {
    showError(trf('Failed to load the config: {msg}', {msg: err.message}));
  }
}

async function saveConfig() {
  saveBtn.disabled = true;
  saveBtn.textContent = tr('Saving…');
  hideError();
  /* 服务端在写响应体之前就广播了 SSE，而页面要等响应回来才知道本次写盘的 rev：
     这段窗口里若把 rev 变化当作「页面外被修改」，自己保存也会弹出冲突提示条。
     置位 saving 期间只暂存 rev、不弹条，等响应回来再比对 */
  saving = true;
  revDuringSave = '';

  try {
    /* 请求体在发起请求的这一刻固化：保存期间表格与「撤销改动」并未禁用，
       用户完全可能继续打字、删行、新增、拖拽换序，
       这些新改动不属于本次写盘内容，后续必须能与快照区分开 */
    var sentBody = JSON.stringify(data);
    var resp = await apiFetch(STORE_API, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: sentBody
    });
    if (!resp.ok) { var txt = await resp.text(); throw new Error(txt || 'HTTP ' + resp.status); }
    var payload = {};
    try { payload = await resp.json(); } catch (_) {}
    showSuccess(tr('Saved successfully'));
    /* 保存成功：快照必须等于「真正写进磁盘的那份内容」，即 sentBody，
       因此这里从固化的请求体重建快照，绝不读此刻的 data ——
       响应回来之前用户产生的新改动不在 sentBody 里，读当前 data 会把它们一起标成「已保存」，
       脏值随之归零、保存按钮消失，页面再也不会自愈，用户以为存上了、实际内容已丢
       用 sentBody 重建后，这些窗口内改动会继续显示为未保存，撤销改动同理（内存与磁盘分叉可见）
       版本号仍取响应里的 rev：磁盘此刻对应的就是 sentBody 这一版，
       这样紧随其后的 SSE 事件（rev 相同）会被识别为本页自己触发的而忽略 */
    originData = JSON.parse(sentBody);
    if (payload.rev) knownRev = payload.rev;
    originRev = knownRev;
    hideConflict();
    /* 只重绘表格不动导航，用户停留在当前平台/设备上；
       renderTable 内部会调用 refreshDirtyUI，保存按钮随之隐藏 */
    renderTable();
    /* 保存期间还观察到别的版本，说明外部确实也改过文件：
       此刻才按真冲突处理——还有未保存改动就提示，否则直接采纳磁盘内容 */
    var seenDuringSave = revDuringSave;
    revDuringSave = '';
    if (seenDuringSave && seenDuringSave !== knownRev) {
      if (computeDiff().count > 0) showConflict(seenDuringSave);
      else { knownRev = seenDuringSave; loadConfig(); }
    }
  } catch (err) {
    showError(trf('Save failed: {msg}', {msg: err.message}));
  } finally {
    saving = false;
    saveBtn.disabled = false;
    saveBtn.textContent = tr('Save');
  }
}

/* ---------- 撤销改动 ----------
   把 data 整份替换回快照：被删除的设备/平台可能因此复活，必须 renderAll 重建导航。
   快照本身也可能已经落后于磁盘（冲突待裁决，或用户先选了「保留我的改动」、磁盘随后又被外部改过）：
   本地改动既然已经放弃，就直接采纳磁盘内容，避免页面停在过期数据上再也不会自我纠正 */
function discardChanges() {
  if (!originData) return;
  data = JSON.parse(JSON.stringify(originData));
  renderAll();
  refreshDirtyUI();
  if (pendingRev || originRev !== knownRev) {
    hideConflict();
    loadConfig(true);
  }
}

/* ---------- SSE ----------
   连接必须在页面初始化时立刻建立：若延迟建立，这段时间内发生的文件变更事件会永久丢失，
   用户就会看到一份不弹冲突、也不自动重载的过期页面 */
function connectSSE() {
  /* token 只能走查询参数：EventSource 无法自定义请求头（见 apiURL 的说明），
     漏掉这一步 SSE 会静默 401——页面照常渲染，但实时更新再也不会到达 */
  var es = new EventSource(apiURL(EVENT_API));
  /* 连接状态只有一个来源：EventSource 自己。
     onerror 在浏览器自动重连期间也会触发，此时先如实显示「已断开」，重连成功后 onopen 再改回来 */
  es.onopen  = function () { setOnline(true); };
  es.onerror = function () { setOnline(false); };
  es.addEventListener('updated', function (e) {
    updateInfo.textContent = trf('Last updated: {time}', {time: new Date().toLocaleTimeString()});
    var rev = '';
    try { rev = (JSON.parse(e.data || '{}') || {}).rev || ''; } catch (_) { rev = ''; }
    /* rev 为空（服务端未提供版本）或与本页已知版本相同 ⇒ 视为本页自己的保存，忽略 */
    if (!rev || rev === knownRev) return;
    /* 本页正在保存：此刻的 rev 变化很可能就是自己这次写盘引起的（响应还没回来，knownRev 尚未更新），
       只暂存不弹条，等 saveConfig 拿到响应版本后再判断是不是真的外部改动 */
    if (saving) { revDuringSave = rev; return; }
    /* 首次加载尚未完成：此刻没有本地改动可言，直接整份重载即可 */
    if (!data) {
      knownRev = rev;
      loadConfig();
      return;
    }
    if (computeDiff().count > 0) {
      /* 有未保存改动：只提示，不重载，用户的输入原样保留 */
      showConflict(rev);
      return;
    }
    knownRev = rev;
    loadConfig();
  });
  /* 链接状态事件：服务端修完链接后广播，清单文件本身没有改动，因此不能走 updated 那条路
     （它的 rev 比对会把同版本事件当成「本页自己保存」忽略掉）。
     这里只需要重跑一次可用性检测把徽标刷新，不需要重载配置，也不会弹冲突提示条 */
  es.addEventListener('repaired', function () {
    if (!data) return;          /* 首次加载尚未完成，随后那次 loadConfig 会自带一次检测 */
    loadCheckStatus();
  });
  /* 语言事件：服务端语言已切换（可能是另一个标签页触发的），整页重载以对齐
     页面文案、注入的语言与命令树文案。本页自己发起的那次切换会先 reload，
     这条事件多半到不了，重复触发也无害 */
  es.addEventListener('language', function () { window.location.reload(); });
}

/* ---------- 语言切换 ----------
   服务端在 /api/language 里一次完成三件事：换翻译器、重译命令树、写回设置文件，
   然后广播 language 事件。前端这边最省事也最不易出错的收尾方式是**整页重载**：
   页面文案（applyI18N）、注入的 window.__FLK_LANG__、命令树文案三者一次对齐，
   不必自己判断"哪些 DOM 已经翻译过、哪些还是旧语言"——那正是最容易漏一处的地方 */
async function switchLanguage(lang) {
  langSelect.disabled = true;
  try {
    var resp = await apiFetch(LANGUAGE_API, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ language: lang })
    });
    if (!resp.ok) {
      /* 服务端的错误体是纯文本（http.Error），带一个结尾换行，去掉它再展示 */
      var txt = await resp.text();
      throw new Error((txt || ('HTTP ' + resp.status)).trim());
    }
    /* 重载而不是本地改文案：服务端已经全量生效并落盘，页面重新拉一次即可全部对齐 */
    window.location.reload();
  } catch (err) {
    showError(trf('Failed to switch language: {msg}', { msg: err.message }));
    /* 失败时把下拉框拨回当前语言：服务端此时可能已经回滚，留着新选中的值会让用户
       以为语言已经切过去了，而页面上的文案其实一点没变 */
    langSelect.value = LANG;
    langSelect.disabled = false;
  }
}

/* ---------- Init ---------- */
function init() {
  storePathEl.textContent = tr('Connecting to the server…');
  applyI18N();

  /* 下拉框对齐服务端注入的语言。它必然是 l10n.Current() 的取值，
     也就是与 options 里某一项逐字相同（见 pkg/l10n 的 Normalize） */
  langSelect.value = LANG;
  langSelect.addEventListener('change', function () { switchLanguage(langSelect.value); });

  recheckBtn.addEventListener('click', loadCheckStatus);
  saveBtn.addEventListener('click', saveConfig);

  /* 主题切换：点选即生效并持久化；初始高亮由 applyI18N 后的 syncThemeButtons 补上
     （<html data-theme> 的初始值已由 <head> 引导脚本写好，这里只同步按钮态） */
  themeButtons.forEach(function (btn) {
    btn.addEventListener('click', function () { applyThemeMode(btn.dataset.themeMode); });
  });
  syncThemeButtons();

  /* 路径过滤：输入即过滤（renderTable 只重渲染 tbody，不会抢表格输入框的焦点）；
     Esc 清空——输入框里按 Esc 是用户「不要这个过滤」最直觉的表达 */
  filterInput.addEventListener('input', function () { setFilter(filterInput.value); });
  filterInput.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') setFilter('');
  });
  filterClear.addEventListener('click', function () { setFilter(''); });
  discardBtn.addEventListener('click', discardChanges);
  conflictReloadBtn.addEventListener('click', function () { resolveConflict(true); });
  conflictKeepBtn.addEventListener('click', function () { resolveConflict(false); });
  addEntryBtn.addEventListener('click', addEntry);
  migrateBtn.addEventListener('click', openMigrateModal);
  migrateConfirmBtn.addEventListener('click', doMigrate);
  migrateCancelBtn.addEventListener('click', function () { migrateModal.classList.add('hidden'); });
  addDeviceBtn.addEventListener('click', addDevice);
  repairBtn.addEventListener('click', function () { openRepairModal(invalidResults(), true); });
  repairConfirmBtn.addEventListener('click', doRepair);
  repairCancelBtn.addEventListener('click', function () { repairModal.classList.add('hidden'); });
  repairCloseBtn.addEventListener('click', function () { repairModal.classList.add('hidden'); });
  unlinkConfirmBtn.addEventListener('click', doUnlink);
  unlinkCancelBtn.addEventListener('click', function () { unlinkModal.classList.add('hidden'); });
  unlinkCloseBtn.addEventListener('click', function () { unlinkModal.classList.add('hidden'); });
  cancelAddDeviceBtn.addEventListener('click', function () {
    addDeviceArea.classList.add('hidden');
    newDeviceInput.value = '';
  });
  newDeviceInput.addEventListener('keydown', function (e) {
    if (e.key === 'Enter') addDevice();
    if (e.key === 'Escape') { addDeviceArea.classList.add('hidden'); newDeviceInput.value = ''; }
  });

  /* 先接 SSE 再拉配置：SSE 不能晚于首次加载建立，否则建立前发生的文件变更会被永久漏掉，
     而 updated 事件万一早于首次加载到达，connectSSE 里对 data == null 的兜底会直接重载 */
  connectSSE();
  loadConfig();
}

init();

})();
