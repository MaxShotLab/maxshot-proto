# `maxshot-proto` — Maxshot 契约与全局架构中枢仓

`MaxShotLab/maxshot-proto` 是 Maxshot 全局“6 业务仓 + 1 契约架构仓”体系中的无依赖底层契约仓库，统一维护：

1. **全量 Protobuf `.proto` 契约源**（跨仓异地组网交互 + Gateway 同机房内部全模态账本交互）；
2. **预生成双语言 gRPC Stubs**（`gen/go` 与 `gen/ts`，下游无需在本地或 CI 安装 `protoc` / `buf`）；
3. **跨语言逐字节一致的 `grpc-hmac` 双向拦截器中间件**（Go `pkg/grpcauth` 与 TypeScript `@maxshot/proto/grpcauth`，含方法级 ACL、±5 分钟时间窗、Nonce 防重放与双密钥平滑轮换）；
4. **全局技术架构文档、Archify 交互式可视化图与跨仓 ADR**（位于 [`architecture/`](./architecture/) 与 [`adr/`](./adr/)）。

---

## 1. 目录结构与服务承载矩阵

```text
maxshot-proto/
├── proto/maxshot/
│   ├── common/v1/types.proto              # 模态、Tier、资金桶顺序、Key 策略、拒绝码、预算周期、分页游标
│   ├── provider_sync/v1/provider_sync.proto   # [跨仓] Provider 后端 <-> Gateway 后端（轻量事件流、快照/密文拉取、对账明细）
│   ├── contract_grant/v1/contract_grant.proto # [跨仓] 合约后端 <-> Gateway 后端（Term同步、钱包资格、7天发放、合约状态）
│   └── gateway_internal/v1/execution.proto    # [同机房] Go 数据面 / TS Studio / TS BFF <-> TS 计费核心全模态账本
├── gen/go/                                # Go 预编译 Stubs (Client + Server)
├── gen/ts/                                # TypeScript 预编译 Stubs (@grpc/grpc-js Client + Server)
├── pkg/grpcauth/                          # Go Unary & Stream 双向 HMAC + ACL 拦截器
├── ts/grpcauth/                           # TS Unary & Stream 双向 HMAC + ACL 拦截器
├── testdata/golden_vectors.json           # Go <-> TS 共享的黄金签名与二进制编码向量
├── architecture/                          # 全局架构总纲与 Archify 交互式可视化
└── adr/                                   # 跨仓架构决策记录 (ADR-0001..0003)
```

### 全局 5 大 gRPC 服务与方法级 ACL 归属表

| Protobuf Package | gRPC Service | 服务端承载方（Server） | 允许调用方 `service_id`（Client） | 核心职责 |
| --- | --- | --- | --- | --- |
| `maxshot.provider_sync.v1` | `ProviderSyncService` | **Provider 后端**（Go 或 TS） | `gateway-dp`, `gateway-billing`, `gateway-studio`, `gateway-finance` | `WatchSyncEvents` 轻量版本号流、节点同步心跳、`GetCatalogSnapshot`（含精确十进制费率、`SourceGroup` / `TierGroup` / `ModelTierOverride`、媒体能力与计费公式、Curation、7天观测性能）、公钥注册与加密凭证拉取、分钟级直方图指标上报、账期结算单同步 |
| `maxshot.provider_sync.v1` | `GatewayReconciliationService` | **Gateway 计费服务**（TS） | `provider-svc` | 按时间窗 / `offering_id` / `native_request_id` 按需分页拉取脱敏逐笔 `Attempt` 计量结算记录；接收 Provider Portal 确认的发票核对与争议调账并写入 Gateway Finance 差异账本（`PRV-R14 / ADM-R17 / ADM-R18`） |
| `maxshot.contract_grant.v1` | `ContractBackendService` | **合约后端**（Go 或 TS） | `gateway-billing`, `gateway-admin` | 接收 Gateway 下发的权威 `FundedTerm` 配置与 1:1 钱包绑定/条款签署推送；向运营台与 BFF 提供只读合约暂停位、全局代币指标与单账户质押边界权益视图（`ADM-R20..R23`） |
| `maxshot.contract_grant.v1` | `AitCreditGrantService` | **Gateway 计费服务**（TS） | `contract-svc` | 供合约后端启动/重连拉取 `FundedTerm` 与钱包资格增量，并接收 7 天边界 `W(t)` 质押 `AIT Earned Credit` 批量入账（含边界批次完整性汇总与链上 finalized 区块锚点） |
| `maxshot.gateway_internal.v1` | `BillingExecutionService` | **Gateway 计费服务**（TS） | `gateway-dp`, `gateway-studio`, `gateway-bff`, `gateway-finance`, `gateway-admin` | 主体/限流/资格解析（支撑无 DB `/v1/models`、`EvaluateCatalogDiscovery` `/v1/discovery/models` 与 Redis 限流）、提交前 `PreviewQuote`（含 `/me/funding-estimate` 资金窗口/桶余额、5 分钟 Studio 锁价 `quote_id` 与 Dollar 确认）、全模态统一 `AuthorizeAndHold` / `MarkSending` / `ExtendExecutionLease` / `Settle`（含 `provider_actual` 计费、差额补扣与 24h 自动释放）及 `MarkUncertain` / `ResolveUncertain`（含文本 24h 恢复与媒体 `uncertain`） |

