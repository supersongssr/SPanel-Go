/* AdminTable — 管理端服务端分页表格组件 (2026-11-18 admin 移植方案 A5)
 * DataTables serverSide UX 等价: 排序/搜索防抖/页大小(含"全部")/列显隐 localStorage/分页。
 * 对接 /api/v2/admin 统一列表契约:
 *   GET <api>?page=1&page_size=25&order_by=id&order_dir=desc&search=xxx[&页面自定义参数]
 *   → {ret:1, data:{items:[...], total:N}}
 *
 * 用法:
 *   AdminTable('#tbl', {
 *     api: '/api/v2/admin/user',
 *     stateKey: 'adm-users',                 // localStorage 前缀 (列显隐/页大小)
 *     columns: [
 *       {key:'id', title:'ID', sortable:true},
 *       {key:'enable', title:'状态', render: r => r.enable===1?'<span class="badge bg-green-lt">可用</span>':'<span class="badge bg-red-lt">禁用</span>'},
 *     ],
 *     defaultOrder: ['id','asc'],
 *     search: true,                           // 或 '搜索 id/用户名/邮箱'
 *     ops: r => '<a class="btn btn-sm" href="...">编辑</a>',   // 行操作列 (首列, 不可排序/隐藏)
 *     params: () => ({status: window.xxx}),   // 每次加载附加的查询参数
 *   });
 * 依赖: api.js (SPanelAPI), sp-ui.js (SPUI), tabler.min.css + spanel-admin.css (方案 D)
 */
