# Maxshot Gateway 全局六仓架构演进与跨网协作规划（v3 定稿）

> **文档状态**：v3 最终定稿（基于现役 `/Volumes/SN550/maxshot/maxshot-gateway` 代码实况 + `spec/` 14 份规范 + 六仓拆分与异地组网对齐结论）  
> **全局范围**：覆盖 **Gateway 前后端、Provider 前后端、合约仓库与合约后端** 共 6 个独立仓库的职责边界、技术栈选型与跨网 `gRPC + TLS + HMAC` 协作契约  
> **核心策略**：新建后端仓（`Provider 后端`、`合约后端`）与 `Gateway 推理数据面` 统一采用 **Go** 从零构建；`gateway 后端` 内部的计费账本与 Studio 媒体继续保留现役已验证的 **TypeScript** 核心资产，按绞杀者模式平滑解耦

---

## 1. 全局六仓划分与技术栈矩阵

### 1.1 六大独立仓库全景表

| # | 仓库定位 | 建议仓库名 | 技术栈 | 核心职责与唯一事实来源（Source of Truth） |
| --- | --- | --- | --- | --- |
| 1 | **Gateway 后端** | `maxshot-gateway` | **Go 1.23+**（推理数据面）+ **TypeScript**（Node 22 + Fastify：计费账户、Studio 媒体、Web BFF、账务运营台） | 对外推理与媒体服务、客户身份与钱包绑定、Scoped API Key、6 桶双分录账本、供应商/平台/业务线预算准入、本地目录只读快照、向 Provider 回传聚合指标、接收合约 Credits 入账复核 |
| 2 | **Gateway 前端** | `maxshot-gateway-web` | **React + TypeScript**（含 wagmi/viem 钱包交互） | 面向终端用户与开发者的主站（Chat、Studio、Credits、Tokenomics、Models、Providers、Referrals、Profile/API Keys）；**承载链上合约交互（用户钱包直接签名上链）并直连 `合约后端` 公网只读 API 拉取链上数据** |
| 3 | **合约仓库** | `maxshot-contracts` | **Solidity + Foundry** | MAX 创世（1B）、LayerZero 四链跨链、Ethereum 合并仓位与 HWM 铸造/烧毁/7 天赎回、AIT 质押合约、形式化不变量测试，向 `合约后端` 与 `Gateway 前端` 导出版本化 ABI 与合约地址注册表 |
| 4 | **合约后端** | `maxshot-contract-service` | **Go 1.23+ 或 TypeScript**（待定，`maxshot-proto` 双端对等支持）+ **PostgreSQL** | **双平面隔离**：① **公网只读 HTTPS API** 向 `Gateway 前端` 提供 18 位整数铸造报价、仓位/待赎回状态、质押与 12 周发放记录、全网统计大盘；② **异地私网 `gRPC + HMAC`** 同步 Gateway 钱包绑定/条款状态，按 7 天边界计算 `W(t)` 推送用户 Credits 入账 |
| 5 | **Provider 后端** | `maxshot-provider-service` | **Go 1.23+ 或 TypeScript**（待定，`maxshot-proto` 双端对等支持）+ **PostgreSQL** | 一期即独立上线；统一承载供给侧（供应商主体、6 项准入证据、端点、能力、内部成本卡）与零售侧（客户零售价、Basic/Premium 档位、自定义目录、默认 `max_tokens`、供应商预算）；用 Gateway 公钥加密托管上游 API Key；经 `gRPC + HMAC` 向 Gateway 下发版本化 `CatalogSnapshot` 并接收分钟级 Uptime/延迟/调用汇总 |
| 6 | **Provider 前端** | `maxshot-provider-web` | **React + TypeScript** | 对标 OpenRouter 的独立 Provider 治理门户；一期供 Maxshot 运营直管配置，二期开放外部供应商自助入驻、提交 Offering、查看实测 Uptime/p50/p95 延迟与应付结算单 |

