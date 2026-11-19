// SPanel Global Unified AJAX Communication Core
// §4.0 方案 B: 凭据 = HttpOnly Cookie `spanel_token` (同源 fetch 自动携带, 无需手动注入)
// Bearer 通道保留为调试兜底 (localStorage spanel_jwt 若存在则附加)。
// 401 → 清残留态跳登录; 403 + data.redirect → 禁用页等定向跳转。

(function () {
  const API_BASE = '/api/v1';

  window.SPanelAPI = {
    /**
     * 底层统一的 HTTP 请求 Fetch 拦截封装
     */
    async request(endpoint, options = {}) {
      // 以 http 开头 → 完整 URL; 以 /api/ 开头 → 站内绝对路径 (admin v2 等跨版本端点);
      // 其余 → 拼接 API_BASE (如 '/user/x' → '/api/v1/user/x')
      const url = (endpoint.startsWith('http') || endpoint.startsWith('/api/'))
        ? endpoint : `${API_BASE}${endpoint}`;

      // 合并 headers; 调试兜底: localStorage 中的 token 仍以 Bearer 附加 (非主通道)
      const headers = {
        'Content-Type': 'application/json',
        ...options.headers,
      };
      const dbgToken = localStorage.getItem('spanel_jwt');
      if (dbgToken) {
        headers['Authorization'] = `Bearer ${dbgToken}`;
      }

      const fetchOptions = {
        ...options,
        headers,
        credentials: 'same-origin', // HttpOnly Cookie 同源自动携带
      };

      try {
        const response = await fetch(url, fetchOptions);

        // 强力拦截 401 会话凭证失效错误，瞬间清除过期缓存并强制越权跳转。
        // 豁免 /api/v1/auth/* 端点: 其 401 是业务失败 (如登录密码错误),
        // 必须透传后端真实 msg, 否则登录页会误报 "会话过期"。
        const isAuthEndpoint = url.includes('/api/v1/auth/');
        if (response.status === 401 && !isAuthEndpoint) {
          localStorage.removeItem('spanel_jwt');
          localStorage.removeItem('spanel_user');

          // 如果当前不是在登录页，强制跳转
          if (!window.location.pathname.includes('/auth/')) {
            window.location.replace('/auth/login.html');
          }
          throw new Error('会话过期，请重新登录。');
        }

        const data = await response.json();

        // 403 + data.redirect (禁用页等定向跳转, §6.3)
        if (response.status === 403 && data && data.data && data.data.redirect) {
          window.location.replace(data.data.redirect);
          throw new Error(data.msg || '访问受限');
        }

        // Normalize Go backend format (ret/msg) to ElysiaJS/frontend expected format (status/message)
        if (typeof data.ret !== 'undefined') {
          data.status = data.ret === 1 ? 'success' : 'error';
        }
        if (typeof data.msg !== 'undefined' && typeof data.message === 'undefined') {
          data.message = data.msg;
        }

        if (response.ok && data.status === 'success') {
          return data;
        } else {
          // 附带原始信封 (如 ret=0 + nextid 的工单队列推进场景需要读取附加字段)
          const err = new Error(data.message || '网络请求响应异常，请稍后再试。');
          err.data = data;
          throw err;
        }
      } catch (err) {
        // 保持 err.data (上面手动构造的错误不应被覆盖)
        if (!(err instanceof Error)) {
          console.error('[API Connection Error]:', err && err.message);
        } else {
          console.error('[API Connection Error]:', err.message);
        }
        throw err;
      }
    },
  };
})();
