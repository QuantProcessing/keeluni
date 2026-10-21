# 开发与测试

## 环境

- Go 1.26
- Node 22
- 可选：PostgreSQL（hedger 的数据库测试与本地运行对冲应用）
- 可选：Docker（部署、冒烟测试）

## 常用命令

```sh
scripts/dev.sh                 # 构建前端与二进制，启动 gateway + trader（+ hedger）
make test                      # 全部离线测试：go test -short ./... 与前端测试
make build                     # 构建前端与 bin/gateway、bin/trader、bin/hedger
make up / make down / make logs
make smoke                     # 对运行中的 compose 栈做公共接口检查
```

前端单独开发：

```sh
cd web
npm run dev                    # http://127.0.0.1:5173，/api 代理到 TRADER_ORIGIN（默认 127.0.0.1:8080）
npx vitest run                 # 测试
npm run build                  # 类型检查 + 构建
```

文档站点：

```sh
cd docs
npm install
npm run dev                    # 本地预览
npm run build                  # 构建到 docs/.vitepress/dist
```

推送到 master 且 `docs/` 有变化时，`.github/workflows/docs.yml` 自动构建并发布到 GitHub Pages。

## 测试分层

| 层 | 测试 | 说明 |
|---|---|---|
| sdk | fixture 测试 | `testdata/` 中保存实际抓取的回包；签名用官方测试向量 |
| exchange | 契约测试 | 每个交易所都要通过同一组契约测试；公开 API 清单测试 |
| gateway | 单元与集成 | 账户 runtime、路由、过滤、限流、hub、资金费率数据集 |
| pairexec | 场景测试（真实 PostgreSQL） | 用假的 gateway（`fakeVenue`）驱动：开仓、限价对冲、部分成交、失败策略、异步拒单、结果无法确认、与交易所对账、重启接管；两个应用的表前缀在同一库中互不干扰 |
| web | Vitest | 账户选择、路由规则、下单与查询、对冲模型（比例、价差序列、保证金检查）、字典完整性、hedger 编码文本与 Go 源码一致 |

执行内核的数据库测试需要一个超级用户连接串，每个测试创建并删除自己的库：

```sh
HEDGER_TEST_DATABASE_URL=postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable \
  go test ./pairexec/
```

没有设置时这些测试会跳过。

## 实盘测试

默认测试路径完全离线。访问真实交易所的测试由环境变量显式开启：

| 变量 | 作用 |
|---|---|
| `KEELUNI_ENABLE_LIVE_READ_TESTS=1` | 开启只读的实盘测试（行情、目录） |
| `KEELUNI_ENABLE_<VENUE>_UNSAFE_RAW_SDK_WRITES=1` | 开启会真实下单的 sdk 测试（需要相应凭证），**会动用真实资金** |

没有任何 Make 目标会设置这些变量。

## 代码约定

- 依赖只能自上而下：`sdk → exchange → gateway → trader / hedger → web`。
- 精确数值一律使用 `decimal`（Go）或字符串（前端），显示时才转为数字。
- 新增 `exchange/` 导出符号时，在 `exchange/testdata/public_surface_manifest.json` **追加**条目（只追加；失效条目可原样删除）。
- 面向用户的文字必须同时有中英文：前端加到 `web/src/i18n/en.ts` 与 `zh.ts`；hedger 加到 `pairexec/text.go` 与 `web/src/i18n/hedger.ts`（见[国际化](./i18n)）。
- Go 代码 `gofmt`；提交信息用完整的描述性句子。
