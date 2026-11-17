/* SPanel UI 反馈层 — 统一替代原生 alert/confirm (方案 C, 对应旧版 #result modal "知道了")
 * 依赖: sp-shell.js 注入的 #spModal / #spToast 节点 (任意页面引入本文件即可用)
 * API:
 *   SPUI.msg(text, onOk)           弹窗提示, "知道了" 按钮 (对应旧版 $("#result").modal())
 *   SPUI.confirm(text, onOk, opt)  确认弹窗 { okText, cancelText, danger }
 *   SPUI.toast(text, type)         轻提示 (success|error, 默认纯文本)
 */
(function () {
  function el(id) { return document.getElementById(id); }

  function showModal(text, opts) {
    opts = opts || {};
    var modal = el('spModal');
    if (!modal) { console.warn('[SPUI] shell 未注入, 降级 alert:', text); alert(text); return; }
    el('spModalMsg').innerHTML = text;
    var btnOk = el('spModalOk');
    var btnCancel = el('spModalCancel');
    btnOk.textContent = opts.okText || '知道了';
    btnCancel.style.display = opts.showCancel ? '' : 'none';
    if (opts.danger) { btnOk.className = 'sp-btn sp-btn-danger'; }
    else { btnOk.className = 'sp-btn sp-btn-brand'; }
    btnOk.onclick = function () {
      hide();
      if (typeof opts.onOk === 'function') opts.onOk();
    };
    btnCancel.onclick = hide;
    modal.classList.add('show');
    document.addEventListener('keydown', escClose);
    btnOk.focus();
  }
  function escClose(e) { if (e.key === 'Escape') hide(); }
  function hide() {
    var modal = el('spModal');
    if (modal) modal.classList.remove('show');
    document.removeEventListener('keydown', escClose);
  }

  var toastTimer = null;
  function toast(text, type) {
    var t = el('spToast');
    if (!t) { console.warn('[SPUI] shell 未注入, 降级 alert:', text); alert(text); return; }
    t.className = 'sp-toast show ' + (type || '');
    t.innerHTML = (type === 'success' ? '<i class="mi">check_circle</i>' :
                   type === 'error' ? '<i class="mi">error</i>' : '') + text;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.className = 'sp-toast'; }, 2600);
  }

  window.SPUI = {
    msg: function (text, onOk) { showModal(text, { onOk: onOk }); },
    confirm: function (text, onOk, opts) {
      opts = opts || {};
      opts.onOk = onOk; opts.showCancel = true;
      showModal(text, opts);
    },
    toast: toast,
    hide: hide
  };

  /* 全局工具 */
  window.SP = {
    appName: window.SP_APP_NAME || 'SPanel',
    /* 登出: POST 后端清 cookie → 清残留 → 回登录页 (对应旧 /user/logout) */
    logout: function () {
      fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'same-origin' })
        .catch(function () {})
        .finally(function () {
          localStorage.removeItem('spanel_jwt');
          localStorage.removeItem('spanel_user');
          window.location.replace('/auth/login.html');
        });
    },
    user: function () {
      try { return JSON.parse(localStorage.getItem('spanel_user') || 'null'); }
      catch (e) { localStorage.removeItem('spanel_user'); return null; }
    },
    isAdmin: function () {
      var u = SP.user();
      return !!(u && (u.isAdmin === true || u.isAdmin === 1 || u.is_admin === 1));
    }
  };
})();