### 1.2 选型与资产复用依据
- **为什么新建后端与网关数据面全用 Go**：`Provider 后端`、`合约后端` 无历史代码包袱，与 `Gateway 推理数据面`（`net/http` + `chi`）统一使用 Go 1.23+，可共享同一套 Go `gRPC + HMAC` 拦截器、可观测中间件与单二进制容器化基建；推理网关在高并发 SSE 长连接下协程开销小、无大堆 GC 抖动。
- **为什么 Gateway 账本与 Studio 保留现役 TypeScript**：现役 `packages/server-core`（约 9.4 万行非测试代码）已稳定跑通 PostgreSQL 事务级资金分配、账户开关 **`Continue with Premium when Basic is not enough`**（`allow_basic_to_premium`）、订阅周期、链上充值重组及 Studio 租约/Fencing Worker。保留这部分 TS 资产可彻底规避重写财务核心的高危风险，最快完成上线闭环。
- **跨仓 `.proto` 单一契约源**：使用 `buf` + Git Submodule（或独立版本化契约包）单源维护所有跨仓 Protobuf 定义，自动编译生成 Go Module 与 TypeScript 类型包，CI 强制拦截破坏性变更，严禁手工复制 `.proto`。

---

## 2. 全局拓扑与异地组网架构

### 2.1 六仓部署与跨网通信拓扑

```mermaid
flowchart TB
    subgraph PublicUsers["公网访问层"]
        U_WEB["终端用户浏览器"]
        U_API["External Agents / SDK<br/>(Scoped API Keys)"]
        U_VENDOR["外部供应商 / Maxshot 运营"]
        ETH["Ethereum & 多链 RPC<br/>(finalized 区块)"]
        UPSTREAM["上游大模型供应商<br/>(OpenRouter / OpenAI / Anthropic...)"]
    end

    subgraph WebFrontends["独立前端层"]
        GW_WEB["仓2: maxshot-gateway-web<br/>(Gateway 前端)"]
        PRV_WEB["仓6: maxshot-provider-web<br/>(Provider 门户前端)"]
    end

    subgraph OverlayMesh["WireGuard 异地虚拟专网 (Headscale / WireGuard Mesh · 公网零暴露 gRPC 端口)"]
        subgraph Repo1["仓1: maxshot-gateway (Gateway 后端 · 同机房内网)"]
            GW_GO["gateway-dataplane (Go)<br/>Redis 边缘限流 / 内存快照路由<br/>本地私钥解密上游凭证 / SSE 透传<br/>本地落盘重试队列"]
            BILL_TS["billing-account-service (TS)<br/>6桶双分录账本 / 零信任重算<br/>客户+Key+供应商+平台+业务线预算<br/>钱包绑定与条款状态"]
            FIN_WK["finance-worker (TS)<br/>24h Recovery / 充值与过期Sweep<br/>7天边界只读链上抽样对账"]
            STUDIO_TS["studio-service & worker (TS)<br/>异步图/视频 24h 状态机 + RustFS"]
            BFF_TS["app-adapter (TS 瘦身 BFF)<br/>登录/JWT / Chat 会话编排"]
            PG_GW[("Gateway PG 16<br/>Schema 硬隔离:<br/>billing / studio / bff")]
            REDIS_GW[("Gateway Redis 7<br/>限流 / 幂等缓存 / 内部通知")]
        end

        subgraph Repo5["仓5: maxshot-provider-service (Provider 后端 · Go)"]
            PRV_GO["provider-service (Go)<br/>6项准入 / 供给成本+客户零售价<br/>档位与自定义目录 / Gateway公钥加密Key<br/>CatalogSnapshot 发布 / 观测指标聚合"]
            PG_PRV[("Provider PG 16")]
        end

        subgraph Repo4["仓4: maxshot-contract-service (合约后端 · Go) & 仓3: 合约"]
            CNT_GO["contract-service (Go)<br/>公网只读 HTTPS API (Tokenomics)<br/>私网 Finalized 索引 & 7天 W(t) 推送"]
            PG_CNT[("Contract PG 16")]
        end
    end

    U_WEB --> GW_WEB
    GW_WEB -->|HTTPS| BFF_TS
    GW_WEB -->|HTTPS| STUDIO_TS
    GW_WEB -->|公网只读 HTTPS (JWKS本地验签)| CNT_GO
    GW_WEB -->|用户钱包直签交易 (ABI)| ETH
    U_API -->|HTTPS + API Key| GW_GO
    U_VENDOR --> PRV_WEB
    PRV_WEB -->|HTTPS| PRV_GO

    BFF_TS -->|同机房内网主体透传| GW_GO
    BFF_TS --> BILL_TS
    GW_GO --> REDIS_GW
    GW_GO <-->|同机房三步同步 gRPC:<br/>Hold / MarkSending / Settle| BILL_TS
    STUDIO_TS <-->|同机房同步 gRPC| BILL_TS
    BILL_TS --> PG_GW
    FIN_WK --> PG_GW
    STUDIO_TS --> PG_GW
    BFF_TS --> PG_GW

    GW_GO -->|HTTPS 直连调用| UPSTREAM
    CNT_GO -->|只读 JSON-RPC| ETH
    FIN_WK -.->|7天边界轻量只读对账| ETH

    PRV_GO ==>|异地组网 + gRPC + TLS + HMAC:<br/>推送 CatalogSnapshot & 公钥加密凭证| GW_GO
    PRV_GO ==>|异地组网 + gRPC + TLS + HMAC:<br/>同步只读定价/预算快照副本| BILL_TS
    GW_GO ==>|异地组网 + gRPC + TLS + HMAC:<br/>分钟级批量回推 Uptime/延迟/调用汇总| PRV_GO

    BILL_TS ==>|异地组网 + gRPC + TLS + HMAC:<br/>推送/增量拉取 钱包绑定与条款状态| CNT_GO
    CNT_GO ==>|异地组网 + gRPC + TLS + HMAC:<br/>推送 7天边界用户 Credits 入账| BILL_TS

    PRV_GO --> PG_PRV
    CNT_GO --> PG_CNT
```

