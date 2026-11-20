# SPanel 前端样式规范

> **admin 区 (方案 D, 2026-11-20)**: Tabler 面板框架换肤 — `/assets/vendor/tabler/css/tabler.min.css` (1.7.5 锁版本) + 品牌层 `/assets/css/spanel-admin.css`
> **user/auth 区 (方案 C, 仍有效)**: 自建设计系统 — `/assets/css/spanel.css`
> 图标全站统一 Material 连字: `<i class="mi">icon_name</i>` (本地字体)
> **页面内一律禁止**: 内联颜色 style、`<style>` 块、外链字体/CDN、原生 `alert()/confirm()`
> 新样式需求: admin → 扩充 `spanel-admin.css`; user/auth → 扩充 `spanel.css`

## 一、admin 页面模板 (方案 D — Tabler)

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>页面名 - SPanel</title>
  <script src="/assets/js/auth-guard.js"></script>
  <link rel="stylesheet" href="/assets/css/material-icons.css">
  <link rel="stylesheet" href="/assets/vendor/tabler/css/tabler.min.css">
  <link rel="stylesheet" href="/assets/css/spanel-admin.css">
  <script defer src="/assets/js/alpine.min.js"></script>
</head>
<body data-sp-area="admin" data-sp-page="users" [data-sp-active="tickets"]>
  <main class="sp-content">
    <div class="page-header d-print-none">
      <div class="row align-items-center">
        <div class="col">
          <h2 class="page-title">页面标题</h2>
          <div class="text-secondary mt-1">副文案 (可选)</div>
        </div>
        <div class="col-auto ms-auto d-print-none"><!-- 右上角主操作按钮 --></div>
      </div>
    </div>
    <!-- 页面内容: 卡片/表格/表单, 只用 Tabler 类 -->
  </main>
  <script src="/assets/js/api.js"></script>
  <script src="/assets/js/sp-ui.js"></script>
  <script src="/assets/js/sp-shell.js"></script>
  <script src="/assets/js/admin-table.js"></script>   <!-- 列表页才引 -->
  <script> /* Alpine 组件逻辑 (与旧版一致, alert() → SPUI.msg()) */ </script>
