# ADR-0003：全量规格与现役账本交叉评审后的 Protobuf 契约对齐决策

- **状态**：已采纳（2026-11-26）
- **背景**：对照《Maxshot Token 与 Gateway 文档梳理报告》、`spec/` 14 份规范、六仓架构规划与现役 `maxshot-gateway/packages/server-core` 源码完成逐字段审查与决策访谈，消除跨文档与现役代码之间的规则冲突。
- **核心决策**：
  1. **内部全模态统一账本（`gateway_internal.v1.BillingExecutionService`）**：统一承载 Go 数据面、TS Studio 异步媒体与 Web BFF 的账务原语，包含 `ResolvePrincipalScope`（支撑 Go 数据面零 PG 直连过滤 `/v1/models`）、`PreviewQuote`（支撑提交前报价与 Studio Dollar 确认）、`AuthorizeAndHold / MarkSending / ExtendExecutionLease / Settle` 及异步媒体 `MarkUncertain / ResolveUncertain`。
  2. **模型 Tier 分组、Key 策略与扣费冲突裁决**：
     - `Basic / Premium` 作为模型 Tier 标签与 `TierGroup` 分组由 `Provider 后端` 计算并随 `CatalogSnapshot`（连同 Chat/Studio Curation）下发；订阅套餐（Core/Plus 价格、额度与 `group_ids` 绑定）留在 `Gateway 计费服务`；
     - 保留账户级 `allow_basic_to_premium`（`Continue with Premium when Basic is not enough`）开关；
     - Scoped API Key 新增独立 `allow_ait_earned_credit` 开关（默认开启）；当 Key 为 `SUBSCRIPTION_FIRST` 且关闭 `dollar_relay_enabled` 时，在合资格的 `AIT Earned`（若开启）与订阅额度之后停止，拦截除订阅外的所有 PAYG 桶（Free、Referral、Dollar）；
     - 标为 `F`（`MODEL_TIER_FREE`）的免费模型在免费配额内按 `$0` 记账，免费配额耗尽后自动回落常规 6 桶报价与扣费。
  3. **超支差额补扣与 24 小时自动释放统一兜底**：保留现役 `server-core` 在双授权且未触及显式单次上限时的未冻结 Dollar 差额补扣（超出或未授权部分进 `discrepancy` 差异账，绝不产生负余额）；文本缺失最终 Usage（`PENDING_RECONCILIATION`）与媒体 `uncertain` 统一适用派发后 **24 小时未决自动释放客户预留**，晚到供应商成本记入差异账。
  4. **跨仓 `provider_sync.v1` 轻量流与按需对账**：采用 `WatchSyncEvents` 轻量版本号通知流 + 节点生效心跳 + 按版本号幂等拉取 `CatalogSnapshot`（支持 >4MiB 分块流）与加密凭证 + `SyncGatewayPublicKeys` 公钥轮换；费率统一采用精确十进制美元字符串 `rate_usd_dec`；保留分钟级直方图指标推送，新增账期结算单/修正争议同步与按需分页拉取脱敏 `Attempt` 明细的 `GatewayReconciliationService`。
  5. **跨仓 `contract_grant.v1` 权威边界与退款隔离**：`Gateway 计费` 作为 `FundedTerm` 配置与储备金的唯一主库并同步给 `合约后端`；有理数汇率 `N/D` 仅用于发放折算；结算后 AIT 退款由 Gateway 本地独立 `Refund Reserve` 签发 7 天替代额度（继承原允许目录集，不计入 `TOK-R25` 累计已发额，不混入 Tokenomics 12 周质押发放历史）；新增 `GetContractOperationalStatus` 供运营台只读监控合约暂停位与 `finalized` 高度。
  6. **全量需求二次对齐增补决策（1A–8A，2026-11-26）**：
     - **1A（Studio 与 Finance 凭证访问）**：`ProviderSyncService` 放行 `gateway-studio`（TS `studio-service`/`studio-worker`）与 `gateway-finance`（TS `finance-worker` 24h 恢复）；上游密文凭证支持 Gateway 集群级信封密钥及 `target_node_roles` 角色作用域，同时 Studio 查价改为纯读同步快照。
     - **2A（Studio 媒体能力与非线性计费）**：`CatalogSnapshot` 承载完整的 `MediaModelCapabilitySpec`、`MediaSafetyPolicy`、`MediaOfferingSpec`（含 Ark SSRF 域名白名单）与 `MediaPricingFormula`（支持 `VIDEO_TOKENS`、`MEGAPIXEL_SECONDS`、`RESOLUTION_TOKEN_LOOKUP`、免单参考图及最低收费），并在 `UsageBound.media_dimensions` 中传递计费维度以支撑零信任重算。
     - **3A（5 分钟报价锁与 `/me/funding-estimate`）**：`PreviewQuote` 支持签发 5 分钟锁价 `quote_id` + `request_hash`（与 `AuthorizeAndHold` 闭环），并返回 `/me/funding-estimate` 所需的 `bucket_balances`、`windows`（5h/7d）与 `key_gross_window`。
     - **4A（双口径客户扣费与 115% 预冻缓冲）**：`OfferingRecord` 与 `SettleRequest` 显式支持 `RATE_CARD_FORMULA` 与 `PROVIDER_ACTUAL_COST`（支持 `hold_buffer_bps=11500` 与 `markup_multiplier_dec`），超额时在未触及 `per_request_ceiling` 且双授权前提下允许从未冻结余额差额补扣。
     - **5A（现役演进计费规则采纳）**：保留 `#154` 规则（无活跃订阅时 `SUBSCRIPTION_FIRST + dollar_relay_enabled=false` 自动放行 PAYG 兜底）、按 `media_shares_subscription_windows` 策略允许媒体共享 5h/7d 窗口、`CatalogSnapshot` 补齐 `SourceGroupRecord` / `TierGroupRecord`（含 25% 自动发布熔断阈值）/ `ModelTierOverrideRecord` 及 `SearchPolicy`。
     - **6A（Go 数据面边缘契约补齐）**：`ResolvePrincipalScopeResponse` 补充 `max_concurrent_streams`、`rpm_limit`、`tpm_limit`、`spending_locked` 与 `key_expires_at_unix_ms`；新增 `EvaluateCatalogDiscovery` RPC 支撑 `/v1/discovery/models`；`OfferingRecord` 补充 `MandatoryReasoningPolicy`、`omittable_parameters`（`#154-4`）及 `ServerToolConfig`。
     - **7A（Provider Portal 与 Gateway Admin 双向对账）**：Provider Portal 承载 `ADM-R07/08/13..15/17`，确认后的发票修正/争议通过 `GatewayReconciliationService.ApplyProviderDisputeAdjustment` 回写 Gateway Finance 差异账本（`ADM-R18`）。
     - **8A（7 天边界完整性、TD-64 熔断限流与链上全局/单账户视图）**：`IssueAitCreditGrantsRequest` 补充 `BoundaryBatchSummary`（含批次数、边界总发额与 finalized 区块锚点）；`FundedTermConfig` 补充 `AitCoverageThrottlePolicy` 实现 `TD-64` 覆盖率熔断高成本模型临时限流；`GetContractOperationalStatusResponse` 补充 HWM/流通债务/总质押指标，并新增 `GetAccountStakingSummary` 支撑 `ADM-R20/21`。