### 2.2 异地组网与 `gRPC + TLS + HMAC` 安全通信规范
1. **底层异地组网（WireGuard 虚拟专网）**：
   - 采用自建 Headscale / Tailscale 或 WireGuard Mesh 将跨地域、跨云的 `gateway 后端`、`Provider 后端`、`合约后端` 编入统一私有网段（同云同可用区优先走原生 VPC 内网）。
   - 所有跨仓 gRPC 服务仅监听虚拟专网网卡 IP，**公网防火墙零开放内部 gRPC 端口**。
2. **传输与应用层三保险（`WireGuard + TLS + HMAC-SHA256`）**：
   - 统一封装 Go 与 TS 的 gRPC Unary / Stream Interceptor，所有跨仓调用强制携带 Metadata：
     - `x-ms-service-id`：调用方服务身份（如 `provider-service`、`contract-service`、`gateway-billing`）；
     - `x-ms-timestamp`：Unix 秒级时间戳，接收方强制校验 `|now - ts| <= 300s`；
     - `x-ms-nonce`：128-bit 随机唯一串，接收方在 10 分钟滑动窗口内做幂等防重放拦截；
     - `x-ms-signature`：`HMAC-SHA256(shared_secret_by_service_id, full_grpc_method + "\n" + timestamp + "\n" + nonce + "\n" + sha256_hex(protobuf_payload))`（流式消息按帧序号签名）。
   - 支持双密钥平滑轮换（`active_secret` + `previous_secret` 宽限期并存）。
3. **跨网断连自治（零跨网热路径依赖）**：
   - **推理热路径零跨网 RPC**：用户请求进入 `gateway 后端` 时，路由与计费全部基于 `gateway 后端` 本地内存与本地 PG 快照副本完成，**绝不在推理请求热路径同步跨网调用 `Provider 后端` 或 `合约后端`**。
   - **断网容灾**：异地组网中断或对端重启期间，Gateway 依靠本地持久化的最新有效 `CatalogSnapshot` 与加密凭证继续 100% 正常服务；分钟级指标回推、钱包绑定同步与合约 Credits 推送均内置本地持久化重试队列，网络恢复后自动幂等补齐。

---

## 3. 三大跨仓核心交互链路详解

### 3.1 `Provider 后端` ↔ `gateway 后端`（目录定价下发、非对称凭证加密与指标回流）

1. **双模式治理与全套定价归口（`Provider 后端`）**：
   - 一期即独立上线 `maxshot-provider-service`（Go）与 `maxshot-provider-web`（React），迁入原 Gateway Admin 中的供应商与目录配置；一期供内部运营使用，二期开放“外部供应商自助提审 + Maxshot 审批”，同时始终保留 Maxshot 运营直管录入能力。
   - `Provider 后端` 统一维护每个 `Offering` 的 **供给侧信息**（原生模型 ID、端点、能力限制、数据政策、供应商内部成本卡）与 **零售侧商业规则**（映射的客户 `modelId`、客户公开费率卡、Basic/Premium 档位标签、运营自定义目录 `catalogId`、模型默认 `max_tokens`、供应商日/月预算）。供应商修改价格、版本或能力后自动暂停新流量，重审通过后恢复（`PRV-R28`）。