---

## 2. 安装与引用方式

### Go 仓库引入（适用于任意 Go 服务端/客户端）

```bash
go get github.com/MaxShotLab/maxshot-proto@v0.1.0
```

### TypeScript 仓库引入（适用于任意 Node.js/TS 服务端/客户端）

在 `package.json` 中通过 Git Tag 引入（拉取时会自动执行 `prepare` 编译 `dist/`）：

```json
{
  "dependencies": {
    "@grpc/grpc-js": "^1.12.4",
    "@maxshot/proto": "github:MaxShotLab/maxshot-proto#v0.1.0"
  }
}
```

---

## 3. 全语言组合接入示例（Go / TypeScript 任意组合互通）

由于 `Provider 后端` 与 `合约后端` 最终选型可能是 **Go** 也可能是 **TypeScript**，本仓库所有服务均同时生成了 **Go Client/Server** 与 **TS Client/Server**，并通过 `testdata/golden_vectors.json` 保证跨语言 Protobuf 线格式与 `HMAC-SHA256` 签名 100% 互通。

---

### 场景一：`Provider 后端` ↔ `Gateway 后端`（`provider_sync.v1`）

#### 组合 1A：`Provider 后端` 使用 **Go**（承载 `ProviderSyncService` Server + 按需查对账明细 Client）

```go
package main

import (
	"context"
	"net"

	providersyncv1 "github.com/MaxShotLab/maxshot-proto/gen/go/maxshot/provider_sync/v1"
	"github.com/MaxShotLab/maxshot-proto/pkg/grpcauth"
	"google.golang.org/grpc"
)

type providerServer struct {
	providersyncv1.UnimplementedProviderSyncServiceServer
}

func (s *providerServer) WatchSyncEvents(req *providersyncv1.WatchSyncEventsRequest, stream grpc.ServerStreamingServer[providersyncv1.WatchSyncEventsResponse]) error {
	return stream.Send(&providersyncv1.WatchSyncEventsResponse{
		EventKind:                providersyncv1.SyncEventKind_SYNC_EVENT_KIND_CATALOG_PUBLISHED,
		LatestCatalogVersion:     "cat_20261126_01",
		LatestCredentialRevision: "cred_rev_01",
	})
}

func (s *providerServer) GetCatalogSnapshot(ctx context.Context, req *providersyncv1.GetCatalogSnapshotRequest) (*providersyncv1.GetCatalogSnapshotResponse, error) {
	return &providersyncv1.GetCatalogSnapshotResponse{
		Snapshot: &providersyncv1.CatalogSnapshot{CatalogVersion: "cat_20261126_01"},
	}, nil
}

func startGoProviderServer(wireguardAddr string, secrets map[string]string) error {
	authCfg := grpcauth.ServerConfig{
		ResolveSecret: func(svcID, keyID string) (string, bool) {
			sec, ok := secrets[svcID+":"+keyID]
			return sec, ok
		},
		AllowedCallers: map[string][]string{
			"/maxshot.provider_sync.v1.ProviderSyncService/*": {"gateway-dp", "gateway-billing", "gateway-finance"},
		},
	}
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(grpcauth.UnaryServerInterceptor(authCfg)),
		grpc.StreamInterceptor(grpcauth.StreamServerInterceptor(authCfg)),
	)
	providersyncv1.RegisterProviderSyncServiceServer(srv, &providerServer{})
	lis, err := net.Listen("tcp", wireguardAddr) // 仅绑定 WireGuard 专网网卡 IP
	if err != nil {
		return err
	}
	return srv.Serve(lis)
}
```

