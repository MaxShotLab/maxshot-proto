# Maxshot Gateway 全局六仓架构与异地组网规划 {#gateway-v3-plan}

整体系统按物理仓库拆分为 **Gateway 前后端**、**Provider 前后端**、**合约仓库与合约后端** 共 6 个独立仓库。跨仓通信统一跑在 **WireGuard 异地虚拟专网** 上，全部采用 **`gRPC` + `TLS` + `HMAC`** 双向鉴权。

- 新建的 `Provider 后端`、`合约后端` 与 `Gateway 推理数据面` 统一用 Go 构建；`Gateway 后端` 内的计费账本与 Studio 媒体保留现役已验证的 TypeScript 代码。[](source:plan-v3)
- `Provider 后端` 一期即独立上线，统一管理供给成本与客户零售价，用 Gateway 公钥加密上游凭证后随版本化快照下发给 Gateway；Gateway 按分钟窗口批量回推实测在线率与延迟。
- `合约后端` 分离公私双平面：公网只读 HTTPS 接口直接向 `Gateway 前端` 提供报价、仓位与网络大盘（`Gateway 前端` 集成钱包直签链上合约）；私网 `gRPC` 按 7 天边界向 `Gateway 后端` 推送用户应发 Credits 复核入账。
- Gateway 本地持久化缓存目录快照与加密凭证，即使异地组网中断，主站推理与计费仍可独立正常运行。

@answer scope="范围：全局 6 仓职责边界与跨网协议 · v3 定稿"
@meta locale=zh-CN style=clean
@source plan-v3 title="Maxshot Gateway 全局六仓架构规划（v3）" path=docs/plans/2026-11-25-gateway-modular-architecture-plan.zh-CN.md

## 六个独立仓库如何分工与跨网连接？ {#six-repos}

所有内部 `gRPC` 端口只绑定 WireGuard 虚拟专网网卡，不对公网暴露。推理热路径全部在 `Gateway 后端` 同机房内闭环，不跨网同步调用外部仓库。

@architecture id=six-repo-topology title="全局六仓与异地组网通信拓扑"
| gw-web | 仓2：Gateway 前端 | frontend |
| prv-web | 仓6：Provider 前端 | frontend |
| agents | 外部 Agent 与 SDK | external |
| gw-go | 仓1：Go 推理数据面 | backend |
| bill-ts | 仓1：TS 计费与媒体 | backend |
| gw-pg | Gateway PG 与 Redis | database |
| prv-go | 仓5：Provider 后端（Go） | backend |
| prv-pg | Provider PG | database |
| contracts | 仓3：智能合约仓库 | security |
| cnt-go | 仓4：合约后端（Go） | backend |
| upstream | 上游大模型供应商 | cloud |
| chain | 以太坊 Finalized 区块 | cloud |
| gw-web > bill-ts | Web 与媒体 API |
| gw-web > gw-go | 托管流式会话 |
| gw-web > cnt-go | 公网只读 Token API |
| gw-web > contracts | 钱包直签上链 | security |
| agents > gw-go | HTTPS + API Key | emphasis |
| prv-web > prv-go | 门户管理 API |
| gw-go > bill-ts | 同机房三步 gRPC | emphasis |
| bill-ts > gw-pg | 三 Schema 硬隔离 |
| gw-go > upstream | 内存解密直连调用 | emphasis |
| prv-go > prv-pg | 独立存储 |
| prv-go > gw-go | 快照与加密凭证 | security |
| prv-go > bill-ts | 同步定价预算副本 | dashed |
| gw-go > prv-go | 回推在线率与延迟 | dashed |
| contracts > cnt-go | 导出版本化 ABI |
| chain > cnt-go | 监听 Finalized 事件 |
| bill-ts > cnt-go | 同步钱包绑定资格 | security |
| cnt-go > bill-ts | 推送 7天用户 Credits | emphasis |

@steps id=six-repo-steps
| r-gw | 仓1 & 仓2：Gateway 前后端 | Go 数据面扛高并发流式转发与路由，TS 计费与 Studio 复用现役 9.4 万行财务与媒体资产。 | six-repo-topology | gw-go |
| r-prv | 仓5 & 仓6：Provider 前后端 | Go 后端与 React 门户一期即独立上线，统一管准入、成本与零售定价，二期开放供应商自助提审。 | six-repo-topology | prv-go |
| r-cnt | 仓3 & 仓4：合约与合约后端 | Solidity 合约独立审计，Go 合约后端监听链上质押并按 7 天周期向 Gateway 推送用户额度。 | six-repo-topology | cnt-go |

