# spreader 设计文档：低频价差套利

> 状态：设计稿（分支 `spreader`）。本文是实现依据；实现过程中的变更回写本文。

## 1. 目标与边界

**做什么**：在两个市场之间持续监控价差，当价差相对其**公允值**出现**持续的**偏离、且偏离足以覆盖成本时，按规则自动（或手动）开一组对冲仓位，偏离回归后平仓；循环往复，并统计每个周期的盈亏。

**面向谁**：低频、看「持续机会」的交易者。机会以分钟到天计，不与高频和做市争夺毫秒级的瞬时价差。

**不做什么**：

- 不做高频：不建本地强一致订单簿，不做亚秒级反应。价差以 gateway 推送的订单簿（≤100 ms 一次、前 50 档）为准，每秒评估一次。
- 不做跨三个及以上市场的组合、不做统计套利篮子。
- 资金费率套利仍归 hedger（对冲页）；spreader 关心价差本身，资金费只作为持仓成本计入。

## 2. 依据：真实价差长什么样

2026-10-21 用 gateway 的 K 线测得（收盘价，15 分钟 × 9 天 / 1 小时 × 37 天）：

| 组合 | 均值 | 标准差 | 半衰期 | 说明 |
|---|---|---|---|---|
| Binance BTC-USDT / BTC-USDC（同所） | −10.5 bps | 1.5 | 93 分钟 | 稳定的**结构性偏移**：USDT/USDC 汇率 |
| Binance BTC / Kraken（USD 结算） | −6.8（9 天）；37 天里均值 +1.3 → −6.0 | 2.7–4.3 | 18 分钟–26 小时 | 结构性偏移且**缓慢漂移** |
| Binance BTC / Deribit（USDC） | −4.3（9 天）；37 天 +3.6 → −3.6 | 1.7–4.3 | 40 分钟–9 小时 | 同上 |
| 现货 / 永续（Binance、OKX BTC） | −4.5 | 0.7 | 12–15 分钟 | 稳定基差，主要由资金费率决定 |
| Binance / Bybit BTC（同为 USDT） | −0.1 | 0.7 | 16 分钟 | 无结构性偏移，只有噪声 |
| 长尾币（ENA、TRUMP、ETHFI 跨所） | ±3–7 | 3.6–6.7 | 30 分钟–4 小时 | 偏离更大更久，盘口更薄 |

结论：

1. 15 分钟级的偏离多数在 10–40 分钟内回归，偏离段中位约 15 分钟——这是高频与做市的地盘。
2. 大而持续的价差几乎都是**结构性**的（结算币不同、指数成分不同、资金费率），不回归到 0，而是随时间缓慢移动。
3. 长尾币机会多，但风险也大。

因此：固定阈值（不知道结构性偏移）、全历史 z-score（均值滞后，偏移移动时反向开仓）、单独的阶梯（只是加仓方式）都不够。采用下面的组合。

## 3. 信号模型

### 3.1 价差的定义

两条腿：**腿 A 做多、腿 B 做空**（开仓方向由信号决定，见 3.4）。同一底层资产按比例（ratio，来自合约乘数，复用 hedger 的 `MatchLegs`）换算：

- **可执行开仓价差**（策略规模 Q 下）：`open(Q) = (B 的买盘吃 Q 的均价 / ratio − A 的卖盘吃 Q 的均价) / A 均价 × 10⁴`
- **可执行平仓价差**：`close(Q) = (B 的卖盘均价 / ratio − A 的买盘均价) / A 均价 × 10⁴`
- **中间价价差** `mid`：两腿中间价之比，用于公允值与 σ 的估计（不受盘口宽度影响）。

只支持 `MatchBase`（同一底层）。不同底层没有共同价格，不在本 app 范围。

### 3.2 公允值与波动：EWMA

每 **1 秒**采样一次 `mid`，每 **1 分钟**取该分钟的中位数作为一个样本 `s_t`（中位数抗单点闪动）。在分钟样本上：

```
fair_t  = fair_{t-1} + α (s_t − fair_{t-1})            α = 1 − 2^(−1/H)，H = 公允值半衰期（分钟）
var_t   = (1−α) (var_{t-1} + α (s_t − fair_{t-1})²)
σ_t     = √var_t
dev_t   = s_t − fair_t
```