#### 组合 1B：`Provider 后端` 使用 **TypeScript**（承载 `ProviderSyncService` Server + 按需查对账明细 Client）

```ts
import * as grpc from "@grpc/grpc-js";
import { grpcauth, providerSyncV1 } from "@maxshot/proto";

export function startTsProviderServer(
  wireguardAddr: string,
  secrets: Record<string, string>,
): grpc.Server {
  const server = new grpc.Server({
    interceptors: [
      grpcauth.createHmacServerInterceptor({
        resolveSecret: (svcId, keyId) => secrets[`${svcId}:${keyId}`],
        allowedCallers: {
          "/maxshot.provider_sync.v1.ProviderSyncService/*": [
            "gateway-dp",
            "gateway-billing",
            "gateway-finance",
          ],
        },
      }),
    ],
  });

  const impl: Partial<providerSyncV1.ProviderSyncServiceServer> = {
    watchSyncEvents(call) {
      call.write({
        eventKind: providerSyncV1.SyncEventKind.SYNC_EVENT_KIND_CATALOG_PUBLISHED,
        latestCatalogVersion: "cat_20261126_01",
        latestCredentialRevision: "cred_rev_01",
        publishedAtUnixMs: BigInt(Date.now()),
      });
    },
    getCatalogSnapshot(call, callback) {
      callback(null, {
        snapshot: {
          catalogVersion: "cat_20261126_01",
          generatedAtUnixMs: BigInt(Date.now()),
          catalogs: [],
          providers: [],
          models: [],
          offerings: [],
          tierGroups: [],
          autoPool: [],
        },
      });
    },
    reportOfferingMetrics(call, callback) {
      callback(null, { accepted: true });
    },
  };

  server.addService(
    providerSyncV1.ProviderSyncServiceService,
    impl as providerSyncV1.ProviderSyncServiceServer,
  );
  server.bindAsync(wireguardAddr, grpc.ServerCredentials.createSsl(null, []), () => {});
  return server;
}
```

#### 组合 1C：`Gateway 后端` 订阅轻量事件流并按版本拉取（Go 数据面 & TS 计费）

- **Go Client（`gateway-dataplane` 订阅 `WatchSyncEvents` + 按版本拉取快照/密文 + 上报节点生效版本）**：
  ```go
  clientCfg := grpcauth.ClientConfig{
      ServiceID: "gateway-dp",
      KeyID:     "k1",
      Secret:    os.Getenv("MS_HMAC_SECRET_GATEWAY_DP"),
  }
  conn, _ := grpc.NewClient(
      "provider.internal.wg:9090",
      grpc.WithTransportCredentials(tlsCreds),
      grpc.WithUnaryInterceptor(grpcauth.UnaryClientInterceptor(clientCfg)),
      grpc.WithStreamInterceptor(grpcauth.StreamClientInterceptor(clientCfg)),
  )
  prvClient := providersyncv1.NewProviderSyncServiceClient(conn)
  stream, _ := prvClient.WatchSyncEvents(ctx, &providersyncv1.WatchSyncEventsRequest{
      GatewayNodeId: "gw-dp-01",
      NodeRole:      "dataplane",
  })
  ev, _ := stream.Recv()
  snapResp, _ := prvClient.GetCatalogSnapshot(ctx, &providersyncv1.GetCatalogSnapshotRequest{
      CatalogVersion: ev.GetLatestCatalogVersion(),
  })
  _, _ = prvClient.ReportNodeSyncState(ctx, &providersyncv1.ReportNodeSyncStateRequest{
      GatewayNodeId:          "gw-dp-01",
      NodeRole:               "dataplane",
      AppliedCatalogVersion:  snapResp.GetSnapshot().GetCatalogVersion(),
  })
  ```