</body>
</html>
```

- 骨架由 `sp-shell.js` admin 分支注入: 深色左侧栏 (`aside.navbar-vertical` + 6 组菜单常展开) + 顶栏 + `page-wrapper`; `main.sp-content` 被移入 `page-body > .container-xl`, 自身 `display:contents`
- 侧栏/下拉/模态/toast 交互全部自研 class 切换, **不引入** `tabler.min.js` (方案 D2)
- 高亮: `data-sp-page` (或 `data-sp-active` 覆盖) → 对应 `li.nav-item.active`
- 反馈: `SPUI.msg/confirm/toast` API 不变 (内部已换 Tabler modal/toast 皮肤)
- ECharts 图表容器高度用品牌层类: `.sp-chart` + `.sp-chart-240/320/380` (禁内联 height)

### admin 组件速查 (Tabler)

| 用途 | 类 |
|---|---|
| 页头 | `.page-header > .row > .col (h2.page-title + .text-secondary)` + 右侧 `.col-auto.ms-auto` |
| 栅格 | `.row` + `.col-12 / .col-md-6 / .col-sm-6 .col-xl-3` (表单/统计卡按内容定阶) |
| 卡片 | `.card [.mb-3]` > `.card-header (h3.card-title)` + `.card-body`; 卡片组 `.row.row-cards` |
| 统计卡 | `.card > .card-body` > `.subheader`(名) + `.h1.mb-0`(值, 颜色 `text-blue/green/orange`) |
| 按钮 | `btn` + `btn-primary / btn-danger / btn-sm / w-100`; 次级用裸 `btn` |
| 表单 | `div.mb-3` > `label.form-label` + `input.form-control / select.form-select / textarea.form-control` |
| 键值行 | `.datagrid > .datagrid-item (.datagrid-title + .datagrid-content)` |
| 表格 (静态) | `.card > .table-responsive > table.table.table-vcenter.card-table` |
| 表格 (列表页) | `AdminTable(sel, cfg)` 自动渲染 (搜索/排序/分页/列显隐) |
| 角标 | `badge` + `bg-green-lt / bg-red-lt / bg-blue-lt / bg-orange-lt / bg-yellow-lt / bg-secondary-lt` |
| 状态点 | `span.status.status-green` 等 |
| 提示条 | `alert alert-info / alert-success / alert-danger / alert-warning` |
| 空态 | `.empty > .empty-header (i.mi) + .empty-title` |
| 标签页 | `ul.nav.nav-tabs > li.nav-item > a.nav-link(.active)` (切换仍 Alpine `@click`) |
| 进度条 | `.progress.progress-sm > .progress-bar.bg-orange [style width]` (宽度内联 style 允许) |
| 工具类 | `d-flex / gap-2 / flex-wrap / justify-content-between / ms-auto / text-secondary / text-break / font-monospace / text-wrap / w-auto / mb-3 / mt-3` |

### admin sp→Tabler 映射表 (转换依据)

| sp-* (旧) | Tabler (新) |
|---|---|
| `.sp-content-header + .sp-content-heading` | `.page-header` + `h2.page-title` (+ `.text-secondary` 副文案) |
| `.sp-grid / .sp-col-3/4/6…` | `.row` / `.col-sm-6 .col-xl-3` (统计卡) / `.col-md-6` (表单) — 按内容定阶 |
| `.sp-card` / `.sp-card-title` | `.card` / `.card-header > h3.card-title` |
| `.sp-btn -brand/-danger/-success/-flat/-sm/-block` | `btn btn-primary/danger/success/(裸 btn)/sm/w-100` |
| `.sp-input/.sp-select/.sp-textarea` | `form-control / form-select / form-control` |
| `.sp-form-row + .sp-form-label` | `div.mb-3` + `label.form-label` |
| `.sp-table-wrap > table.sp-table` | `.card > .table-responsive > table.table.table-vcenter.card-table` |
| `.sp-tag -orange/-green/-blue/-red/-yellow/-grey` | `badge bg-orange-lt/green-lt/blue-lt/red-lt/yellow-lt/secondary-lt` |
| `.sp-dot.green/…` | `status status-green` (span) |
| `.sp-alert-info/success/danger/warn` | `alert alert-info/success/danger/warning` |
| `.sp-empty` | `.empty > .empty-header + .empty-title` |
| `.sp-tabs > .sp-tab(.active)` | `ul.nav.nav-tabs > a.nav-link(.active)` |
| `.sp-chip(.active)` | 按语境: `btn btn-sm(.active)` 或 `badge` |
| `.sp-dl-row` | `.datagrid` (成组键值) |
| `.sp-text-muted/hint/danger` | `text-secondary / text-secondary / text-danger` |
| `.sp-mb-6 / .sp-mt-* / .sp-pull-right / .sp-break-all / .sp-num / .sp-wrap` | `mb-3 / mt-3 / ms-auto / text-break / font-monospace / text-wrap` |
| `.sp-container` | 移除 (骨架 container-xl 提供) |
| `tdClass: 'sp-num' / 'sp-wrap'` (AdminTable) | `tdClass: 'font-monospace' / 'text-wrap'` |
| 内联 `style="display:flex;gap:…"` 布局 | `d-flex flex-wrap gap-2 align-items-center justify-content-between` 等工具类 |

### admin 迁移铁律

1. Alpine 逻辑/接口调用/字段/端点**一行不改**, 只换 DOM 结构与类名
2. `alert()` 残留必须为 0; `<style>` 块必须为 0 (样式进 spanel-admin.css)
3. 页面不再引用 `spanel.css`; body 不再挂 `class="sp-admin"`
4. 图表高度/特殊样式用品牌层类; 自检: `grep -E 'spanel.css|<style|alert\(|sp-(btn|card|tag|grid|form|input|select|table|alert|empty|tabs|pagination|text-|m[bt]-|col-|dl-|dot|check|switch|wrap|num|pull|break|container|content-(header|inner))' 页面` 应无输出 (sp-content/sp-marquee/sp-menu-group 白名单除外, 后两者只在品牌层/骨架)

## 二、user/auth 页面模板 (方案 C — 自建皮肤, 未换肤)

```html
<script src="/assets/js/auth-guard.js"></script>
<link rel="stylesheet" href="/assets/css/material-icons.css">
<link rel="stylesheet" href="/assets/css/spanel.css">
<script defer src="/assets/js/alpine.min.js"></script>
...
<body data-sp-area="user" data-sp-page="dashboard">     <!-- auth 页用 data-sp-area="auth" -->
  <main class="sp-content"> ... sp-* 组件 ... </main>
```

- 脚本顺序固定: `api.js → sp-ui.js → sp-shell.js → 页面脚本`
- 抽屉高亮默认取 `data-sp-page`; 子页覆盖用 `data-sp-active`
- 组件速查见 `spanel.css` 分节注释 (栅格 `.sp-grid`、卡片 `.sp-card`、浮动标签 `.sp-field`、表格 `.sp-table-wrap` 等)
- /auth 页专属: `<body data-sp-area="auth" class="sp-auth-body">` + `.sp-authpage` 白卡结构 (见 spanel.css §13)