2. **版本化快照下发（Provider → Gateway）**：
   - 每次审批发布或启停变更，`Provider 后端` 生成不可变版本号 `catalogVersion`，通过 `gRPC + HMAC`（主动推送 + Gateway 定时对账拉取兜底）同步至 `gateway 后端`：
     - `gateway-dataplane`（Go）将新快照写入内存并原子切换指针（RCU），用于微秒级路由（筛除在线率/延迟/隐私不达标项后按最低供给成本选路，未 Pin 允许首字前 ≤1 次同模型 Fallback）与 Key 权限过滤版 `/v1/models`；
     - `billing-account-service`（TS）将同版本快照持久化写入本地 `billing.catalog_snapshots_replica` 只读表，用于在扣费事务内零信任重算费用。
3. **上游供应商 API Key 非对称信封加密（Provider → Gateway）**：
   - `gateway 后端` 生成非对称密钥对（私钥仅存于 Gateway KMS/受限密钥环境），公钥注册给 `Provider 后端`；
   - 供应商或运营在 `Provider 门户` 录入上游 API Key 时，`Provider 后端` 生成随机数据密钥（DEK）加密 API Key，再用 **Gateway 公钥** 加密 DEK 存库，并通过专用 `SyncEncryptedCredentials` gRPC 接口同步密文给 `gateway-dataplane`；
   - 即使 `Provider 后端` 数据库泄露，攻击者也无法解密任何上游凭证；`gateway-dataplane` 在内存中用本地私钥解密后直连上游大模型。
4. **观测指标与应付汇总回传（Gateway → Provider）**：
   - `gateway-dataplane` 与 `finance-worker` 按 1~5 分钟滑动窗口在本地预聚合各 `offeringId` 的总调用数、Delivered Success 在线率、TTFT/Completion p50/p95 延迟、429/5xx 分类计数及已结算供应商应付金额汇总，通过 `gRPC + HMAC` 批量调用 `Provider 后端` 的 `ReportOfferingMetrics` 接口，驱动门户指标展示与对账单生成（权威明细仍以 Gateway 财务库为准，支持按日同步或按请求 ID 核查）。

### 3.2 `gateway 后端` 内部推理与强一致计费链路（Go 数据面 ↔ TS 计费核心）

```mermaid
sequenceDiagram
    autonumber
    participant Client as Client / Web(BFF) / Agent
    participant GW as gateway-dataplane (Go)
    participant Redis as Gateway Redis
    participant Bill as billing-account-service (TS)
    participant Upstream as 上游大模型供应商

    Client->>GW: POST /v1/chat/completions (API Key 或 BFF 内部主体, [Idempotency-Key], modelId, [offeringId], [max_tokens], [max_cost])
    GW->>GW: 生成 trace_id; 校验可选 Idempotency-Key 24h 加密缓存与请求体指纹
    GW->>Redis: 0. Lua 滑动窗口检查 RPM / TPM / 最大并发 (超限直返 429 + trace_id)
    GW->>GW: 读内存 CatalogSnapshot(vN) 选 Primary + ≤1 Backup(未Pin)<br/>估算输入 Token 上界; 注入 effective_max_tokens = min(caller, model_default/offering_max)
    GW->>Bill: 1. 同机房同步 gRPC AuthorizeAndHold(catalogVersion, primaryId, backupId, inputUpper, effectiveMaxTokens...)
    Note over Bill: 单一 PG 事务(零信任重算):<br/>1. 查本地 catalogVersion 副本重算档位/目录/客户费率/Hard Max<br/>2. 校验 Key 目录白名单(必填) + 可选模型白名单<br/>3. 校验 Key 预算 + 供应商/平台/业务线日/月预算<br/>4. 6桶原子预留(AIT→Basic→[Continue with Premium when Basic is not enough]→Premium→Free→Ref→Dollar)
    alt 准入拒绝
        Bill-->>GW: Reject(stable_error_code)
        GW-->>Client: 4xx JSON (仅含 trace_id, 无 request_id, 无扣款)
    else 预留成功
        Bill-->>GW: Admitted(request_id, hold_id, lease_token, basic_to_premium_used)
        GW->>Bill: 2. 同步 gRPC MarkSending(request_id, attempt_id, offering_id, dispatch_key, lease_token)
        GW->>Upstream: 内存解密凭证派发 (锁定端点/禁上游 fallback/带 effective_max_tokens)
        Upstream-->>GW: SSE 流式分片 + native_request_id + Final Usage
        GW-->>Client: 透传响应 (携带 request_id 与 trace_id)
        GW->>Bill: 3. 同步 gRPC Settle(request_id, lease_token, serving_offering_id, native_request_id, usage, outcome)
        Note over GW,Bill: 若 gRPC 失败则写 Go 本地持久化磁盘队列重试;<br/>缺失 Usage 进入 pending_reconciliation 由 finance-worker 24h Recovery 补拉闭环
    end
```