## 跨异地组网的两条核心交互怎么跑？ {#cross-mesh-seq}

跨网交互全部带 `service_id`、`timestamp`（300 秒窗口）、`nonce` 防重放与 `HMAC-SHA256` 载荷签名，并配本地持久化队列做断网补推。

@sequence id=cross-repo-flows title="跨异地组网交互：目录凭证同步与合约额度推送"
| actor prv | Provider 后端（Go） | backend |
| actor gw | Go 推理数据面 | backend |
| actor bill | TS 计费核心 | backend |
| actor cnt | 合约后端（Go） | backend |
| phase | 1. 目录定价下发、公钥加密凭证与指标回传 |
| prv | gw | SyncCatalogSnapshot + Gateway公钥加密凭证 | emphasis |
| prv | bill | 同步同版本定价与预算只读副本 |
| gw | prv | 分钟级批量 ReportOfferingMetrics（Uptime/p50/p95） | return |
| phase | 2. 钱包资格同步与 7 天边界 Credits 入账 |
| bill | cnt | SyncWalletEligibility（绑定时间 + 条款版本） | security |
| cnt | bill | IssueAitCreditGrants（7天边界应发额度） | emphasis |
| bill | cnt | 四重校验通过并幂等入账（附 7天只读链上抽样对账） | return |

@steps id=cross-flow-steps
| cf-snap | 快照与非对称加密凭证 | Provider 用 Gateway 公钥加密上游 API Key，Gateway 落盘快照副本并在内存解密调用，断网仍可自治。 | cross-repo-flows | prv |
| cf-metric | 分钟级预聚合回传 | Gateway 本地按分钟窗口聚好在线率、p50/p95 延迟与应付汇总再批量回推，避免跨网海量小包。 | cross-repo-flows | gw |
| cf-grant | 资格同步与四重入账校验 | Gateway 同步绑钱包与签条款状态，合约后端算好用户 Credits 推送，Gateway 复核绑定、幂等、公式与硬顶后入账。 | cross-repo-flows | bill |

## 六大仓库的技术栈与职责怎么切分？ {#repo-matrix}

| 仓库 | 技术栈 | 核心数据与安全边界 |
| --- | --- | --- |
| 1. `gateway 后端` | Go 数据面 + TS 计费/Studio/BFF {good:稳快兼顾} | 独占 `billing`/`studio`/`bff` Schema + Gateway 私钥 |
| 2. `gateway 前端` | React + TypeScript | 主站 UI + 钱包直签合约 + 直连合约后端只读 API |
| 3. `合约仓库` | Solidity + Foundry | 链上 MAX/AIT 合约与不变量测试，向前端与后端导出 ABI |
| 4. `合约后端` | Go 1.23+ + PostgreSQL {good:公读私写隔离} | 公网只读 Token API + 私网 `gRPC` 推送 7天额度 |
| 5. `Provider 后端` | Go 1.23+ + PostgreSQL {good:纯 Go 新建} | 供给成本+客户零售价+档位目录，只存公钥加密密文 |
| 6. `Provider 前端` | React + TypeScript | 对标 OpenRouter 门户，一期内部用、二期开放外部提审 |

## 自动选模型与指定模型/供应商怎么分工？ {#routing-modes}

候选池与评测权重放在 `Provider 仓` 静态维护并随快照下发；请求级实时选路全部在 `Gateway Go 数据面` 内存完成，零跨网调用。

@workflow id=routing-mode-flow title="三种模型与供应商选择收敛路径"
| req | 请求进入 Go 数据面 | external |
| is-auto | 传 maxshot/auto? | backend |
| auto-pool | 内存匹配 Auto 精选池 | backend |
| is-pin | 指定 offeringId? | backend |
| pin-route | 锁定单一供应商（禁兜底） | security |
| auto-prv | 选最低成本主路与同模备份 | backend |
| settle | 三步同步 gRPC 预留与结算 | database |
| req > is-auto | |
| is-auto > auto-pool | 是 |
| is-auto > is-pin | 否 |
| auto-pool > is-pin | 解析出具体模型 |
| is-pin > pin-route | 是 |
| is-pin > auto-prv | 否 | emphasis |
| pin-route > settle | 合格直发 |
| auto-prv > settle | 主备限高 | emphasis |