- **TypeScript Server & Client（`billing-account-service` 同步定价副本 + 暴露 `GatewayReconciliationService` 供 Provider 查争议明细）**：
  ```ts
  import * as grpc from "@grpc/grpc-js";
  import { grpcauth, providerSyncV1 } from "@maxshot/proto";

  // 1. 作为 Client 拉取快照写入 billing.catalog_snapshots_replica
  const prvClient = new providerSyncV1.ProviderSyncServiceClient(
    "provider.internal.wg:9090",
    grpc.credentials.createSsl(),
  );
  const billingHmac = grpcauth.createHmacClientInterceptor({
    serviceId: "gateway-billing",
    keyId: "k1",
    secret: process.env.MS_HMAC_SECRET_BILLING!,
  });
  prvClient.getCatalogSnapshot(
    { catalogVersion: "" },
    new grpc.Metadata(),
    { interceptors: [billingHmac] },
    (err, res) => {
      if (err) throw err;
      console.log("Synced catalog version:", res.snapshot?.catalogVersion);
    },
  );

  // 2. 作为 Server 暴露脱敏 Attempt 明细按需查询（供 Provider 核查争议单）
  server.addService(providerSyncV1.GatewayReconciliationServiceService, {
    listOfferingAttemptRecords(call, callback) {
      callback(null, { records: [], page: { nextCursor: "", hasMore: false } });
    },
  });
  ```

---

### 场景二：`合约后端` ↔ `Gateway 后端`（`contract_grant.v1`）

#### 组合 2A：`合约后端` 使用 **Go**（承载 `ContractBackendService` Server + 调用 `AitCreditGrantService` Client）

```go
// 1. 承载 ContractBackendService Server（接收 Gateway 下发的 FundedTerm 与钱包绑定推送，提供合约暂停状态）
type contractServer struct {
	contractgrantv1.UnimplementedContractBackendServiceServer
}

func (s *contractServer) PushFundedTerms(ctx context.Context, req *contractgrantv1.PushFundedTermsRequest) (*contractgrantv1.PushFundedTermsResponse, error) {
	return &contractgrantv1.PushFundedTermsResponse{Accepted: true}, nil
}

// 2. 作为 Client 向 Gateway Billing 推送 7 天边界 AIT Earned Credit
grantClient := contractgrantv1.NewAitCreditGrantServiceClient(gatewayBillingConn)
resp, err := grantClient.IssueAitCreditGrants(ctx, &contractgrantv1.IssueAitCreditGrantsRequest{
	BatchId:       "term1_b1",
	TermId:        "term_2026_q4",
	BoundaryIndex: 1,
	Items: []*contractgrantv1.AitGrantItem{
		{
			GrantId:                            "term_2026_q4:1:acc_1001",
			TermId:                             "term_2026_q4",
			ProgramId:                          "prog_genesis_01",
			FundingReservationId:               "resv_2026_q4",
			BoundaryIndex:                      1,
			AccountId:                          "acc_1001",
			WalletAddress:                      "0xabc...",
			CumulativeEligibleAitWeiSecondsDec: "604800000000000000000000",
			CumulativeEntitlementCredits:       230136,
			GrantCredits:                       230136,
			FinalizedBlockNumber:               21500000,
		},
	},
})
```

#### 组合 2B：`合约后端` 使用 **TypeScript**（承载 `ContractBackendService` Server + 调用 `AitCreditGrantService` Client）