### 3.3 `合约后端` ↔ `gateway 前端 / 后端`（前端只读与链上直签、资格同步、Credits 推送入账）

1. **双平面端口隔离与 `Gateway 前端` 合约交互（合约仓库/合约后端 ↔ Gateway 前端）**：
   - **链上写交易（前端钱包直签上链）**：`Gateway 前端（仓2）` 集成钱包连接器（wagmi/viem）并引用 `合约仓库（仓3）` 导出的版本化 ABI 与合约地址注册表，MAX 锁仓铸造 AIT、质押、解质押、烧毁、7 天到期提取及跨链均由用户钱包直接对链上合约签名发送（满足 `TOK-R19` 链上主权，后端维护不阻塞链上赎回）。
   - **链上数据读取（`Gateway 前端` 浏览器直连 `合约后端` 公网只读 HTTPS API）**：`合约后端（仓4）` 独立暴露经过 CORS 与速率限制保护的公网只读 HTTPS 接口（如 `token-api.maxshot.ai`），直接向 `Gateway 前端` Tokenomics 页面提供 18 位整数精确铸造报价（`TOK-R17`）、HWM 曲线参数、合并仓位与 7 天待提取倒计时、Finalized 质押状态与下期预估 Credits、近 12 周发放历史、链上交易记录及全网 MAX/AIT 统计面板（`TOK-R38`）。涉及登录态的数据通过本地验证 Gateway JWT 公钥（JWKS）鉴权，实现跨团队前后端独立迭代；**所有写账本/发 Credits 的内部 `gRPC` 接口仍严格锁死在 WireGuard 虚拟专网网卡上，公网零暴露**。
2. **资格状态同步（Gateway 后端 → 合约后端）**：
   - 用户在 Gateway 完成 SIWE/ERC-1271 钱包 1:1 绑定或签署当期 Funded Term 条款时，`billing-account-service` 通过异地私网 `gRPC + HMAC` 调用 `合约后端` 的 `SyncWalletEligibility(walletAddress, accountId, linkedAt, termId, acceptedTermsVersion, acceptedAt)`，同时提供游标增量拉取接口 `PullWalletEligibilityUpdates` 供 `合约后端` 启动或断网恢复时主动对齐。
3. **7 天边界 Credits 推送与四重入账校验（合约后端 → Gateway 后端）**：
   - `合约后端`（Go）监听 Ethereum `finalized` 区块质押事件，结合资格时间裁剪起点 `max(stakeAt, linkedAt, termStart, acceptedAt)`，按 `TOK-R25` 整数公式算出每个账户在当前 7 天边界应发的 `AIT Earned Credit`，通过私网 `gRPC + HMAC` 调用 `gateway 后端` 的 `IssueAitCreditGrants` 批量/单笔推送。
   - `billing-account-service` 执行 **4 重强校验**后原子入账：① HMAC 服务身份与防重放；② 核对本地权威表中的 1:1 钱包绑定、条款接受时间及非排除名单（`TOK-R36`）；③ `(termId, boundaryIndex, accountId)` 幂等防重；④ 用 `TOK-R25` 公式二次验算 `cumulativeEligibleAitWeiSeconds` 并检查当期 `Funding Reservation` 资金硬顶与每日 Coverage Guard。
4. **7 天边界只读链上抽样对账 & 退款隔离**：
   - `finance-worker` 在每个 7 天边界后直连只读以太坊 RPC 核对当期入账总额 ≤ 全网合约合格质押理论上限并随机抽样复核，防单点索引 Bug 超发；
   - 已结束 Term 的 AIT 退款挂靠独立 **`Refund Reserve`**，生成打标 `refund_grant` 的 7 天新额度（沿用原目录集，**不计入 `TOK-R25` 累计已发额度**，待产品补录一条正式 TD 锁定缓冲比例）。

### 3.4 三种模型与 Provider 选择模式的分仓落位（指定模型 / Pin 供应商 / Auto 自动选模型）

