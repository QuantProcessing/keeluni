# Hedger（对冲交易应用）

hedger 是与 trader 平级的应用：把两个交易所（或同一交易所）的两个标的作为**一组**开仓、跟踪、平仓。
它只通过 gateway 交易，状态存在自己的 PostgreSQL 库里；不需要 hedger 时 trader 照常运行。

```
browser ──cookie──> trader ──/api/hedge/*──bearer──> hedger ──bearer──> gateway ──> 交易所
                      └─────/api/gw/*────────────bearer──────────────> gateway
hedger ──> PostgreSQL（自己的库和账号）
```

## 概念

- **对冲组（group）**：两条腿（leg 1、leg 2），每条腿 = 账户 + 标的 + 开仓方向。一组有自己的状态、
  比例（ratio）、标签和备注。
- **比例**：`ratio` = 每 1 单位 leg 1 对应的 leg 2 数量，开仓时确定，之后的平仓、补腿都按它算。
  - 同一底层资产（BTC 对 BTC，PEPE 对 1000PEPE）：按数量匹配，ratio 由合约倍数决定（PEPE/1000PEPE = 0.001）。
  - 不同底层资产（BTC 对 ETH）：按开仓时的名义价值匹配，ratio = leg 2 数量 / leg 1 数量。
- **持仓永远是推导出来的**：每条腿的持有量 = 开仓方向成交 − 平仓方向成交（来自订单的累计成交量和成交记录），
  不单独存储，所以重启、丢包、推送缺失都不会让 hedger 相信一个交易所并不持有的对冲。
- **动作（action）**：open / close / unwind（含 trim）/ rehedge。同一组同一时间只有一个动作在执行
  （数据库唯一索引保证）。

### 状态

| 状态 | 含义 |
|---|---|
| `opening` / `closing` | 有动作在执行 |
| `open` | 两腿匹配（误差在两家步长允许范围内） |
| `broken` | 两腿不匹配，有方向敞口，需要处理（界面标为 Unhedged） |
| `closed` | 两腿都已平 |
| `failed` | 开仓一笔都没成交 |

## 执行

**双边市价**：两腿同时下市价单。

**一边限价一边市价**：限价腿（maker）挂单，每成交一部分就立刻在另一条腿按比例市价对冲；限价单完全成交后，
对冲腿直接补到目标数量，不留舍入零头。可选 Post only（只做 maker）。取消时撤掉限价单，已成交部分已经对冲。
对冲单被拒时立即撤掉限价单并停止（不会继续累积敞口）。

**下单安全**（与 gateway 约定一致）：

1. 每笔订单先写数据库（`hedge_orders`，状态 `sending`）再发送，clientOrderId 是唯一凭据。
2. 结果 `unknown`（超时、断线）时按 clientOrderId 查询，**绝不重下**；查询结果 `not_found` 只在下单 10 秒后才采信。
3. 进程重启后，每个执行中的动作重新接管：先按 clientOrderId 核对所有订单，再继续。市价动作中某条腿还没下单的，补下这一条腿。
4. 订单状态以交易所为准：账户推送实时更新，另有定时查询兜底（推送在线时 30 秒，离线时 5 秒）。
5. 动作结束后从交易所拉取成交记录，补齐手续费和精确成交价。

**一条腿失败时**（开仓时的选项）：

- `unwind`（默认）：自动把多出的那条腿减到与另一条腿相同（一条腿完全没成交时就是全部平掉）；平仓动作同理，自动补完。
  自动修复只做一次，再失败就停在 `broken` 等人处理。
- `hold`：保留，标记 `broken`，由交易员决定 Rehedge（把少的那条腿补足）、Trim（把多的那条腿减少）或 Unwind（全平）。

**数量**：可以按 leg 1 的币数量或按 U 名义价值输入。两腿都按各自交易所步长向下取整，取整后再反推，
保证任何一条腿都不超过另一条腿的等值；取整造成的名义差额（size mismatch）在下单前显示。低于任一腿最小数量或最小名义价值时拒绝。

## 交易员看到的数字

- **入场价差 / 当前价差 / 出场价差**（bps，同底层时）：leg 2 相对 leg 1 的价差；入场按均价，当前按标记价，
  出场按现在平仓会成交的价格（买一 / 卖一）。
