# 接入新交易所

以 Kraken Futures 与 Deribit 的接入为例，一个新交易所（或新市场）需要经过每一层。

## 1. sdk

在 `sdk/<venue>/` 实现原生 API：

- REST 客户端与签名（用官方测试向量写签名测试）；
- WebSocket：连接、鉴权、订阅、心跳、重连所需的钩子；
- 原生类型；
- `testdata/` 保存实际抓取的回包，作为 fixture。

**忠实优先**：不要在这一层做规范化。

## 2. exchange

在 `exchange/internal/factoryclient/` 实现适配器：

| 文件 | 内容 |
|---|---|
| `<venue>.go` | `PerpClient` / `SpotClient`：下单、撤单、查询、持仓、余额、杠杆、精确查单 |
| `<venue>_ws.go` | 公共与私有流：盘口（处理序列号断档）、成交、K 线、持仓、订单、成交推送 |
| `<venue>_catalog_loader.go` | 标的目录：规范 symbol、tick、step、最小数量、资产类别 |
| 资金费率 loader | 全市场资金费率（给 `PublicFundingBoard`）与历史 |

必须决定并记录：

- 规范 symbol 怎么由原生名称得到（如 Kraken `PF_XBTUSD` → `BTC-USD`）；
- 数量单位与步长（合约面值、精度）；
- 资金费率的口径换算（周期、是否 8 小时等效、百分数还是小数）；
- 精确查单在什么情况下只能返回 `unconfirmed`；
- 不支持的方法返回 `exchange.ErrUnsupported`。

然后：

- `exchange/factory/`：新增 `<Venue>PerpConfig`，注册到目录、衍生品、资金费率 loader 与公共连接器；
- `routeprofile/`：新增路由行（交易所、产品、结算币）；
- `credential/`：注册凭证类型（如 `AuthAPIKeySecret`）与字段；
- `exchange/market_support.go`：声明支持的市场；
- 公开 API 清单追加新导出符号；
- 通过契约测试。

## 3. gateway

- `gateway/venue/venue.go`：把新市场加入 `Markets`（这一步决定终端里是否出现它）；
- `gateway/ratelimit/transport.go`：在 `localUnitsPerMinute` 中设置本地预算，并在 `internal/venuehttp` 中为每个接口定义成本（没有策略的请求会被拒绝）；
- `gateway/account/clients.go`：按凭证构造该市场的客户端；
- 如果交易所的私有流会推送 gateway 不服务的合约（期权、反向合约），在账户 runtime 中过滤；
- 如果 Gate CrossEx 也能到达该市场，更新 CrossEx 的 symbol 映射（`crossex_symbols.go`）。

## 4. web

- `web/src/lib/format.ts`：交易所显示名称；
- `web/src/i18n/en.ts` 与 `zh.ts`：连接对话框中的密钥说明 `connect.hint.<venue>`；
- 如果资金费率是连续计息等特殊情况，在界面上明确标出。

## 5. 验证

1. 离线测试：`make test`；
2. 本地启动后 `make smoke`：确认新市场的行情、盘口、K 线、WebSocket 都正常；
3. 查看 `/v1/health`：目录与资金费率采集状态；
4. 实盘只读测试；
5. 小额实盘下单、撤单、查单、持仓推送。