```ts
import * as grpc from "@grpc/grpc-js";
import { contractGrantV1, grpcauth } from "@maxshot/proto";

// 1. 承载 ContractBackendService Server（ACL 仅放行 gateway-billing / gateway-admin）
const contractServer = new grpc.Server({
  interceptors: [
    grpcauth.createHmacServerInterceptor({
      resolveSecret: (svc, kid) => lookupSecret(svc, kid),
      allowedCallers: {
        "/maxshot.contract_grant.v1.ContractBackendService/*": [
          "gateway-billing",
          "gateway-admin",
        ],
      },
    }),
  ],
});

contractServer.addService(contractGrantV1.ContractBackendServiceService, {
  pushFundedTerms(call, cb) {
    cb(null, { accepted: true });
  },
  pushWalletEligibility(call, cb) {
    cb(null, { accepted: true, highestSequence: 1n });
  },
  getContractOperationalStatus(call, cb) {
    cb(null, {
      mintPaused: false,
      burnPaused: false,
      withdrawPaused: false,
      positionPauseAll: false,
      maxBridgeGlobalPaused: false,
      directedBridgePauses: [],
      ethereumHeadBlock: 21500064n,
      ethereumFinalizedBlock: 21500000n,
      ethereumFinalizedTimestampUnixSec: 1795670000n,
      activeTermId: "term_2026_q4",
      nextGrantBoundaryUnixSec: 1796274800n,
    });
  },
});

// 2. 作为 Client 调用 Gateway Billing 推送 7 天质押额度
const hmac = grpcauth.createHmacClientInterceptor({
  serviceId: "contract-svc",
  keyId: "k1",
  secret: process.env.MS_HMAC_SECRET_CONTRACT!,
});
const grantClient = new contractGrantV1.AitCreditGrantServiceClient(
  "gateway-billing.internal.wg:9091",
  grpc.credentials.createSsl(),
);
grantClient.issueAitCreditGrants(
  { batchId: "term1_b1", termId: "term_2026_q4", boundaryIndex: 1, items: [] },
  new grpc.Metadata(),
  { interceptors: [hmac] },
  (err, res) => {
    if (err) throw err;
    console.log("Grant results:", res.results);
  },
);
```

---

### 场景三：`Gateway 后端` 同机房内部全模态账本调用（`gateway_internal.v1`）

- **Go `gateway-dataplane`（Client）**：
  - `GET /v1/models`：调用 `ResolvePrincipalScope` 获取 API Key 的 `allowed_catalog_ids` / `model_allowlist`（可做短 TTL Redis 缓存 + 吊销即时失效），零 PG 直连完成模型列表过滤；
  - 推理请求：依次执行 `AuthorizeAndHold` → `MarkSending` → `Settle`（缺失最终 Usage 时传 `SETTLEMENT_OUTCOME_PENDING_RECONCILIATION`，由账本打上 24 小时自动释放时间戳并交 `finance-worker` 补拉）。
- **TS `studio-service` & `studio-worker` / `app-adapter`（Client）**：
  - 提交前报价：调用 `PreviewQuote` 获取预估、Hard Max、分项明细、`F` 免费模型超额回落提示以及 Studio 是否需弹窗确认 Dollar（`requires_dollar_confirmation`）；
  - 异步图/视频生成：`AuthorizeAndHold`（校验最多 3 个并发 Pending 视频）→ `MarkSending` → 长作业 `ExtendExecutionLease` → `Settle`（支持部分交付按实际完成项结算）；若超时未返回终态则调 `MarkUncertain` 进入人工队列，由运营台或 24h 自动释放任务调用 `ResolveUncertain` 闭环。

---

## 4. 本地开发与契约变更规范

```bash
# 1. 检查 .proto 风格规范
pnpm proto:lint

# 2. 检查是否引入破坏性变更（对比 main 分支）
pnpm proto:breaking

# 3. 重新生成 gen/go 与 gen/ts 并编译 TS 包
make generate

# 4. 运行 Go + TS 双语言黄金测试向量与回环 gRPC 测试
make test
```

**版本铁律**：禁止直接修改 `gen/` 目录下的生成代码；任何 `.proto` 修改必须执行 `make check` 确保 `gen/` 同步且单测全绿，合并 `main` 后打语义化版本 Tag（如 `v0.1.0`）。