- **净资费 APR**：空头腿收、多头腿付，按各自周期年化后相加，按单边名义价值计。另有 7 日均值（来自已结算资费）。
- **下次结算**：最近一次结算的倒计时和预计收付金额。
- **累计资费**：按交易所已结算资费 × 当时持有量估算（`hedge_funding`），精确数字以交易所账单为准。
- **净盈亏** = 未实现 + 已实现 − 手续费 + 资费。
- **每条腿**：持有量、均价、标记价、强平价及距离、杠杆、账户推送是否在线、该账户在该标的上的总仓位
  （交易所按账户+标的合并仓位，如果同一账户上还有对冲之外的仓位，这里会显示出来）。
- **成交质量**：每笔市价单相对下单时盘口价的滑点。

## API（`/v1`，Bearer token）

```
GET   /v1/health
GET   /v1/groups?days=7                 进行中的组 + 最近 N 天结束的组（含实时视图）
GET   /v1/groups/{id}                   一组的视图 + actions / orders / fills / events / funding
POST  /v1/preview                       {legs, size, unit, match, ratio?, mode, makerLeg, limitPrice?, tif?}
                                        → 数量、名义价值、比例、取整差额、两腿吃单均价与滑点、价差、净资费
POST  /v1/groups                        开仓（同 preview 的请求体 + policy, label, note）
PATCH /v1/groups/{id}                   {label, note}
POST  /v1/groups/{id}/close             {fraction (0,1], mode, makerLeg, limitPrice?, tif?}
POST  /v1/groups/{id}/cancel            取消执行中的限价动作
POST  /v1/groups/{id}/rehedge           把少的那条腿补足
POST  /v1/groups/{id}/trim              把多的那条腿减到匹配
POST  /v1/groups/{id}/unwind            两条腿全部市价平掉
GET   /v1/ws                            推送 {"ch":"groups","data":[...]}：有变化时（≤ 250ms 一次），活跃期间每 2 秒一次
```

trader 以 `/api/hedge/v1/*`、`/api/hedge/ws` 转发（同样的会话和 CSRF 校验）。

## 存储

PostgreSQL，表结构由程序内嵌的迁移脚本在启动时自动执行（`hedger_migrations` 记录版本，advisory lock 防止并发迁移）：

| 表 | 内容 |
|---|---|
| `hedge_groups` | 组：状态、比例、策略、标签、备注、问题说明 |
| `hedge_legs` | 两条腿：账户、标的、方向、步长等规则（开仓时快照） |
| `hedge_actions` | 每次开/平/修复：模式、目标数量、结果 |
| `hedge_orders` | 每笔订单：先写后发，clientOrderId 主键 |
| `hedge_fills` | 成交与手续费，(clientOrderId, fillId) 去重 |
| `hedge_events` | 每组的操作日志 |
| `hedge_funding` | 每次结算的资费估算 |

每个应用一个库、一个账号：`hedger initdb` 用 PostgreSQL 超级用户创建 `hedger` 角色（随机密码）和 `hedger` 库
（属主为 `hedger`，撤销 PUBLIC 的连接权限），把连接串写入 `data/hedger/database.url`（0600）。
以后的应用照此新增自己的 initdb。数据库不可用时 hedger 不能开新仓，但 gateway 和 trader 不受影响。

## 部署

- compose：`postgres`（只在内网）、`pg-secret`（首次生成超级用户密码）、`hedger-initdb`（一次性）、`hedger`。
  trader 的 `HEDGER_URL` / `HEDGER_TOKEN_FILE` 去掉即可不带对冲应用运行。
- 独立部署：hedger 只需要 gateway 地址和 token（`GATEWAY_URL`、`GATEWAY_TOKEN_FILE`）、一个数据库连接串
  （`HEDGER_DATABASE_URL` 或 `HEDGER_DATABASE_URL_FILE`），自己的 token 在 `data/hedger/token`。
- 本地：`POSTGRES_ADMIN_URL=postgres://... scripts/dev.sh` 第一次运行时初始化，之后直接 `scripts/dev.sh`。

## 已知限制

- 资费收付是估算值（结算时的费率 × 持有量 × 价格），未读取交易所的资费账单。
- 同一账户、同一标的上的对冲外仓位会被交易所合并；hedger 只管自己下的单，界面会提示。
- 现货腿只能做多（做空现货需要借币）。
- 限价动作不自动改价追单；需要时取消后重新挂。
