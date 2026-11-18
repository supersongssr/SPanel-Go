/* SPanel 共享骨架注入 — 方案 C (单点维护, 复刻旧版 main.tpl 信息架构)
 *
 * 页面用法:
 *   <body data-sp-area="user" data-sp-page="dashboard" [data-sp-active="tickets"]>
 *     <main class="sp-content"> ... 页面内容 ... </main>
 *     <script src="/assets/js/api.js"></script>
 *     <script src="/assets/js/sp-ui.js"></script>
 *     <script src="/assets/js/sp-shell.js"></script>
 *   </body>
 *
 * - area="user"  → 注入橙色顶栏 + 左侧抽屉(五组菜单) + 页脚 marquee
 * - area="auth"  → 仅注入 modal/toast 节点 (登录页自有蓝底白卡布局)
 * - data-sp-page 用于高亮抽屉当前项; data-sp-active 可覆盖 (如工单子页高亮"技术支持")
 * - 抽屉/顶栏菜单 = 旧版 user/main.tpl 原样结构 (我的/商店/使用/帮助/账户)
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

  /* ---------- 通用反馈节点 (modal + toast, 所有页面都需要) ---------- */
  function injectFeedbackNodes() {
    var div = document.createElement('div');
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
    var name = (u && (u.user_name || u.email)) || null;
    var isAdmin = !!(window.SP && SP.isAdmin && SP.isAdmin());
    var year = new Date().getFullYear();
    var appName = (window.SP && SP.appName) || 'SPanel';
    /* 未登录 → 下拉为 登录/注册 (对齐旧版 main.tpl else 分支) */
    var userDropHtml = name
      ? '<a href="/user/"><i class="mi">account_box</i>用户中心</a>' +
        (isAdmin ? '<a href="/admin/dashboard.html"><i class="mi">dashboard</i>管理后台</a>' : '') +
        '<button class="sp-dd-danger" id="spLogoutBtn"><i class="mi">exit_to_app</i>登出</button>'
      : '<a href="/auth/login.html"><i class="mi">account_box</i>登录</a>' +
        '<a href="/auth/register.html"><i class="mi">person_add</i>注册</a>';

    /* 顶栏 (复刻旧 header-orange: 左菜单钮+logo / 右用户下拉) */
    var header = document.createElement('header');
    header.className = 'sp-header';
    header.innerHTML =
      '<div class="sp-header-inner">' +
        '<div class="sp-header-left">' +
          '<button class="sp-header-menu-btn" id="spDrawerToggle" aria-label="菜单"><i class="mi">menu</i></button>' +
          '<a class="sp-header-logo" href="/"><i class="mi">language</i><span>' + esc(appName) + '</span></a>' +
        '</div>' +
        '<div class="sp-header-user" id="spUserDrop">' +
          '<button class="sp-header-user-toggle" id="spUserBtn">' +
            '<span class="sp-header-user-name">' + esc(name || '未登录') + '</span>' +
            '<i class="mi">account_circle</i>' +
          '</button>' +
          '<div class="sp-dropdown" id="spUserMenu">' + userDropHtml + '</div>' +
        '</div>' +
      '</div>';
    body.insertBefore(header, body.firstChild);

    /* 左抽屉 (复刻旧 nav-drawer-md, 分组默认展开 "collapse in") */
    var navHtml = '<a class="sp-drawer-logo" href="/"><i class="mi">language</i><span>' + esc(appName) + '</span></a><ul class="sp-drawer-nav">';
    MENU.forEach(function (g) {
      navHtml += '<li class="sp-drawer-group">' +
        '<div class="sp-drawer-group-title">' + esc(g.title) + '<i class="mi sp-arrow">expand_more</i></div><ul class="sp-drawer-sub">';
      g.items.forEach(function (it) {
        navHtml += '<li><a href="' + it.href + '" data-key="' + it.key + '">' +
          '<i class="mi">' + it.icon + '</i>&nbsp;' + esc(it.text) + '</a></li>';
      });
      navHtml += '</ul></li>';
    });
    navHtml += '</ul>';
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
})();