遵循 **“静态候选池与策略配置在 `Provider 仓`、请求级毫秒匹配在 `Gateway Go 数据面`、额度与权限硬顶在 `Gateway 计费服务`”** 原则，推理热路径绝不跨网请求 `Provider 后端`：

| 调用模式 | 调用方传参 | `Provider 前后端`（仓5/6）职责 | `Gateway Go 数据面`（仓1）职责 | `Gateway TS 计费`（仓1）职责 |
| --- | --- | --- | --- | --- |
| **① 指定模型 + 自动选 Provider**（首发默认主路径） | 传具体 `modelId`，不传 `offeringId` | 维护该模型下所有已准入 Offering、能力、成本卡与零售费率卡，打入 `CatalogSnapshot` 下发；运行离线推荐列表筛选任务（原 `model-update-selection`） | 读内存快照筛除能力/地域/隐私/在线率不达标或熔断节点，确定性选出**供给成本最低**的 Primary + ≤1 个同模型 Backup | 按该模型目录与档位重算 Hard Max（取主备较高客户费率）并原子预留 |
| **② 指定模型 + 指定 Provider**（Pin 锁定模式） | 传具体 `modelId` + `offeringId` | 提供该 `offeringId` 的准入状态、端点与双层费率快照 | 仅校验该指定 `offeringId`：合格则直发且**严禁任何 Fallback 切换**；不合格或熔断则派发前直接报错拒单（`PRV-R22`） | 仅按该单一指定 Offering 的客户费率重算并预留 |
| **③ 自动选模型**（`maxshot/auto`，后续 Web Chat 试点） | 传虚拟模型 `maxshot/auto` | 维护经评测获批的 **Auto 精选池（Auto Pool）**、任务标签（`ordinary_chat` / `coding` / `complex_reasoning`）、排序权重与单条费用上限，随同一 `CatalogSnapshot` 推送 | 1. 零额外付费调用的本地规则识别请求特征（附件/图片、Search/Think 开关、上下文长度、任务标签、同会话模型粘性 Stickiness）<br>2. 在内存 Auto 池中匹配出具体的 `(modelId, primaryOfferingId, backupOfferingId)`<br>3. **选定模型后完全收敛至模式 ①（中途绝不跨模型重试）**，在响应与 Usage 中返回实际服务模型与供应商 | 校验最终选定模型是否符合用户套餐/Key 目录权限及 Auto 单条费用硬顶（超顶拒单或要求前端确认） |

```mermaid
flowchart LR
    REQ["请求进入 Gateway Go 数据面"] --> MODE{"传参是否为 maxshot/auto?"}
    MODE -->|"是 (Auto 选模型)"| AUTO["本地规则识别任务与能力<br/>从内存 Auto Pool 解析出具体 modelId"]
    MODE -->|"否 (已指定具体 modelId)"| PIN{"是否指定 offeringId?"}
    AUTO --> PIN
    PIN -->|"已指定 (Pin 模式)"| ONE["校验单一指定 Offering<br/>(不合格直接拒单, 严禁 Fallback)"]
    PIN -->|"未指定 (Auto 选供应商)"| TWO["筛合格节点 → 选最低供给成本 Primary<br/>+ ≤1 同模型 Backup"]
    ONE --> HOLD["同机房三步同步 gRPC 对接 TS 计费<br/>(AuthorizeAndHold → MarkSending → Settle)"]
    TWO --> HOLD
```

---

## 4. 跨仓与内部核心 Protobuf 契约清单（对齐 `maxshot-proto` 实现）

