/* SPanel 共享骨架注入 — 方案 D (admin: Tabler 骨架 / user·auth: 方案 C 旧骨架)
 *
 * 页面用法:
 *   <body data-sp-area="admin" data-sp-page="users" [data-sp-active="tickets"]>
 *     <main class="sp-content"> ... 页面内容 (Tabler 类) ... </main>
 *     <script src="/assets/js/api.js"></script>
 *     <script src="/assets/js/sp-ui.js"></script>
 *     <script src="/assets/js/sp-shell.js"></script>
 *   </body>
 *
 * - area="admin" → 注入 Tabler 布局 (深色左侧栏 + 顶栏 + page-wrapper), main.sp-content 移入 page-body
 * - area="user"  → 注入橙色顶栏 + 左侧抽屉(五组菜单) + 页脚 marquee (方案 C 原样)
 * - area="auth"  → 仅注入 modal/toast 节点 (登录页自有蓝底白卡布局)
 * - data-sp-page 用于高亮侧栏当前项; data-sp-active 可覆盖 (如工单子页高亮"工单管理")
 * - admin: 侧栏/顶栏交互全部自研 class 切换, 不依赖 tabler.min.js (方案 D2)
 */
(function () {
  var body = document.body;
  var area = body.getAttribute('data-sp-area') || 'user';
  var pageKey = body.getAttribute('data-sp-active') || body.getAttribute('data-sp-page') || '';

  /* ---------- 抽屉菜单 (照抄旧版 main.tpl 五组, 图标/文案/顺序一致) ---------- */
  var MENU = [
    { title: '我的', items: [
      { key: 'dashboard',  href: '/user/',  icon: 'account_balance_wallet', text: '用户面板' },
      { key: 'invite',     href: '/user/invite.html',     icon: 'loyalty',                text: '邀请返利' }
    ]},
    { title: '商店', items: [
      { key: 'code',       href: '/user/code.html',       icon: 'code',                   text: '捐赠/充值' },
      { key: 'shop',       href: '/user/shop.html',       icon: 'shop',                   text: '套餐购买' },
      { key: 'bought',     href: '/user/bought.html',     icon: 'shopping_cart',          text: '购买记录' }
    ]},
    { title: '使用', items: [
      { key: 'tutorial',   href: '/user/tutorial.html',   icon: 'get_app',                text: '下载软件' },
      { key: 'rss',        href: '/user/rss.html',        icon: 'playlist_add',           text: '订阅节点' },
      { key: 'nodes',      href: '/user/nodes.html',      icon: 'airplanemode_active',    text: '节点列表' }
    ]},
    { title: '帮助', items: [
      { key: 'announcement', href: '/user/announcement.html', icon: 'announcement',      text: '网站公告' },
      { key: 'tickets',      href: '/user/tickets.html',      icon: 'question_answer',   text: '技术支持' }
    ]},
    { title: '账户', items: [
      { key: 'profile',    href: '/user/profile.html',    icon: 'account_box',           text: '账户信息' },
      { key: 'edit',       href: '/user/edit.html',       icon: 'sync_problem',          text: '个人设定' },
      { key: 'trafficlog', href: '/user/trafficlog.html', icon: 'hourglass_empty',       text: '流量记录' },
      { key: 'detect',     href: '/user/detect.html',     icon: 'account_balance',       text: '审计规则' },
      { key: 'detect-log', href: '/user/detect-log.html', icon: 'assignment_late',       text: '审计记录' }
    ]}
  ];

  function esc(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  /* ---------- 通用反馈节点 (modal + toast, 所有页面都需要; admin 用 Tabler 类) ---------- */
  function injectFeedbackNodes() {
    var div = document.createElement('div');
    if (area === 'admin') {
      /* Tabler modal (开合由 sp-ui.js 切换 .show / backdrop hidden, 不依赖 bootstrap JS) */
      div.innerHTML =
        '<div class="modal modal-blur modal-sm" id="spModal" aria-hidden="true">' +
          '<div class="modal-dialog modal-dialog-centered" role="dialog" aria-modal="true">' +
            '<div class="modal-content">' +
              '<div class="modal-body" id="spModalMsg"></div>' +
              '<div class="modal-footer">' +
                '<button class="btn" id="spModalCancel" style="display:none">取消</button>' +
                '<button class="btn btn-primary" id="spModalOk">知道了</button>' +
              '</div>' +
            '</div>' +
          '</div>' +
        '</div>' +
        '<div class="modal-backdrop" id="spModalMask" hidden></div>' +
        '<div class="sp-toast" id="spToast"></div>';
      while (div.firstChild) body.appendChild(div.firstChild);
      document.getElementById('spModalMask').addEventListener('click', function () {
        window.SPUI && SPUI.hide();
      });
      return;
    }
    div.innerHTML =
      '<div class="sp-modal" id="spModal" aria-hidden="true">' +
        '<div class="sp-modal-mask"></div>' +
        '<div class="sp-modal-dialog" role="dialog" aria-modal="true">' +
          '<div class="sp-modal-inner" id="spModalMsg"></div>' +
          '<div class="sp-modal-footer">' +
            '<button class="sp-btn sp-btn-flat" id="spModalCancel" style="margin-right:8px;display:none">取消</button>' +
            '<button class="sp-btn sp-btn-brand" id="spModalOk">知道了</button>' +
          '</div>' +
        '</div>' +
      '</div>' +
      '<div class="sp-toast" id="spToast"></div>';
    while (div.firstChild) body.appendChild(div.firstChild);
    document.querySelector('#spModal .sp-modal-mask').addEventListener('click', function () {
      window.SPUI && SPUI.hide();
    });
  }

  /* ---------- 用户区骨架 ---------- */
  function injectUserShell() {
    var u = (window.SP && SP.user()) || null;
    var isAdmin = !!(window.SP && SP.isAdmin && SP.isAdmin());
    /* 未登录 → 下拉为 登录/注册 (对齐旧版 main.tpl else 分支) */
    var userDropHtml = u
      ? '<a href="/user/"><i class="mi">account_box</i>用户中心</a>' +
        (isAdmin ? '<a href="/admin/"><i class="mi">dashboard</i>管理后台</a>' : '') +
        '<button class="sp-dd-danger" id="spLogoutBtn"><i class="mi">exit_to_app</i>登出</button>'
      : '<a href="/auth/login.html"><i class="mi">account_box</i>登录</a>' +
        '<a href="/auth/register.html"><i class="mi">person_add</i>注册</a>';
    injectAppShell(MENU, { userDropHtml: userDropHtml });
  }

  /* ---------- 管理区骨架 (方案 D: Tabler 布局 — 深色侧栏 + 顶栏 + page-wrapper) ---------- */
  var ADMIN_MENU = [
    { title: '我的', items: [
      { key: 'dashboard',         href: '/admin/',                      icon: 'business_center',   text: '系统概览' },
      { key: 'announcements',     href: '/admin/announcements.html',    icon: 'announcement',      text: '公告管理' },
      { key: 'tickets',           href: '/admin/tickets.html',          icon: 'question_answer',   text: '工单管理' },
      { key: 'autos',             href: '/admin/autos.html',            icon: 'flash_auto',        text: '下发命令' }
    ]},
    { title: '节点', items: [
      { key: 'nodes',             href: '/admin/nodes.html',            icon: 'router',            text: '节点列表' },
      { key: 'node-ctl',          href: '/admin/node-ctl.html',         icon: 'router',            text: '节点调整' },
      { key: 'node-group-traffic',href: '/admin/node-group-traffic.html', icon: 'show_chart',      text: '流量统计' },
      { key: 'node-decision',     href: '/admin/node-decision.html',    icon: 'assessment',        text: '节点决策台' },
      { key: 'traffic-log',       href: '/admin/traffic-log.html',      icon: 'traffic',           text: '流量记录' },
      { key: 'rss-headers',       href: '/admin/rss-headers.html',      icon: 'content_paste',     text: '订阅请求记录' },
      { key: 'blockips',          href: '/admin/blockips.html',         icon: 'dialer_sip',        text: '已封禁IP' },
      { key: 'unblockips',        href: '/admin/unblockips.html',       icon: 'dialer_sip',        text: '已解封IP' }
    ]},
    { title: '用户', items: [
      { key: 'users',             href: '/admin/users.html',            icon: 'supervisor_account', text: '用户列表' },
      { key: 'relays',            href: '/admin/relays.html',           icon: 'compare_arrows',    text: '中转规则' },
      { key: 'invites',           href: '/admin/invites.html',          icon: 'loyalty',            text: '邀请与返利' },
      { key: 'audit-login',       href: '/admin/audit-login.html',      icon: 'text_fields',        text: '登录记录' },
      { key: 'audit-alive',       href: '/admin/audit-alive.html',      icon: 'important_devices', text: '在线IP' },
      { key: 'user-log',          href: '/admin/user-log.html',         icon: 'history',           text: '用户操作记录' }
    ]},
    { title: '审计', items: [
      { key: 'detects',           href: '/admin/detects.html',          icon: 'account_balance',   text: '审计规则' },
      { key: 'detect-log',        href: '/admin/detect-log.html',       icon: 'assignment_late',   text: '审计记录' }
    ]},
    { title: '交易', items: [
      { key: 'codes',             href: '/admin/codes.html',            icon: 'code',              text: '充值与捐赠记录' },
      { key: 'shops',             href: '/admin/shops.html',            icon: 'shop',              text: '商品' },
      { key: 'coupons',           href: '/admin/coupons.html',          icon: 'card_giftcard',     text: '优惠码' },
      { key: 'boughts',           href: '/admin/boughts.html',          icon: 'shopping_cart',     text: '购买记录' },
      { key: 'recharge-stats',    href: '/admin/recharge-stats.html',   icon: 'equalizer',         text: '充值统计' }
    ]}
  ];

  function injectAdminShell() {
    var u = (window.SP && SP.user()) || null;
    var name = (u && (u.user_name || u.email)) || null;
    var year = new Date().getFullYear();
    var appName = (window.SP && SP.appName) || 'SPanel';

    /* 侧栏菜单: 分组常展开 (对齐旧版, 不用 Tabler dropdown 折叠) */
    var navHtml = '';
    ADMIN_MENU.forEach(function (g) {
      navHtml += '<li class="nav-item"><div class="nav-section-title sp-menu-group">' + esc(g.title) + '</div></li>';
      g.items.forEach(function (it) {
        navHtml += '<li class="nav-item"><a class="nav-link" href="' + it.href + '" data-key="' + it.key + '">' +
          '<i class="mi nav-link-icon">' + it.icon + '</i>' +
          '<span class="nav-link-title">' + esc(it.text) + '</span></a></li>';
      });
    });

    /* 顶栏用户下拉 (旧 admin/main.tpl: 个人资料/用户中心/登出; 自研 .show 切换) */
    var dropHtml = u
      ? '<a class="dropdown-item" href="/admin/profile.html"><i class="mi">account_box</i>个人资料</a>' +
        '<a class="dropdown-item" href="/user/"><i class="mi">person</i>用户中心</a>' +
        '<div class="dropdown-divider"></div>' +
        '<button class="dropdown-item text-danger" id="spLogoutBtn" type="button"><i class="mi">exit_to_app</i>登出</button>'
      : '<a class="dropdown-item" href="/auth/login.html"><i class="mi">account_box</i>登录</a>';

    /* 顶栏右侧: ADMIN 徽标 + 用户下拉 */
    var toolsHtml =
      '<span class="badge bg-blue-lt d-none d-md-inline-flex">ADMIN</span>' +
      '<div class="nav-item dropdown" id="spUserDrop">' +
        '<button class="nav-link d-flex align-items-center gap-2" id="spUserBtn" type="button">' +
          '<i class="mi">account_circle</i>' +
          '<span class="nav-link-title">' + esc(name || '未登录') + '</span>' +
          '<i class="mi">expand_more</i>' +
        '</button>' +
        '<div class="dropdown-menu dropdown-menu-end" data-tblr-popper id="spUserMenu">' + dropHtml + '</div>' +
      '</div>';

    var shell = document.createElement('div');
    shell.className = 'page';
    shell.innerHTML =
      '<aside class="navbar navbar-vertical navbar-expand-lg" data-bs-theme="dark" id="spDrawer">' +
        '<div class="container-fluid">' +
          '<div class="navbar-brand"><i class="mi">person_pin</i><span>' + esc(appName) + ' 管理面板</span></div>' +
          '<div class="navbar-collapse">' +
            '<ul class="navbar-nav pt-lg-3">' + navHtml + '</ul>' +
          '</div>' +
          '<div class="navbar-footer">' +
            '<div class="navbar-nav">' +
              '<a class="nav-link" href="/user/"><i class="mi nav-link-icon">person</i>' +
                '<span class="nav-link-title">用户中心</span></a>' +
              '<button class="nav-link sp-fold-btn" id="spSidebarFold" type="button">' +
                '<i class="mi nav-link-icon">chevron_left</i>' +
                '<span class="nav-link-title">收起菜单</span></button>' +
            '</div>' +
          '</div>' +
        '</div>' +
      '</aside>' +
      '<div class="page-wrapper">' +
        '<header class="navbar navbar-expand-md d-print-none">' +
          '<div class="container-xl">' +
            '<div class="d-flex align-items-center flex-grow-1 d-lg-none">' +
              '<button class="btn" id="spDrawerToggle" type="button" aria-label="打开菜单"><i class="mi">menu</i></button>' +
              '<a class="navbar-brand" href="/admin/"><i class="mi">person_pin</i><span>管理面板</span></a>' +
            '</div>' +
            '<div class="navbar-nav flex-row order-md-last ms-auto align-items-center gap-2">' + toolsHtml + '</div>' +
          '</div>' +
        '</header>' +
        '<div class="page-body"><div class="container-xl"></div></div>' +
        '<footer class="footer footer-transparent d-print-none">' +
          '<div class="container-xl"><div class="sp-marquee">&copy;' + year + ' ' + esc(appName) +
            ' | Powered by <a href="/">SSP</a></div></div>' +
        '</footer>' +
      '</div>';
    body.insertBefore(shell, body.firstChild);

    /* 遮罩 (移动端侧栏) */
    var mask = document.createElement('div');
    mask.className = 'sp-drawer-mask';
    mask.id = 'spDrawerMask';
    body.appendChild(mask);

    /* 内容骨架: main.sp-content 移入 page-body 容器 (Tabler container-xl 接管宽度) */
    var container = shell.querySelector('.page-body .container-xl');
    var content = document.querySelector('main.sp-content');
    if (content) container.appendChild(content);

    /* 当前页高亮 (li.active 携带 Tabler 侧栏激活态) */
    if (pageKey) {
      shell.querySelectorAll('.navbar-nav a.nav-link[data-key]').forEach(function (a) {
        if (a.getAttribute('data-key') === pageKey) {
          a.classList.add('active');
          var li = a.closest('li');
          if (li) li.classList.add('active');
        }
      });
    }

    /* 交互: 侧栏折叠 (桌面) / 侧栏开合 (移动端) / 用户下拉 / 登出 — 全部自研切换 */
    var drawer = document.getElementById('spDrawer');

    /* 侧栏折叠 (condensed): Tabler .navbar-folded-hover — 平时 4rem 图标栏, 悬停展开; 偏好记忆 localStorage */
    var FOLD_KEY = 'sp.admin.sidebarFolded';
    var foldBtn = document.getElementById('spSidebarFold');
    var folded = false;
    try { folded = localStorage.getItem(FOLD_KEY) === '1'; } catch (e) {}
    function applyFold(f) {
      drawer.classList.toggle('navbar-folded-hover', f);
      if (foldBtn) {
        foldBtn.querySelector('.mi').textContent = f ? 'chevron_right' : 'chevron_left';
        var foldTitle = foldBtn.querySelector('.nav-link-title');
        if (foldTitle) foldTitle.textContent = f ? '展开菜单' : '收起菜单';
      }
    }
    applyFold(folded);
    if (foldBtn) foldBtn.addEventListener('click', function () {
      folded = !folded;
      try { localStorage.setItem(FOLD_KEY, folded ? '1' : '0'); } catch (e) {}
      applyFold(folded);
    });

    var toggleBtn = document.getElementById('spDrawerToggle');
    function openDrawer(o) {
      drawer.classList.toggle('open', o);
      mask.classList.toggle('show', o);
    }
    if (toggleBtn) toggleBtn.addEventListener('click', function () { openDrawer(!drawer.classList.contains('open')); });
    mask.addEventListener('click', function () { openDrawer(false); });

    var userBtn = document.getElementById('spUserBtn');
    var userMenu = document.getElementById('spUserMenu');
    if (userBtn) {
      userBtn.addEventListener('click', function (e) {
        e.stopPropagation();
        userMenu.classList.toggle('show');
      });
    }
    document.addEventListener('click', function () { userMenu && userMenu.classList.remove('show'); });

    var logoutBtn = document.getElementById('spLogoutBtn');
    if (logoutBtn) logoutBtn.addEventListener('click', function () { window.SP && SP.logout(); });
  }

  /* ---------- 通用骨架注入 (用户区/管理区共用) ---------- */
  function injectAppShell(menu, cfg) {
    cfg = cfg || {};
    var u = (window.SP && SP.user()) || null;
    var name = (u && (u.user_name || u.email)) || null;
    var year = new Date().getFullYear();
    var appName = (window.SP && SP.appName) || 'SPanel';

    /* 顶栏 (复刻旧 header-orange: 左菜单钮+logo / 右用户下拉) */
    var header = document.createElement('header');
    header.className = 'sp-header';
    header.innerHTML =
      '<div class="sp-header-inner">' +
        '<div class="sp-header-left">' +
          '<button class="sp-header-menu-btn" id="spDrawerToggle" aria-label="菜单"><i class="mi">menu</i></button>' +
          '<a class="sp-header-logo" href="/"><i class="mi">language</i><span>' + esc(appName) + '</span>' + (cfg.headerBadge || '') + '</a>' +
        '</div>' +
        '<div class="sp-header-user" id="spUserDrop">' +
          '<button class="sp-header-user-toggle" id="spUserBtn">' +
            '<span class="sp-header-user-name">' + esc(name || '未登录') + '</span>' +
            '<i class="mi">account_circle</i>' +
          '</button>' +
          '<div class="sp-dropdown" id="spUserMenu">' + cfg.userDropHtml + '</div>' +
        '</div>' +
      '</div>';
    body.insertBefore(header, body.firstChild);

    /* 左抽屉 (复刻旧 nav-drawer-md, 分组默认展开 "collapse in") */
    var logoIcon = cfg.drawerLogoIcon || 'language';
    var logoText = cfg.drawerLogoText != null ? cfg.drawerLogoText : appName;
    var navHtml = '<a class="sp-drawer-logo" href="/"><i class="mi">' + logoIcon + '</i><span>' + esc(logoText) + '</span></a><ul class="sp-drawer-nav">';
    menu.forEach(function (g) {
      navHtml += '<li class="sp-drawer-group">' +
        '<div class="sp-drawer-group-title">' + esc(g.title) + '<i class="mi sp-arrow">expand_more</i></div><ul class="sp-drawer-sub">';
      g.items.forEach(function (it) {
        navHtml += '<li><a href="' + it.href + '" data-key="' + it.key + '">' +
          '<i class="mi">' + it.icon + '</i>&nbsp;' + esc(it.text) + '</a></li>';
      });
      navHtml += '</ul></li>';
    });
    navHtml += '</ul>';
    if (cfg.drawerExtra) navHtml += '<div class="sp-drawer-extra">' + cfg.drawerExtra + '</div>';
    var drawer = document.createElement('nav');
    drawer.className = 'sp-drawer';
    drawer.id = 'spDrawer';
    drawer.innerHTML = navHtml;
    body.appendChild(drawer);

    var mask = document.createElement('div');
    mask.className = 'sp-drawer-mask';
    mask.id = 'spDrawerMask';
    body.appendChild(mask);

    /* 页脚 marquee (复刻旧 ui-footer: ©年份 站名 | Powered by SSP) */
    var footer = document.createElement('footer');
    footer.className = 'sp-footer';
    footer.innerHTML = '<div class="sp-container"><div class="sp-marquee">&copy;' + year + ' ' + esc(appName) +
      ' | Powered by <a href="/">SSP</a></div></div>';
    body.appendChild(footer);

    /* 内容骨架: header 之后包一层, 让桌面端内容右侧偏移 (抽屉常驻) */
    var content = document.querySelector('main.sp-content');
    if (content) {
      var wrap = document.createElement('div');
      wrap.className = 'sp-content-body';
      content.parentNode.insertBefore(wrap, content);
      var holder = document.createElement('div');
      holder.className = 'sp-drawer-holder';
      wrap.appendChild(holder);
      wrap.appendChild(content);
    }

    /* 当前页高亮 */
    if (pageKey) {
      drawer.querySelectorAll('.sp-drawer-sub a').forEach(function (a) {
        if (a.getAttribute('data-key') === pageKey) a.classList.add('active');
      });
    }

    /* 交互: 抽屉开合 (移动端) / 分组折叠 / 用户下拉 / 登出 */
    var toggleBtn = document.getElementById('spDrawerToggle');
    function openDrawer(o) {
      drawer.classList.toggle('open', o);
      mask.classList.toggle('show', o);
    }
    if (toggleBtn) toggleBtn.addEventListener('click', function () { openDrawer(!drawer.classList.contains('open')); });
    mask.addEventListener('click', function () { openDrawer(false); });
    drawer.querySelectorAll('.sp-drawer-group-title').forEach(function (t) {
      t.addEventListener('click', function () { t.parentNode.classList.toggle('collapsed'); });
    });

    var userBtn = document.getElementById('spUserBtn');
    var userMenu = document.getElementById('spUserMenu');
    if (userBtn) {
      userBtn.addEventListener('click', function (e) {
        e.stopPropagation();
        userMenu.classList.toggle('open');
      });
    }
    document.addEventListener('click', function () { userMenu && userMenu.classList.remove('open'); });

    var logoutBtn = document.getElementById('spLogoutBtn');
    if (logoutBtn) logoutBtn.addEventListener('click', function () { window.SP && SP.logout(); });
  }

  /* ---------- 启动 ---------- */
  injectFeedbackNodes();
  if (area === 'user') injectUserShell();
  else if (area === 'admin') injectAdminShell();
})();