- 默认 H = 24 小时（1440 分钟）。结构性偏移被吸收进公允值，只交易偏离。
- **持仓期间冻结公允值**（`fair` 与 `σ` 停止更新，用开仓时的值），否则公允值会追着偏离走、把偏离"消化"掉；平仓后从冻结值继续更新。
- 冷启动：创建策略时用过去 `max(3H, 3 天)` 的 1 分钟 K 线回放初始化（与回测同一实现）；回放不足时状态为 `warming`，不交易。

### 3.3 成本

```
cost_bps = (fee_A + fee_B) × 2 + roundtrip(Q) + buffer
roundtrip(Q) = close(Q) − open(Q)   （同向时为两次穿越盘口的成本，见对冲页「价差深度」）
```

- 费率：策略上填写两腿的吃单费率（bps，默认按交易所公开费率表给出，用户按自己的等级修改）。gateway 目前不提供账户费率，后续可补。
- `buffer`：用户设定的最低净利（bps），默认 2。

### 3.4 开仓条件（全部满足）

令 `threshold = max(cost_bps, k·σ)`，k 默认 2。

1. **偏离**：`|dev| ≥ threshold`。`dev > 0`（B 相对 A 偏贵）→ 做多 A、做空 B；`dev < 0` → 反向。方向可在策略上限制为「只做一边」。
2. **可执行**：在当前盘口上，按开仓方向的**可执行价差**相对 `fair` 的偏离也 ≥ threshold（不是只看中间价）。
3. **持续**：以上两条**连续满足 T 分钟**（默认 10 分钟；按秒采样计，允许最多 10% 的秒样本不满足，以容忍一次推送的抖动）。
4. **资金费不吃掉利润**：`预期回归收益 + 预期持仓期净资金费 ≥ buffer`，预期持仓期取最近 7 天该策略或回测的平均持仓时长（默认 12 小时）。
5. **风控**允许（见第 6 节）。

### 3.5 平仓条件（任一满足）

1. **回归**：`|dev_exec| ≤ c·σ_frozen`，c 默认 0.5（`dev_exec` 用平仓方向的可执行价差）。
2. **止损**：偏离继续朝不利方向扩大到 `stop × threshold`（默认 3 倍）——判定为结构变化而非偏离。
3. **超时**：持仓超过 D（默认 3 天）。
4. **手动**：用户点击平仓，或暂停策略并选择「平掉所有仓位」。

平仓同样需要持续满足 T_close（默认 2 分钟，止损为 0：立刻）。

### 3.6 阶梯（可选，默认关闭）

开启后，同方向每再偏离 Δ（默认 1σ）开一档，最多 N 档（默认 3），每档规模 Q。每档独立记录入场价差，按自己的入场偏离平仓（第 i 档在 `dev` 回到 `entry_i − Δ` 以内时平）。总敞口受风控上限约束；止损与超时作用于全部档位。

## 4. 架构

```
web ─ trader ─┬─ /api/hedge   → hedger   （资金费套利：groups）
              ├─ /api/options → optioner （期权）
              └─ /api/spread  → spreader （价差套利：strategies → cycles → groups）
                                   │ 只经 gateway 交易
                                   ▼
                                gateway ─ exchange ─ sdk
spreader ──> PostgreSQL（与 hedger 同库实例，独立的 database 与 role）
```

### 4.1 抽出执行内核：`pairexec`

hedger 的执行部分是 spreader 需要的全部执行能力：两腿下单（双边市价 / 一腿挂单一腿对冲）、单腿失败的回退或保留、成交驱动的组状态、未知结果查询不重发、启动时对账、与交易所持仓的定期核对、资金费估算。

把它从 `hedger` 抽到新包 `pairexec`，hedger 与 spreader 各自在上面加业务：

| 留在 `pairexec`（通用） | 留在 `hedger`（资金费套利） | `spreader` 新增 |
|---|---|---|
| Group / Leg / Action / Order / Fill 模型 | 开仓请求的校验与预览 API | 策略、规则、信号、周期 |
| Engine、runner（下单、查询、撤单、对账） | `/v1/groups` 等现有 API（不变） | 评估器（采样、EWMA、持续判定） |
| Market（订阅 gateway 的盘口、资金费、持仓） | 对冲页的视图（GroupView） | 风控与审计事件 |
| Store 中 group/action/order/fill/event/funding 的读写 | | 回测 |
| sizing（PlanSize、SweepBook、MatchLegs）、文本码 | | |

