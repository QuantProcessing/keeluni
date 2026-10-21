# 前端

`web/` 是 React + TypeScript 的单页应用，由 trader 提供静态文件。它不持有任何凭证，所有请求同源发往 trader。

## 技术栈

| 项 | 选择 |
|---|---|
| 框架 | React 19、TypeScript（严格模式、`noUncheckedIndexedAccess`） |
| 构建 | Vite；开发服务器把 `/api` 代理到运行中的 trader |
| 样式 | Tailwind CSS 4，设计变量见[设计规范](./design) |
| 数据 | TanStack Query（REST 缓存）；自己的 WebSocket 客户端（`api/realtime.ts`，单连接多订阅、自动重连） |
| 图表 | lightweight-charts（K 线、价差图）；自研画线层（`chart-drawings.ts`） |
| 组件 | Radix（Popover、Dialog） |
| 测试 | Vitest + jsdom |

## 目录

```
src/
  app.tsx                 会话门、顶栏、交易页布局、登录页、状态栏
  api/
    client.ts             gateway REST 客户端；ApiError；newClientOrderId
    realtime.ts           WebSocket：订阅、重连、保留最后一条
    hedge.ts              hedger 客户端；useHedgeGroups（推送）；useMatching；useHedgeHoldings
    options.ts            期权链（useOptionSeries / useOptionChain / useOptionQuote）与 optioner 客户端（useOptionerStream）
    types.ts              gateway 的线上类型
  state/
    workspace.tsx         工作区：所选标的、全部账户与其实时状态、路由、委托与成交、交易账户选择、连接对话框
    hooks.ts              useInstruments / useBook / useTicker / useFunding / useAccountStreams …
    route.ts              极简的前端路由（/、/options、/hedge）
  features/
    market-header.tsx     行情头与市场选择器（InstrumentPicker，交易页与对冲页共用）
    chart.tsx             K 线与画线工具
    order-book.tsx        盘口与成交
    order-ticket.tsx      下单面板与调整杠杆
    account-picker.tsx    账户选择（AccountPicker / AccountMenu / NoAccount，交易页与对冲页共用）
    account-summary.tsx   账户概览与保证金池
    blotter.tsx           资产 / 持仓 / 委托 / 成交（全部账户）
    connect.tsx           连接交易所对话框、账户菜单
    submit.ts             下单与结果未知时的查询
    hedge/                对冲页（组合栏、价差图、腿盘口、下单、对冲列表）
    options/              期权页（系列栏、到期日、T 型报价、微笑与期限结构、组合下单、组合风险）；按需加载
  lib/
    format.ts             数字、价格、时间、交易所名称
    routes.ts             账户能否交易标的、账户选择、路由规则、权益、保证金等级
    storage.ts            浏览器存储迁移（bolter.* → keeluni.*）
  i18n/                   中英文字典、hedger 消息渲染
  ui/                     基础组件、Logo、Toast
```

## 状态与数据流

- **会话**：`Gate` 组件查询 `/api/auth/session`，未登录显示登录页；登录后启动 WebSocket。
- **工作区**（`WorkspaceProvider`）：
  - 订阅**全部账户**的私有流（`useAccountStreams`），持仓、余额、状态、保证金以完整快照替换；
  - 每个账户的路由（`/accounts/{id}/instruments`）加载一次并缓存；
  - 当前委托与成交：每个账户一次 REST 快照，之后由推送更新；
  - 交易账户：候选 = 能交易所选标的的账户（原生在前、经纪在后）；选择按交易所记住（`keeluni.tradeAccount`），候选变化时只回退到候选中的账户，不会切换到无关账户；
  - 本终端下的订单记住 10 分钟，期间收到异步拒单会弹出提示。
- **行情**：`useBook`、`useTicker` 等通过 `realtime` 订阅，同一频道在页面内也只订阅一次。

## 下单流程（submit.ts）

1. 生成 clientOrderId（每次提交一次）。
2. `POST /orders`：
   - 4xx（交易所之前即被拒）→ 明确拒绝；
   - `accepted` → 成功；`rejected` → 拒绝，附交易所代码；
   - 网络失败或 5xx、`unknown` → 进入查询。
3. 查询：间隔 0.5、1.5、3 秒按 clientOrderId 查询，`found` 即成功，`not_found` 即未下出；仍不确定则提示「结果未知，重试前请先检查当前委托」。**任何情况下都不重下。**

## 浏览器存储

| 键 | 内容 |
|---|---|
| `keeluni.lang` | 界面语言 |
| `keeluni.instrument` | 交易页所选标的 |
| `keeluni.tradeAccount` | 每个交易所上次选择的交易账户 |
| `keeluni.accountFilter` | 底部表格的账户筛选 |
| `keeluni.interval` / `keeluni.chart.magnet` / `keeluni.drawings.<标的>` | K 线周期、磁吸、画线 |
| `keeluni.hedge.pair` / `keeluni.hedge.interval` | 对冲组合与价差图周期 |

改名前保存在 `bolter.*` 下的值在首次加载时迁移一次。