```protobuf
// 1. proto/maxshot/provider_sync/v1/provider_sync.proto (Provider 后端 <-> Gateway 后端, WireGuard + TLS + HMAC)
service ProviderSyncService { // Server: Provider 后端
  rpc WatchSyncEvents(WatchSyncEventsRequest) returns (stream WatchSyncEventsResponse); // 轻量版本号通知流
  rpc ReportNodeSyncState(ReportNodeSyncStateRequest) returns (ReportNodeSyncStateResponse);
  rpc GetCatalogSnapshot(GetCatalogSnapshotRequest) returns (GetCatalogSnapshotResponse); // 精确十进制费率 + TierGroup + Curation + 7d性能
  rpc StreamCatalogSnapshotChunks(StreamCatalogSnapshotChunksRequest) returns (stream StreamCatalogSnapshotChunksResponse);
  rpc SyncGatewayPublicKeys(SyncGatewayPublicKeysRequest) returns (SyncGatewayPublicKeysResponse);
  rpc SyncEncryptedCredentials(SyncEncryptedCredentialsRequest) returns (SyncEncryptedCredentialsResponse);
  rpc ReportOfferingMetrics(ReportOfferingMetricsRequest) returns (ReportOfferingMetricsResponse);
  rpc SyncProviderStatements(SyncProviderStatementsRequest) returns (SyncProviderStatementsResponse);
}

service GatewayReconciliationService { // Server: Gateway 计费服务（供 Provider 对账争议按需分页查脱敏明细）
  rpc ListOfferingAttemptRecords(ListOfferingAttemptRecordsRequest) returns (ListOfferingAttemptRecordsResponse);
}

// 2. proto/maxshot/contract_grant/v1/contract_grant.proto (合约后端 <-> Gateway 后端, WireGuard + TLS + HMAC)
service ContractBackendService { // Server: 合约后端
  rpc PushFundedTerms(PushFundedTermsRequest) returns (PushFundedTermsResponse); // Gateway 为 FundedTerm 主库
  rpc PushWalletEligibility(PushWalletEligibilityRequest) returns (PushWalletEligibilityResponse);
  rpc GetContractOperationalStatus(GetContractOperationalStatusRequest) returns (GetContractOperationalStatusResponse);
}

service AitCreditGrantService { // Server: Gateway 计费服务
  rpc ListFundedTerms(ListFundedTermsRequest) returns (ListFundedTermsResponse);
  rpc PullWalletEligibilityUpdates(PullWalletEligibilityUpdatesRequest) returns (PullWalletEligibilityUpdatesResponse);
  rpc IssueAitCreditGrants(IssueAitCreditGrantsRequest) returns (IssueAitCreditGrantsResponse);
}

// 3. proto/maxshot/gateway_internal/v1/execution.proto (Gateway 同机房全模态统一账本契约)
service BillingExecutionService { // Server: TS billing-account-service
  rpc ResolvePrincipalScope(ResolvePrincipalScopeRequest) returns (ResolvePrincipalScopeResponse);
  rpc PreviewQuote(PreviewQuoteRequest) returns (PreviewQuoteResponse);
  rpc AuthorizeAndHold(AuthorizeAndHoldRequest) returns (AuthorizeAndHoldResponse);
  rpc MarkSending(MarkSendingRequest) returns (MarkSendingResponse);
  rpc ExtendExecutionLease(ExtendExecutionLeaseRequest) returns (ExtendExecutionLeaseResponse);
  rpc Settle(SettleRequest) returns (SettleResponse);
  rpc MarkUncertain(MarkUncertainRequest) returns (MarkUncertainResponse);
  rpc ResolveUncertain(ResolveUncertainRequest) returns (ResolveUncertainResponse);
}
```

---

## 5. `gateway 后端` 内部隔离与数据库迁移规范

- **三 Schema + Role 硬隔离**：剥离 Provider 库（独立为 `Provider 后端` 自有 PG）与链上索引库（独立为 `合约后端` 自有 PG）后，`gateway 后端` 的 PostgreSQL 16 集群收敛为 `billing`、`studio`、`bff` 三个独立 Schema，分别授予 `billing_rw`、`studio_rw`、`bff_rw` 最小权限角色；Go `gateway-dataplane` 完全不连 PG。
- **Expand/Contract 无停写独立发版**：各服务仅迁移自身 Schema，遵循“先加兼容列/表 → 新代码上线 → 下周期清理旧字段”，彻底消除历史“发版全站停写排空”限制。
- **前置历史债清理**：Phase 0 统一将线上存量 `credits_v1` 账户迁入 `dollar_v1`（仅维护单一 6 桶模型）；存量 API Key 的 `allowedCatalogIds` 自动回填当前全部已启用目录，原 `modelScope` 保留为可选模型白名单（新 Key 空目录即全拒，遵循 `BIL-R12`）。

---

## 6. 分阶段演进路线图与风险预案

### 6.1 四阶段落地路线图