**做法**（P1 已完成）：

1. hedger 除 HTTP API（`hedger/api.go`）外的全部代码移入 `pairexec`：模型、Engine、runner、Market、Store、sizing、position、文本码、Service、视图与资金费估算。
2. Store 由 `Schema{Prefix, Migrations, Lock}` 决定表名：SQL 与迁移文件中的 `{{groups}}`、`{{prefix}}` 等占位符在运行时替换。`pairexec.HedgeSchema` 就是 hedger 一直使用的表名（`hedge_*`、`hedger_migrations`、原 advisory lock），已部署的库无需任何迁移；已用旧库验证。
3. `Engine.SetObserver(Observer)`：动作结束、组状态确定后回调 `ActionSettled(group, action, status)`。spreader 用它回填周期；hedger 不设置。
4. `Store.Migrate(fs, dir)` 让应用把自己的迁移文件（含 `{{...}}` 占位符）应用到同一 schema；`NewStoreWithPool` 让应用复用连接池。
5. 文本码保留在 `pairexec/text.go`（全部是执行相关的），前端字典与测试改为读这个文件。
6. 结果：hedger 行为不变（同一 API、同一表）；执行内核测试在真实 PostgreSQL 上全部通过，并新增「两个前缀在同一库中互不干扰」的测试。

### 4.2 spreader 的组件

```
spreader/
  model.go       Strategy, Rule, Cycle, Sample, 状态常量
  store.go       策略、周期、样本、审计事件；内嵌 pairexec.Store（spread_ 前缀）
  evaluator.go   每个启用的策略一个评估循环：1s 采样、1min 聚合、EWMA、条件判定
  signal.go      纯函数：EWMA 更新、阈值、可执行价差、持续判定、阶梯档位（回测与实盘共用）
  risk.go        单策略与全局风控
  backtest.go    用 gateway K 线回放 signal.go，输出周期列表与统计
  service.go     组装：gwclient 流、pairexec.Engine、评估器、广播
  api.go         HTTP + WebSocket
cmd/spreader/main.go   与 cmd/hedger 同构（initdb、token、环境变量）
```

**评估循环**（每个运行中的策略一个 goroutine）：

1. 每秒从 `pairexec.Market` 取两腿盘口（订阅由 Market 管理；盘口超过 5 秒未更新视为不可用，本秒不计样本，且不得开仓）。
2. 计算 `mid`、`open(Q)`、`close(Q)`；累计到当前分钟。
3. 每分钟结束：中位数 → EWMA 更新（持仓时冻结）→ 写入 `spread_samples`。
4. 每秒判定开/平条件与持续计时；满足时生成**决策**：先写审计事件，再调用 `pairexec.Engine.Open/Close`（规模、方向、模式来自策略）。
5. 执行中（有 working action）不再产生新决策；执行结束由 Observer 回填周期。

**单写者**：一个策略的决策只由它自己的评估循环产生；手动开平仓通过同一个循环的命令通道进入，保证不会和自动决策并发。

### 4.3 与 hedger 的关系

- 同一个账户可能同时被 hedger 与 spreader 使用。两者各自的组只按自己的成交计算持仓，不会互相"认领"；但**交易所的净持仓是合并的**。因此：
  - spreader 的风控里，单市场敞口上限按**交易所实际持仓**（来自 gateway 的账户流）检查，而不是只看自己的组；
  - 与交易所持仓的核对（`ReconcileVenue`）在合并持仓下会误报，spreader 侧改为「本 app 的组之和 ≤ 交易所持仓」的方向检查；文档与界面提示：建议为价差套利使用独立的子账户。

## 5. 数据模型

迁移文件 `spreader/migrations/001_init.sql`（`pairexec` 的 group/action/order/fill/event/funding 表以 `spread_` 前缀创建，结构与 hedger 相同）。spreader 自有表：

