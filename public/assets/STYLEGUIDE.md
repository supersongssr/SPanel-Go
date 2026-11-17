# SPanel 前端样式规范 (方案 C: 骨架照抄旧版 + 自建设计系统)

> 全站唯一样式来源: `/assets/css/spanel.css` + `/assets/css/material-icons.css`
> **页面内禁止**: Tailwind 类、内联 style 颜色、`<style>` 块、FontAwesome、Google Fonts、原生 `alert()/confirm()`
> 图标一律 Material 连字: `<i class="mi">icon_name</i>` (与旧版观感一致)

## 页面模板

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>页面名 - SPanel</title>
  <script src="/assets/js/auth-guard.js"></script>
  <link rel="stylesheet" href="/assets/css/material-icons.css">
  <link rel="stylesheet" href="/assets/css/spanel.css">
  <script defer src="/assets/js/alpine.min.js"></script>
</head>
<body data-sp-area="user" data-sp-page="dashboard">     <!-- auth 页用 data-sp-area="auth" -->
  <main class="sp-content">
    <div class="sp-content-header">
      <div class="sp-container"><h1 class="sp-content-heading">用户中心 ★</h1></div>
    </div>
    <div class="sp-content-inner">
      <!-- 页面内容: 只用下方组件类 -->
    </div>
  </main>
  <script src="/assets/js/api.js"></script>
  <script src="/assets/js/sp-ui.js"></script>
  <script src="/assets/js/sp-shell.js"></script>
  <script> /* Alpine 组件: 保留原逻辑, alert() → SPUI.msg() */ </script>
</body>
</html>
```

- 脚本顺序固定: `api.js → sp-ui.js → sp-shell.js → 页面脚本`
- 抽屉高亮默认取 `data-sp-page`; 子页覆盖用 `data-sp-active` (如 ticket-view → `data-sp-active="tickets"`)
- Alpine 逻辑(x-data/方法/API 调用)**原样保留**, 只换 DOM 骨架与类名; `alert('xx')` → `SPUI.msg('xx')`
  需要用户确认的删除类操作 → `SPUI.confirm('确定删除?', () => {...}, {danger:true, okText:'删除'})`
  轻量成功提示可用 `SPUI.toast('已复制', 'success')`

## 组件速查 (全部定义见 spanel.css)

| 用途 | 类 |
|---|---|
| 栅格 | `.sp-grid` + `.sp-col-12/9/8/6/4/3` |
| 卡片 | `.sp-card` (标题 `.sp-card-title`, 标题右侧操作 `.sp-extra`) |
| 仪表盘统计卡 | `.sp-card.sp-stat` → `.sp-stat-head(.sp-stat-name + a.sp-tag-orange)` `.sp-stat-value` `.sp-stat-icon.red/green/yellow/blue > i.mi` `.sp-stat-foot` |
| 按钮 | `.sp-btn` + `-brand(靛蓝主按钮)/-flat/-danger/-success/-block/-sm/-lg` |
| 浮动标签输入 | `.sp-field > input[placeholder=" "] + label` (焦点/有值自动上浮) |
| 普通表单 | `.sp-form-row > label.sp-form-label + .sp-input/.sp-select/.sp-textarea` |
| 复选/开关 | `.sp-check` / `.sp-switch` |
| 表格 | `.sp-table-wrap > table.sp-table` (th/td 自动样式, 溢出自动横向滚动) |
| 角标/状态 | `.sp-tag-orange/green/blue/red/yellow/grey`, `.sp-dot.green/red/yellow/grey` |
| 提示条 | `.sp-alert-info/success/danger/warn` (内放 `<i class="mi">info</i>` + 文案) |
| 空态 | `.sp-empty` (`<i class="mi">inbox</i>` + 文案) |
| 进度条 | `.sp-progress > i[style=width:x%]` (颜色变体 `-orange/-green/-red`) |
| 标签页 | `.sp-tabs > button.sp-tab(.active)`; 药丸选择 `.sp-chip(.active)` |
| 分页 | `.sp-pagination > button(.on)` |
| 教程步骤 | `ol.sp-steps` |
| 工具类 | `.sp-container .sp-text-muted .sp-text-hint .sp-mt-2/4/6 .sp-mb-6 .sp-pull-right .sp-break-all` |

## /auth 页专属

`<body data-sp-area="auth" class="sp-auth-body">`, 内容:
```
<div class="sp-authpage"><div class="sp-auth-main">
  <div class="sp-auth-top">
    <a class="sp-board-btn sp-left" href="/">首 页</a>
    <div class="sp-auth-logo"><img src="/images/authlogo.jpg" alt=""></div>
    <a class="sp-board-btn sp-right" href="/auth/register.html">注 册</a>
  </div>
  <div class="sp-auth-body"> ... .sp-field 表单 ... </div>
</div></div>
```
主按钮 "确认登录" = `.sp-btn.sp-btn-brand.sp-btn-block`; 记住我/忘记密码行 = `.sp-auth-help`。

## 迁移铁律

1. 保留全部 Alpine 逻辑与 API 调用, 不改任何业务行为 (字段/端点/跳转)
2. 页面标题照抄旧版 `content-heading` (见方案文档 §4 映射表)
3. 删除所有 Tailwind/FontAwesome/Google Fonts 引用与类名
4. `alert` 计数必须为 0; 深色 slate-* 类必须为 0
5. 迁移后自检: `grep -lE 'tailwindcss|font-awesome|googleapis|slate-|alert\(' 目标页` 应无输出