| 阶段 | 核心目标 | 关键交付物（跨 6 仓协同） | 验收标准 |
| --- | --- | --- | --- |
| **Phase 0：跨仓契约、组网基建与存量账本清理** | 打通异地组网与 `gRPC + HMAC` 底座，清理 Gateway 历史包袱 | 1. 搭建 WireGuard 异地虚拟专网，落地 Go/TS 通用 `gRPC + TLS + HMAC` 拦截器与单源 `.proto` 仓库/Submodule<br>2. `maxshot-gateway` 完成存量账户统一迁至 `dollar_v1`、存量 Key 目录回填，建立 `billing/studio/bff` 三 Schema 权限隔离 | 跨网 `gRPC + HMAC` 连通性与防重放压测通过；存量账户 100% 统一为 `dollar_v1` |
| **Phase 1：Provider 前后端独立上线 + Go 数据面全量切换（Foundation 闭环）** | 跑通 `Provider 仓 ↔ Gateway 仓` 跨网闭环，一步到位替换旧 TS 网关 | 1. 新建并上线 `maxshot-provider-service`（Go）与 `maxshot-provider-web`（React），迁入多 Provider/模型/目录/双层费率配置，实现非对称公钥加密凭证与 `CatalogSnapshot` 下发（一期内部运营使用）<br>2. `maxshot-gateway` 内 `billing-account-service` 上线本地快照副本与三步同步 gRPC，剥离 `finance-worker` 承担通用 24h Recovery<br>3. 用 **Go** 完整实现 `gateway-dataplane`（5 类协议、边缘限流、内存选路与单次 Fallback、默认 `max_tokens`、`Idempotency-Key`、分钟级指标回推），预发流量回放 Diff 达标后**原地址、原路径一次性全量切换** | `Provider 门户` 发布模型/改价秒级同步至 Gateway 并锁价生效；多 Provider 路由、Fallback 与三账对账全部通过验收 |
| **Phase 2：合约仓 & 合约后端联调上线（MAX/AIT 闭环）** | 打通链上质押 → 合约后端 → Gateway 入账全链路 | 1. `maxshot-contracts` 完成合约审计与测试网/主网部署准备，导出标准 ABI<br>2. 新建并上线 `maxshot-contract-service`（Go），打通从 Gateway 同步钱包绑定/条款状态及 7 天边界 `IssueAitCreditGrants` 推送<br>3. Gateway 上线 `AIT Earned Credit` 第一优先级桶、`Refund Reserve`、Coverage Guard 与 7 天只读链上抽样对账 | `SPEC_TOKEN` 全部整数参考向量与重放幂等测试 100% 一致；断网重连自动补推无重发漏发 |
| **Phase 3：Provider 门户开放外部供应商入驻 & Gateway 内部彻底去耦** | 全面开放供应商生态，完成 Studio 与 BFF 彻底解耦 | 1. `maxshot-provider-web` 与 `maxshot-provider-service` 开放外部供应商自助注册、提交 Offering、6 项准入审批流与供应商结算单视图<br>2. `maxshot-gateway` 完成 `studio-service/worker` 与 `app-adapter` 彻底切离 `server-core` 直连，锁死全部数据库跨 Schema 权限 | 外部供应商自助提审上线闭环跑通；6 个仓库各自独立 CI/CD 零耦合发版 |

### 6.2 关键风险与应对措施

| 风险点 | 潜在影响 | 架构应对措施 |
| --- | --- | --- |
| **异地组网跨地域抖动或断连** | Provider 配置无法下发、指标回推或合约 Credits 推送中断 | Gateway 本地内存 + 本地 PG 持久化缓存最新有效 `CatalogSnapshot` 与加密凭证，断网期间主站推理与计费 100% 自治不受影响；所有跨网推送均配本地持久化队列 + 幂等键 + 定时增量拉取兜底 |
| **`Provider 后端` 与 `gateway-dataplane` 同步用 Go 新写工作量集中** | Phase 1 交付周期拉长或协议边缘兼容遗漏 | 一期 `Provider 前后端` 聚焦内部运营必需字段，砍掉二期外部供应商自助入驻界面；Go 数据面切换前跑现役全套契约测试与真实录制流量回放 Diff，切流当天保留旧 TS 网关热备秒级回切 |
| **跨仓修改 `.proto` 引发上下游兼容故障** | 跨仓 gRPC 解码错误或字段语义漂移 | CI 引入 `buf breaking` 强制禁止删改已有字段编号与类型，所有跨仓接口变更必须向后兼容（先发接收方、再发发送方） |
| **上游供应商 API Key 跨网传输泄露风险** | 供应商额度被盗刷 | 强制采用 Gateway 公钥非对称混合信封加密 + WireGuard 加密隧道 + TLS + HMAC 签名，Provider 库与跨网抓包均无明文，Gateway 仅在内存解密且严禁打日志 |