```sql
create table spread_strategies (
    id            text primary key,
    name          text not null,
    status        text not null check (status in ('paused', 'warming', 'watching', 'entering', 'holding', 'exiting', 'halted')),
    -- legs: A and B; the side of each is decided per cycle.
    leg_a_account text not null, leg_a_inst text not null,
    leg_b_account text not null, leg_b_inst text not null,
    ratio         numeric not null,
    -- rule (all bps unless noted)
    direction     text not null default 'both' check (direction in ('both', 'long_a', 'long_b')),
    size          numeric not null,           -- per cycle (or per rung), quote of leg A
    fair_half_life_min int not null default 1440,
    k_sigma       numeric not null default 2,
    min_profit    numeric not null default 2,
    fee_a         numeric not null, fee_b numeric not null,
    persist_min   int not null default 10,
    close_sigma   numeric not null default 0.5,
    close_persist_min int not null default 2,
    stop_mult     numeric not null default 3,
    max_hold_min  int not null default 4320,
    ladder_step_sigma numeric,                -- null: ladder off
    ladder_max    int not null default 1,
    exec_mode     text not null default 'market' check (exec_mode in ('market', 'limit')),
    maker_leg     smallint,
    -- risk
    max_notional  numeric not null,
    max_daily_loss numeric not null,
    -- state (survives restarts)
    fair          numeric, variance numeric, frozen boolean not null default false,
    halted_reason text not null default '',
    created_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now()
);

-- One open → close round trip (per rung when laddering).
create table spread_cycles (
    id           text primary key,
    strategy_id  text not null references spread_strategies (id) on delete cascade,
    rung         smallint not null default 0,
    group_id     text not null,               -- spread_groups.id
    direction    text not null check (direction in ('long_a', 'long_b')),
    status       text not null check (status in ('opening', 'open', 'closing', 'closed', 'failed', 'broken')),
    entry_fair   numeric not null, entry_sigma numeric not null,
    entry_dev    numeric not null,            -- deviation at the decision (bps)
    entry_exec   numeric,                     -- executed open spread (bps), from fills
    exit_reason  text check (exit_reason in ('revert', 'stop', 'timeout', 'manual')),
    exit_dev     numeric, exit_exec numeric,
    fees         numeric not null default 0, funding numeric not null default 0,
    pnl          numeric,                     -- net, quote
    opened_at    timestamptz not null default now(),
    closed_at    timestamptz
);
create index spread_cycles_strategy on spread_cycles (strategy_id, opened_at desc);

-- Minute samples, kept 30 days: the chart and the next warm-up.
create table spread_samples (
    strategy_id text not null references spread_strategies (id) on delete cascade,
    at          timestamptz not null,
    mid         numeric not null, open_exec numeric, close_exec numeric,
    fair        numeric not null, sigma numeric not null,
    primary key (strategy_id, at)
);

-- Every decision and rule change, with the numbers that justified it.
create table spread_audit (
    id          bigserial primary key,
    strategy_id text not null references spread_strategies (id) on delete cascade,
    at          timestamptz not null default now(),
    kind        text not null,                -- decision.open, decision.close, skip.risk, rule.change, halt, resume …
    detail      jsonb not null
);
create index spread_audit_strategy on spread_audit (strategy_id, id desc);
```

**策略状态**：

| 状态 | 含义 |
|---|---|
| `paused` | 用户暂停（新建策略的默认状态）；不评估、不交易，已有仓位保留 |
| `warming` | 历史不足以估计公允值；只采样 |
| `watching` | 无仓位，等待开仓条件 |
| `entering` / `exiting` | 有执行中的开/平动作 |
| `holding` | 有仓位（阶梯未满时仍可加档） |
| `halted` | 风控停止（原因见 `halted_reason`）；需要用户确认后恢复 |

## 6. 风控

全部在决策前检查，不满足则写 `skip.*` 审计事件，不下单：

| 层级 | 规则 |
|---|---|
| 策略 | 名义金额 ≤ `max_notional`；当日（UTC）已实现亏损 ≤ `max_daily_loss`，超出 → `halted` |
| 策略 | 任一周期 `broken`（单腿暴露）→ 策略 `halted`，直到组被修复 |
| 全局 | 所有策略名义金额之和 ≤ 全局上限（服务配置）；同一市场（交易所实际持仓）上的敞口 ≤ 单市场上限 |
| 数据 | 任一腿盘口过期、gateway 断线、账户流断开 → 不开新仓（已有仓位的止损仍按最后可用数据判定，并在恢复后立即复核） |
| 保证金 | 开仓前按两腿杠杆估算所需保证金 ≤ 可用（复用对冲页 marginCheck 的逻辑，移到 Go） |
| 节流 | 同一策略两次开仓间隔 ≥ 冷却时间（默认 5 分钟）；一个周期失败后冷却加倍 |