| 调用模式 | `Provider 仓`（配置态） | `Gateway 仓`（运行态） |
| --- | --- | --- |
| 指定模型 + 自动选供应商 {good:默认主路径} | 下发该模型下已准入 Offering 与双层费率 | 筛合格节点选最低供给成本主路，首字前失败最多切 1 次同模备份 |
| 指定模型 + 指定供应商 | 下发指定 `offeringId` 准入状态与端点 | 只验该单一节点，不合格直拒，严禁偷偷切换其他供应商 |
| 自动选模型（`maxshot/auto`） | 维护评测通过的 Auto 精选池、任务标签与费用硬顶 | 本地规则识别任务与附件能力并保持会话粘性，选定模型后不跨模型重试 |

## 网关计费与跨网安全有哪些硬约束？ {#key-rules}

| 关键机制 | 定稿规则 |
| --- | --- |
| 异地组网与鉴权 | WireGuard 虚拟专网隐藏内部端口，叠加 `gRPC TLS` 与 `HMAC-SHA256` 时间窗防重放签名。 |
| 跨仓契约管理 | 单源维护 `.proto`（`buf` + Submodule/版本包），`buf breaking` 门禁拦截不兼容改动，禁止手工拷贝。 |
| 网关同机房计费 | Go 数据面与 TS 计费走 `AuthorizeAndHold` → `MarkSending` → `Settle` 三步同步 `gRPC` 配本地落盘重试。 |
| 6 桶顺序与开关 | `AIT` → `Basic` → `Premium` → `Free` → `Referral` → `Dollar`，保留 `Continue with Premium when Basic is not enough`。 |

@callout level=warning title="待产品补录的唯一未决项"
已结束 Funded Term 的 AIT 退款缓冲池（`Refund Reserve`）比例与超限审批规则，需在 Phase 2 合约联调前向产品补录正式 TD 决策。

## 四阶段跨仓演进路线怎么排？ {#roadmap-v3}

@workflow id=six-repo-roadmap title="六仓协同四阶段落地路线"
| lane s0 | Phase 0：组网与底座 |
| lane s1 | Phase 1：Provider与Go网关 |
| lane s2 | Phase 2：合约与AIT闭环 |
| lane s3 | Phase 3：开放门户与去耦 |
| p0-net | 打通异地组网与 HMAC | security | s0 |
| p0-clean | 统一 dollar_v1 与 Key | database | s0 |
| p1-prv | 上线 Provider 前后端 | backend | s1 |
| p1-gw | Go 数据面一次性全量切换 | backend | s1 |
| p2-cnt | 部署合约与 Go 合约后端 | backend | s2 |
| p2-ait | 跑通 7天推送与抽样对账 | security | s2 |
| p3-open | 开放外部供应商自助提审 | frontend | s3 |
| p3-lock | 彻底解耦 Studio 与权限锁死 | database | s3 |
| p0-net > p0-clean | |
| p0-clean > p1-prv | 单源 Proto |
| p1-prv > p1-gw | 快照与回放达标 | emphasis |
| p1-gw > p2-cnt | |
| p2-cnt > p2-ait | 向量验证 | emphasis |
| p2-ait > p3-open | |
| p3-open > p3-lock | |

## 跨网风险与开工核对清单 {#checklist-v3}

| 风险点 | 应对预案 |
| --- | --- |
| 异地组网闪断或对端重启 {caution:跨网常态} | Gateway 本地 PG 落盘快照与加密凭证保障自治运行；指标回推与额度推送均走本地持久化队列断点续传。 |
| 上游供应商密钥跨网传输泄露 | Provider 后端用 Gateway 公钥非对称加密 DEK，仅 Gateway 持私钥在内存解密，抓包与拖库均无明文。 |
| Phase 1 双 Go 服务同步新写工作量集中 | 一期 Provider 门户只做内部运营必需功能；Go 数据面切流前做全量真实流量回放 Diff 并留旧网关秒级回切。 |

- [ ] Phase 0：完成 WireGuard 异地组网连通测试，落地 Go/TS 通用 `gRPC + HMAC` 拦截器与单源 `.proto`。
- [ ] Phase 0：将 `maxshot-gateway` 存量账户全部迁至 `dollar_v1`，回填存量 API Key 目录白名单。
- [ ] Phase 1：上线 `Provider 前后端`（一期内部运营版），验证公钥加密凭证与 `CatalogSnapshot` 跨网同步。
- [ ] Phase 1：完成 Go 推理数据面流量回放 Diff，原地址、原路径一次性全量切换并核对分钟级回传指标。
- [ ] Phase 2：上线 Go `合约后端`，联调钱包资格同步、`IssueAitCreditGrants` 四重校验与 7 天链上抽样对账。
