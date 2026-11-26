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