全局开关：`POST /v1/halt` 立即停止所有策略的新决策（已在执行的动作继续完成），界面顶部常驻「全部暂停」按钮。

## 7. 回测

与实盘**同一份** `signal.go`：

- 数据：两腿的 1 分钟 K 线（gateway `/v1/candles`，按需分页，最多 30 天）。K 线只有收盘价，没有盘口：
  - `mid` 用两腿收盘价；
  - 可执行价差用 `mid ± 当前盘口半个往返成本`近似（由创建时的「价差深度」给出），并在结果上标注「按当前盘口宽度估算」。
- 资金费：两腿的资金费历史（`/v1/funding/history`）按持仓时长计入。
- 输出：周期列表（开平时间、方向、入场/出场偏离、持仓时长、毛利、成本、资金费、净利）；统计（周期数、胜率、平均与中位持仓时长、净利合计、最大不利偏离、按日净值曲线）。
- 在创建与编辑规则时实时运行（防抖 500 ms），结果画在同一张价差图上。

## 8. API（`/api/spread/v1`，经 trader 代理）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/strategies` | 列表（含实时价差、偏离、σ、仓位、当日盈亏、状态） |
| POST | `/strategies` | 新建（默认 `paused`） |
| GET | `/strategies/{id}` | 详情：规则、状态、当前周期、最近样本 |
| PATCH | `/strategies/{id}` | 修改规则（持仓中只能改平仓相关与风控字段） |
| POST | `/strategies/{id}/start` · `/pause` | 启停；暂停时可选 `{"closeAll": true}` |
| POST | `/strategies/{id}/open` · `/close` | 手动开/平（走同一评估循环，受风控约束） |
| DELETE | `/strategies/{id}` | 仅在无仓位时 |
| GET | `/strategies/{id}/samples?since=` | 分钟样本（图表） |
| GET | `/strategies/{id}/cycles` | 周期 |
| GET | `/strategies/{id}/audit` | 审计事件 |
| POST | `/backtest` | 规则 + 两腿 + 窗口 → 回测结果（不保存） |
| GET | `/suggestions` | 适合低频的组合排行（见 9.4） |
| POST | `/halt` · `/resume` | 全局停止 / 恢复 |
| GET | `/ws` | 推送：策略状态、每秒的实时价差与偏离、周期变化 |

错误与文本码沿用 hedger 的约定（`{code, params, message}`，前端双语渲染）。

## 9. 界面

顶部导航新增「价差」：交易 · 期权 · 对冲 · **价差**。仅在 trader 配置了 `SPREADER_URL` 时出现。

### 9.1 策略列表（默认）

```
┌ 价差策略 (6)   [+ 新建]                         全部 · 运行中 · 已暂停 · 停止      [全部暂停] ┐
│ 名称           组合                    偏离        σ     阈值    持续   仓位   今日   累计   状态   │
│ BTC USDT/USDC  多 BN BTC-USDT·空 BN BTC-USDC  +2.1σ +3.1  1.5  ±3.0   6/10m  —      —     +$310  观察中 │
│ ETH BN/Kraken  …                        −0.4σ …                       1 档   −$8   +$95   持仓中 │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```

- 「偏离」= 当前偏离 / σ，带正负色；「持续」是开仓持续计时（6/10 分钟），用进度条显示，让人知道"快要开了"。
- 行内操作：启动 / 暂停；点击行进入详情。
- 状态只用文字和一个小圆点：观察中（灰）、持仓中（青）、执行中（青、闪烁）、预热中（灰）、已暂停（灰）、已停止（红）。

### 9.2 策略详情

```
┌ ← BTC USDT/USDC · 持仓中 1 档   偏离 +2.1σ · 公允 −10.4 bps · σ 1.5     [平仓] [暂停 ▾] ┐
├ 价差图：价差（灰）· 公允值（青虚线）· 开仓带 fair±threshold（浅色带）· 平仓带 · 持续满足时段（底部条）┤
│        · 开/平标记（▲▼）· 冻结期间公允值画成水平段 · 1h/1d/7d/30d                              │
├ 周期 · 审计 · 两腿 ──────────────────────────────────────────┬ 规则（可编辑，修改即重跑回测）    ┤
│ 开仓时间 方向 入场偏离 出场偏离 持仓时长 毛利 成本 资金费 净利 原因 │ 规模 · 方向 · 公允半衰期 · k · 最低净利│
│                                                             │ 费率 A/B · 持续 · 平仓 c · 止损 · 超时 │
│                                                             │ 阶梯（开关、Δ、档数）· 执行方式        │
│                                                             │ 风控：名义上限、当日亏损上限           │
│                                                             │ 回测摘要（按当前规则，30 天）          │
│                                                             │ [保存] [放弃修改]                     │
└─────────────────────────────────────────────────────────────┴──────────────────────────────────────┘
```