(function () {
  function esc(s) {
    if (s === null || s === undefined) return '';
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function store(key, fallback) {
    try {
      var v = localStorage.getItem(key);
      return v === null ? fallback : JSON.parse(v);
    } catch (e) { return fallback; }
  }
  function save(key, v) {
    try { localStorage.setItem(key, JSON.stringify(v)); } catch (e) {}
  }

  function AdminTable(selector, opts) {
    var mount = typeof selector === 'string' ? document.querySelector(selector) : selector;
    if (!mount) { console.error('[AdminTable] 挂载点不存在:', selector); return null; }

    var cols = opts.columns || [];
    var hasOps = typeof opts.ops === 'function';
    var stateKey = opts.stateKey || ('at:' + opts.api);
    var pageSizeOpts = opts.pageSizes || [10, 25, 50, 100, 0];
    /* ---- 状态 (页大小/列显隐持久化; 未存过的列回退 opts.columns[].hide 默认) ---- */
    var defaults = {};
    cols.forEach(function (c) { if (c.hide) defaults[c.key] = true; });
    var defaultPS = pageSizeOpts.indexOf(25) >= 0 ? 25 : pageSizeOpts[0];
    var st = {
      page: 1,
      pageSize: (function () {
        var v = store(stateKey + ':ps', defaultPS);
        return pageSizeOpts.indexOf(v) >= 0 ? v : defaultPS;
      })(),
      order: opts.defaultOrder || [firstSortable(), 'asc'],
      search: '',
      hidden: (function () {
        var saved = store(stateKey + ':cols', null);
        if (saved && typeof saved === 'object') {
          var merged = {};
          cols.forEach(function (c) {
            merged[c.key] = Object.prototype.hasOwnProperty.call(saved, c.key) ? !!saved[c.key] : !!defaults[c.key];
          });
          return merged;
        }
        return defaults;
      })()
    };
    if (!Array.isArray(st.order) || !st.order[0]) st.order = [firstSortable(), 'asc'];
    if (pageSizeOpts.indexOf(st.pageSize) < 0) st.pageSize = defaultPS;

    function firstSortable() {
      for (var i = 0; i < cols.length; i++) if (cols[i].sortable) return cols[i].key;
      return cols.length ? cols[0].key : 'id';
    }
    function colVisible(i, key) { return !st.hidden[key]; }
    function resetCols() {
      try { localStorage.removeItem(stateKey + ':cols'); } catch (e) {}
      st.hidden = {};
      cols.forEach(function (c) { if (c.hide) st.hidden[c.key] = true; });
      $colPanel.querySelectorAll('input').forEach(function (t) {
        var i = parseInt(t.getAttribute('data-i'), 10);
        t.checked = colVisible(i, cols[i].key);
      });
      applyColVisibility();
    }

    /* ---- DOM (方案 D: Tabler card 结构 — card-header 工具栏 + table-responsive 表体 + card-footer 分页) ---- */
    mount.classList.add('card', 'sp-adm');
    mount.innerHTML =
      '<div class="card-header d-flex flex-wrap gap-2 align-items-center justify-content-between">' +
        (opts.search === false ? '' :
          '<div class="input-icon flex-grow-1" style="min-width:220px;max-width:360px">' +
          '<span class="input-icon-addon"><i class="mi">search</i></span>' +
          '<input type="text" class="form-control" placeholder="' + esc(opts.search === true ? '搜索…' : opts.search) + '"></div>') +
        '<div class="d-flex align-items-center gap-2">' +
          '<select class="form-select form-select-sm w-auto" title="每页条数">' +
          pageSizeOpts.map(function (n) {
            return '<option value="' + n + '"' + (n === st.pageSize ? ' selected' : '') + '>' + (n === 0 ? '全部' : n + ' 条/页') + '</option>';
          }).join('') +
          '</select>' +
          '<div class="dropdown sp-adm-cols">' +
            '<button type="button" class="btn btn-sm dropdown-toggle"><i class="mi">visibility</i>列显示</button>' +
            '<div class="dropdown-menu dropdown-menu-end sp-adm-colpanel"></div>' +
          '</div>' +
        '</div>' +
      '</div>' +
      '<div class="table-responsive position-relative">' +
        '<table class="table table-vcenter card-table"><thead></thead><tbody></tbody></table>' +
        '<div class="sp-adm-loading"><div class="spinner-border spinner-border-sm"></div>加载中…</div>' +
      '</div>' +
      '<div class="card-footer d-print-none">' +
        '<div class="d-flex flex-wrap gap-2 align-items-center justify-content-between">' +
          '<div class="text-secondary sp-adm-info"></div>' +
          '<ul class="pagination pagination-sm m-0 sp-pagination"></ul>' +
        '</div>' +
      '</div>';

    var $thead = mount.querySelector('thead');
    var $tbody = mount.querySelector('tbody');
    var $info = mount.querySelector('.sp-adm-info');
    var $pager = mount.querySelector('.sp-pagination');
    var $loading = mount.querySelector('.sp-adm-loading');
    var $search = mount.querySelector('.input-icon input');
    var $ps = mount.querySelector('.card-header select');
    var $colBtn = mount.querySelector('.sp-adm-cols button');
    var $colPanel = mount.querySelector('.sp-adm-colpanel');

    /* ---- 列显示面板 (自研 .show 切换, 无 bootstrap JS) ---- */
    $colPanel.innerHTML = '<button class="dropdown-item text-primary" type="button" data-reset="1">' +
      '<i class="mi">restart_alt</i>重置默认列</button>' +
      '<div class="dropdown-divider"></div>' +
      cols.map(function (c, i) {
        return '<label class="dropdown-item"><input type="checkbox" data-i="' + i + '"' + (colVisible(i, c.key) ? ' checked' : '') + '>' + esc(c.title || c.key) + '</label>';
      }).join('');
    $colBtn.addEventListener('click', function (e) {
      e.stopPropagation();
      $colPanel.classList.toggle('show');
    });
    $colPanel.addEventListener('click', function (e) {
      e.stopPropagation();
      if (e.target.closest('[data-reset]')) { resetCols(); return; }
    });
    document.addEventListener('click', function () { $colPanel.classList.remove('show'); });
    $colPanel.addEventListener('change', function (e) {
      var t = e.target;
      if (t.tagName !== 'INPUT') return;
      var i = parseInt(t.getAttribute('data-i'), 10);
      st.hidden[cols[i].key] = !t.checked;
      save(stateKey + ':cols', st.hidden);
      applyColVisibility();
    });

    /* ---- 表头 (排序指示走 Tabler th>button.table-sort .asc/.desc) ---- */
    function renderHead() {
      var html = '<tr>';
      if (hasOps) html += '<th>操作</th>';
      cols.forEach(function (c) {
        var sortable = c.sortable !== false; // 默认可排序
        if (!sortable) { html += '<th>' + esc(c.title || c.key) + '</th>'; return; }
        var sorted = st.order[0] === c.key;
        var cls = 'table-sort' + (sorted ? (st.order[1] === 'desc' ? ' desc' : ' asc') : '');
        html += '<th><button type="button" class="' + cls + '" data-key="' + esc(c.key) + '">' + esc(c.title || c.key) + '</button></th>';
      });
      html += '</tr>';
      $thead.innerHTML = html;

      $thead.querySelectorAll('button.table-sort').forEach(function (btn) {
        var key = btn.getAttribute('data-key');
        var colDef = null;
        cols.forEach(function (c) { if (c.key === key) colDef = c; });
        if (!colDef || colDef.sortable === false) return;
        btn.addEventListener('click', function () {
          if (st.order[0] === key) st.order[1] = st.order[1] === 'asc' ? 'desc' : 'asc';
          else st.order = [key, 'asc'];
          st.page = 1;
          load();
        });
      });
      applyColVisibility();
    }

    function applyColVisibility() {
      var ths = $thead.querySelectorAll('th');
      var idx = 0;
      ths.forEach(function (th) {
        var i = hasOps ? idx - 1 : idx;
        var hide = false;
        if (hasOps && idx === 0) hide = false;
        else hide = !colVisible(i, cols[i].key);
        th.style.display = hide ? 'none' : '';
        idx++;
      });
      $tbody.querySelectorAll('tr').forEach(function (tr) {
        var tds = tr.querySelectorAll('td');
        var j = 0;
        tds.forEach(function (td) {
          var i = hasOps ? j - 1 : j;
          var hide = false;
          if (hasOps && j === 0) hide = false;
          else hide = !colVisible(i, cols[i].key);
          td.style.display = hide ? 'none' : '';
          j++;
        });
      });
    }

    /* ---- 数据加载 ---- */
    var seq = 0;
    function load() {
      var mySeq = ++seq;
      var params = {
        page: st.page,
        page_size: st.pageSize,
        order_by: st.order[0],
        order_dir: st.order[1]
      };
      if (st.search) params.search = st.search;
      if (typeof opts.params === 'function') {
        try {
          var extra = opts.params() || {};
          Object.keys(extra).forEach(function (k) {
            if (extra[k] !== '' && extra[k] !== null && extra[k] !== undefined) params[k] = extra[k];
          });
        } catch (e) { console.error('[AdminTable] params()', e); }
      }
      var qs = Object.keys(params).map(function (k) {
        return encodeURIComponent(k) + '=' + encodeURIComponent(params[k]);
      }).join('&');

      $loading.classList.add('show');
      SPanelAPI.request(opts.api + '?' + qs).then(function (res) {
        if (mySeq !== seq) return; // 过期响应
        $loading.classList.remove('show');
        var data = (res && res.data) || {};
        var items = data.items || [];
        var total = data.total || 0;
        renderRows(items);
        renderFoot(total);
        if (typeof opts.afterLoad === 'function') opts.afterLoad(items, total);
      }).catch(function (err) {
        if (mySeq !== seq) return;
        $loading.classList.remove('show');
        renderRows([]);
        renderFoot(0);
        SPUI.toast(err.message || '加载失败', 'error');
      });
    }

    function renderRows(items) {
      if (!items.length) {
        $tbody.innerHTML = '<tr><td colspan="' + (cols.length + (hasOps ? 1 : 0)) + '">' +
          '<div class="empty"><div class="empty-header"><i class="mi">inbox</i></div>' +
          '<div class="empty-title">暂无数据</div></div></td></tr>';
        return;
      }
      var html = '';
      items.forEach(function (row) {
        html += '<tr>';
        if (hasOps) html += '<td><div class="sp-adm-ops">' + opts.ops(row) + '</div></td>';
        cols.forEach(function (c) {
          var v = typeof c.render === 'function' ? c.render(row) : esc(row[c.key]);
          html += '<td' + (c.tdClass ? ' class="' + esc(c.tdClass) + '"' : '') + '>' + (v === null || v === undefined ? '' : v) + '</td>';
        });
        html += '</tr>';
      });
      $tbody.innerHTML = html;
      applyColVisibility();
    }

    function renderFoot(total) {
      var pages = st.pageSize > 0 ? Math.max(1, Math.ceil(total / st.pageSize)) : 1;
      if (st.page > pages) st.page = pages;
      var from, to;
      if (total === 0) { from = to = 0; }
      else if (st.pageSize > 0) { from = (st.page - 1) * st.pageSize + 1; to = Math.min(st.page * st.pageSize, total); }
      else { from = 1; to = total; }
      $info.textContent = '共 ' + total + ' 条' + (st.pageSize > 0 ? ' · 第 ' + from + '-' + to + ' 条' : '');

      /* 页码窗口: ‹ 1 … p-1 p p+1 … N › (Tabler pagination 结构) */
      var li = function (p, text, on, dis, span) {
        var inner = span ? '<span class="page-link">' + text + '</span>'
                         : '<a class="page-link" href="javascript:void(0)" data-p="' + p + '">' + text + '</a>';
        return '<li class="page-item' + (on ? ' active' : '') + (dis ? ' disabled' : '') + '">' + inner + '</li>';
      };
      var h = li(st.page - 1, '‹', false, st.page <= 1);
      var win = 7;
      var s = Math.max(1, st.page - 3);
      var e = Math.min(pages, s + win - 1);
      s = Math.max(1, Math.min(s, e - win + 1));
      if (s > 1) { h += li(1, '1', st.page === 1); if (s > 2) h += li(0, '…', false, true, true); }
      for (var p = s; p <= e; p++) h += li(p, p, p === st.page);
      if (e < pages) { if (e < pages - 1) h += li(0, '…', false, true, true); h += li(pages, pages, false); }
      h += li(st.page + 1, '›', false, st.page >= pages);
      $pager.innerHTML = st.pageSize > 0 && pages > 1 ? h : '';
      $pager.querySelectorAll('a.page-link').forEach(function (b) {
        b.addEventListener('click', function () {
          var p = parseInt(b.getAttribute('data-p'), 10);
          if (p >= 1 && p <= pages && p !== st.page) { st.page = p; load(); }
        });
      });
    }

    /* ---- 工具栏事件 ---- */
    if ($search) {
      var timer = null;
      $search.addEventListener('input', function () {
        clearTimeout(timer);
        timer = setTimeout(function () {
          st.search = $search.value.trim();
          st.page = 1;
          load();
        }, 300);
      });
      $search.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') { clearTimeout(timer); st.search = $search.value.trim(); st.page = 1; load(); }
      });
    }
    $ps.addEventListener('change', function () {
      st.pageSize = parseInt($ps.value, 10);
      save(stateKey + ':ps', st.pageSize);
      st.page = 1;
      load();
    });

    /* ---- 启动 ---- */
    renderHead();
    load();

    return {
      reload: load,
      state: st
    };
  }

  window.AdminTable = AdminTable;
  window.esc = esc; // 全局 HTML 转义工具 (各列表页 render/ops 复用)
})();
