# 资金费率

资金费率是永续合约持仓的主要成本或收益，也是对冲（基差/资金费套利）的核心输入。各交易所的口径差异很大：有的每小时结算、有的每 8 小时，有的给的是「8 小时等效值」，有的单位是百分数。KeelUni 把它们统一成一种口径，作为 gateway 的**常驻数据集**。

## 统一口径

| 字段 | 含义 |
|---|---|
| `rate` | `nextFunding` 那次结算收付的仓位名义价值比例（小数），**>0 表示多头付给空头** |
| `intervalHours` | 该标的自己的结算周期；无法确定时缺省，**绝不默认 8 小时** |
| `apr` | `rate × 8760 / intervalHours`（单利年化），跨交易所比较用；周期未知时缺省 |
| `nextFunding` | `rate` 对应的那次结算时刻（毫秒）；已过结算但新数据未到时按周期顺延 |
| `stale` | 该市场的采集连续 3 个周期没有成功 |

只有当 `intervalHours` 在 1–24 之间时才计算 `apr`。

## 各交易所的换算

| 交易所 | 原生数据 | 换算 |
|---|---|---|
| Binance | `premiumIndex` 给 rate；`fundingInfo` 只列出非默认周期的标的 | `fundingInfo` 读取成功时，未列出的标的为 8 小时；读取失败时周期视为未知 |
| OKX | `fundingTime` 才是下次结算（`nextFundingTime` 是再下一次） | 周期 = 二者之差 |
| Bybit / Bitget | 直接给出周期 | — |
| Gate | 周期以秒给出 | ÷3600 |
| Hyperliquid | 每整点结算 | 周期 1 小时，下次结算为下一个整点 |
| Lighter | REST 资金费表是 8 小时等效值；WebSocket 是「百分数/小时」 | REST ÷8，WS ÷100；周期 1 小时，下次结算为下一个整点 |
| Kraken Futures | `relativeFundingRate`：每小时、按仓位价值的比例 | 周期 1 小时 |
| Deribit | 资金费连续计息；`funding_8h` 是 8 小时等效值 | 每小时 = `funding_8h / 8`，周期 1 小时；界面标为「连续计息」，不显示倒计时 |

## 采集

`gateway/funding` 用每个市场的衍生品数据源（`factory.PublicFundingBoard`）采集，全部免凭证：

| 市场 | 方式 | 频率 |
|---|---|---|
| Hyperliquid | WebSocket `allDexsAssetCtxs`：一个订阅覆盖主 dex 与全部 HIP-3 dex | 约每 15 秒一次完整快照，不占 REST 额度 |
| Gate | 全市场 REST | 每 30 秒（回包约 0.5MB 且不压缩） |
| 其他 | 全市场 REST | 每 15 秒 |

Hyperliquid 启动时用三次请求拿到标的目录（不带费率），费率由推送补齐，避免启动时耗尽 REST 额度。

采集规则：

- 后台采集的每个请求都标记为「后台」，最多使用该交易所额度的 25%；**拿不到额度就跳过本轮，不排队**。
- 某次读取失败时保留上一份快照。
- 快照（snapshot）替换该市场的全部事实；增量（delta）只更新已知标的、只更新它携带的字段。
- 费率无效的行不发布。

## 对外提供

| 接口 | 内容 |
|---|---|
| `GET /v1/funding?venue=&base=` | 全部永续的当前资金费率表，以及各市场的采集状态 |
| `GET /v1/funding?inst=` | 单个标的 |
| `GET /v1/funding/history?inst=&since=&until=` | 已结算资金费率，窗口最长 30 天（默认最近 7 天），直接读交易所 |
| WebSocket `funding` 频道 | 订阅时先推当前值，之后有变化才推；不开新的交易所连接 |
| `ticker` 推送 / `GET /v1/ticker` | 资金费率字段从同一个数据集读取 |
| `GET /v1/health` 的 `datasets` | 各市场的采集模式、最后更新时间、是否过期、错误 |

交易页、对冲页、hedger 的资金费估算都使用这一个来源。

## 对冲中的资金费

hedger 在每次结算时按「结算时的报价费率 × 持有量 × 价格」估算每条腿的资金费收付，记入 `hedge_funding`。这是估算值，精确数字以交易所账单为准。净资金费率 = 空头腿收取的 APR − 多头腿支付的 APR（现货腿为 0），按单边名义价值计。
