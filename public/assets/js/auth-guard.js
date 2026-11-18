// SPanel Anti-Flicker Front-End Navigation Guard Shield
// §4.0 方案 B 判据改造: 登录态凭证在 HttpOnly Cookie 中 (JS 不可读),
// 前端以 spanel_user 摘要 (非凭证) 存在性做预判 —— 服务端中间件才是真防线。
// 同步阻塞未授权渲染, 保证零内容闪烁。

(function () {
  let userStr = localStorage.getItem('spanel_user');
  // 兼容: 旧 Bearer 调试 token 残留时一并视为摘要缺失 (cookie 才是真凭据)
  localStorage.removeItem('spanel_jwt');

  let user = null;
  try {
    user = userStr ? JSON.parse(userStr) : null;
  } catch (e) {
    localStorage.removeItem('spanel_user');
    user = null;
  }

  const path = window.location.pathname;
  const authed = !!user;
  const isAdmin = !!(user && (user.isAdmin === true || user.isAdmin === 1 || user.is_admin === 1));

  const jump = (to) => {
    document.documentElement.style.display = 'none';
    window.location.replace(to);
  };

  // 1. 会员中心页面: 未登录前置拦截
  if (path.includes('/user/')) {
    if (!authed) {
      jump('/auth/login.html');
      return;
    }
  }

  // 2. 管理后台页面: 需管理员身份
  if (path.includes('/admin/')) {
    if (!authed || !isAdmin) {
      jump('/auth/login.html');
      return;
    }
  }

  // 3. 已具备有效会话的用户进入认证页 (登录/注册) 时, 正向业务分流
  if (path.includes('/auth/')) {
    if (authed && !path.includes('logout') && !path.includes('reset') && !path.includes('token')) {
      jump(isAdmin ? '/admin/dashboard.html' : '/user/');
      return;
    }
  }
})();