- 价差图是核心：一眼能看出"为什么开、为什么没开、规则是否合理"。
- 规则修改后，回测结果（周期数、胜率、净利、平均持仓时长）在面板底部即时更新，同时在图上画出回测周期的开平点（虚线标记，与实盘区分）。
- 「两腿」标签页复用对冲页的组详情（两腿持仓、订单、成交、资金费）。

### 9.3 新建策略（右侧抽屉，三步）

1. **选组合**：两个标的选择器（复用 InstrumentPicker），或从「建议」列表（9.4）一键带入；选完显示 7 天价差小图与统计（均值、σ、半衰期、偏离持续时长分布）。
2. **规则**：默认值已按该组合的数据给出（公允半衰期取该组合半衰期的 10 倍并限制在 6 小时–7 天；费率按交易所默认；k=2；持续 10 分钟）。每个参数旁一句说明。实时回测。
3. **账户与风控**：两腿账户（无默认、需选择，与对冲页一致）、执行方式、名义上限、当日亏损上限。创建后状态为「已暂停」，需要手动启动。

### 9.4 组合建议

`GET /v1/suggestions`：对资金费率表里同一底层、深度足够的永续两两组合，用最近 7 天 15 分钟 K 线计算，按

```
score = 偏离超过 2σ 且持续 ≥ T 的次数 × 平均超额偏离(bps) − 估算往返成本
```

排序；每 6 小时重算一次并缓存。列表显示：组合、结构性偏移（均值）、σ、半衰期、7 天内的合格机会次数、估算单次净利。点击进入新建第 1 步。

### 9.5 颜色与文字

沿用对冲页的约定：方向色只出现在「多/空」标签上；数值的正负用青/粉；红色只用于阻止交易或需要处理的状态（已停止、单腿暴露）。所有文案中英双语，术语与对冲页一致。

## 10. 实施计划

| 阶段 | 内容 | 完成标准 |
|---|---|---|
| P1 | 抽出 `pairexec`；hedger 改为使用它 | hedger 全部测试通过；对冲页行为不变；表前缀可配 |
| P2 | `spreader` 后端：模型、迁移、`signal.go`（含单测：EWMA、冻结、持续、阶梯、成本）、评估器、风控、Observer 回填周期、API | 用假 gateway 的端到端测试：偏离持续 → 开仓 → 回归 → 平仓 → 周期净利正确；风控各项拒绝；重启后状态恢复 |
| P3 | 回测与建议：K 线回放、资金费计入、`/backtest`、`/suggestions` | 回测与实盘对同一序列给出相同决策（共用 signal.go 的一致性测试） |
| P4 | 部署：`cmd/spreader`、Dockerfile、compose（spreader-initdb + spreader）、trader 代理 `/api/spread`、`/api/apps` | 本地 compose 起得来，导航出现「价差」 |
| P5 | 前端：列表、详情（价差图、周期、审计、两腿）、规则面板与实时回测、新建抽屉、建议 | 浏览器验证各状态；i18n 测试通过 |
| P6 | 文档：模块文档、API 文档、使用指南、DESIGN.md | 文档站构建通过 |

每个阶段单独提交；P1 完成后先合并一次，降低后续冲突。

## 11. 待定与风险

- **费率来源**：gateway 不提供账户费率，第一版由用户填写；后续可在 exchange 层补「账户费率」接口。
- **共享账户的持仓核对**：同一账户同时被 hedger 与 spreader 使用时，交易所净持仓是合并的（见 4.3）。建议使用独立子账户，界面在选择已被 hedger 使用的账户时给出提示。
- **K 线回测的盘口近似**：回测没有历史盘口，可执行价差是近似值；回测结果始终标注这一点。
- **结构性偏移的突变**（例如稳定币脱锚、交易所指数调整）：止损 + 当日亏损上限 + 单腿暴露即停止三层兜底；建议列表对近期均值剧烈变化的组合降权。
