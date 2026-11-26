# `maxshot-proto` — Maxshot 契约与全局架构中枢仓

`MaxShotLab/maxshot-proto` 是 Maxshot 全局“6 业务仓 + 1 契约架构仓”体系中的无依赖底层契约仓库，统一维护：

1. **全量 Protobuf `.proto` 契约源**（跨仓异地组网交互 + Gateway 同机房内部交互）；
2. **预生成双语言 gRPC Stubs**（`gen/go` 与 `gen/ts`，下游无需在本地或 CI 安装 `protoc` / `buf`）；
3. **跨语言逐字节一致的 `grpc-hmac` 双向拦截器中间件**（Go `pkg/grpcauth` 与 TypeScript `@maxshot/proto/grpcauth`，含方法级 ACL、±5 分钟时间窗、Nonce 防重放与双密钥平滑轮换）；
4. **全局技术架构文档、Archify 交互式可视化图与跨仓 ADR**（位于 [`architecture/`](./architecture/) 与 [`adr/`](./adr/)）。

---

## 1. 目录结构与契约分包

```text
maxshot-proto/
├── proto/maxshot/
│   ├── common/v1/types.proto              # 模态、档位、标准拒绝码、增量分页游标
│   ├── provider_sync/v1/provider_sync.proto   # [跨仓] Provider 后端 <-> Gateway 后端
│   ├── contract_grant/v1/contract_grant.proto # [跨仓] 合约后端 <-> Gateway 后端
│   └── gateway_internal/v1/execution.proto    # [同机房] Gateway Go 数据面 <-> TS 计费核心
├── gen/go/                                # Go 预编译 Stubs (Client + Server)
├── gen/ts/                                # TypeScript 预编译 Stubs (@grpc/grpc-js Client + Server)
├── pkg/grpcauth/                          # Go Unary & Stream 双向 HMAC + ACL 拦截器
├── ts/grpcauth/                           # TS Unary & Stream 双向 HMAC + ACL 拦截器
├── testdata/golden_vectors.json           # Go <-> TS 共享的黄金签名与二进制编码向量
├── architecture/                          # 全局架构总纲与 Archify 交互式可视化
└── adr/                                   # 跨仓架构决策记录 (ADR)
```

---

## 2. 安装与引用方式

### Go 仓库引入（适用于任意 Go 服务端/客户端）

```bash
go get github.com/MaxShotLab/maxshot-proto@v0.1.0
```

### TypeScript 仓库引入（适用于任意 Node.js/TS 服务端/客户端）

在 `package.json` 中通过 Git Tag 引入（拉取时会自动编译 `dist/`）：

```json
{
  "dependencies": {
    "@grpc/grpc-js": "^1.14.0",
    "@maxshot/proto": "github:MaxShotLab/maxshot-proto#v0.1.0"
  }
}
```

---

## 3. 全语言组合接入示例（Go / TypeScript 任意组合互通）

由于 `Provider 后端` 与 `合约后端` 最终选型可能是 **Go** 也可能是 **TypeScript**，本仓库所有服务均同时生成了 **Go Client/Server** 与 **TS Client/Server**，并通过 `testdata/golden_vectors.json` 保证跨语言 Protobuf 线格式与 `HMAC-SHA256` 签名 100% 互通。

---

### 场景一：`Provider 后端` ↔ `Gateway 后端`（`provider_sync.v1`）

#### 组合 1A：`Provider 后端` 使用 **Go** 实现 Server

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
			"/maxshot.provider_sync.v1.ProviderSyncService/*": {"gateway-dp", "gateway-billing"},
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

#### 组合 1B：`Provider 后端` 使用 **TypeScript** 实现 Server

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
          "/maxshot.provider_sync.v1.ProviderSyncService/*": ["gateway-dp", "gateway-billing"],
        },
      }),
    ],
  });

  const impl: Partial<providerSyncV1.ProviderSyncServiceServer> = {
    getCatalogSnapshot(call, callback) {
      callback(null, {
        snapshot: {
          catalogVersion: "cat_20261126_01",
          generatedAtUnixMs: BigInt(Date.now()),
          catalogs: [],
          providers: [],
          models: [],
          offerings: [],
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
  server.bindAsync(wireguardAddr, grpc.ServerCredentials.createSsl(/* TLS certs */ null, []), () => {});
  return server;
}
```

#### 组合 1C：`Gateway 后端` 调用 `Provider 后端`（Go 数据面 Client & TS 计费 Client）

- **Go Client（`gateway-dataplane` 拉取快照 / 上报分钟级指标）**：
  ```go
  clientCfg := grpcauth.ClientConfig{
      ServiceID: "gateway-dp",
      KeyID:     "k1",
      Secret:    os.Getenv("MS_HMAC_SECRET_GATEWAY_DP"),
  }
  conn, err := grpc.NewClient(
      "provider.internal.wg:9090",
      grpc.WithTransportCredentials(tlsCreds),
      grpc.WithUnaryInterceptor(grpcauth.UnaryClientInterceptor(clientCfg)),
      grpc.WithStreamInterceptor(grpcauth.StreamClientInterceptor(clientCfg)),
  )
  prvClient := providersyncv1.NewProviderSyncServiceClient(conn)
  snapResp, err := prvClient.GetCatalogSnapshot(ctx, &providersyncv1.GetCatalogSnapshotRequest{})
  ```

- **TypeScript Client（`billing-account-service` 对账兜底拉取定价快照）**：
  ```ts
  import * as grpc from "@grpc/grpc-js";
  import { grpcauth, providerSyncV1 } from "@maxshot/proto";

  const client = new providerSyncV1.ProviderSyncServiceClient(
    "provider.internal.wg:9090",
    grpc.credentials.createSsl(),
  );
  const hmacInterceptor = grpcauth.createHmacClientInterceptor({
    serviceId: "gateway-billing",
    keyId: "k1",
    secret: process.env.MS_HMAC_SECRET_BILLING!,
  });

  client.getCatalogSnapshot(
    { catalogVersion: "" },
    new grpc.Metadata(),
    { interceptors: [hmacInterceptor] },
    (err, res) => {
      if (err) throw err;
      console.log("Synced catalog version:", res.snapshot?.catalogVersion);
    },
  );
  ```

---

### 场景二：`合约后端` ↔ `Gateway 后端`（`contract_grant.v1`）

#### 组合 2A：`合约后端` 使用 **Go**（推送 7 天 Credits + 接收/拉取钱包绑定资格）

```go
// 1. 作为 Client 向 Gateway Billing 推送 7 天边界 AIT Earned Credit
conn, _ := grpc.NewClient(
	"gateway-billing.internal.wg:9091",
	grpc.WithTransportCredentials(tlsCreds),
	grpc.WithUnaryInterceptor(grpcauth.UnaryClientInterceptor(grpcauth.ClientConfig{
		ServiceID: "contract-svc",
		KeyID:     "k1",
		Secret:    os.Getenv("MS_HMAC_SECRET_CONTRACT"),
	})),
)
grantClient := contractgrantv1.NewAitCreditGrantServiceClient(conn)
resp, err := grantClient.IssueAitCreditGrants(ctx, &contractgrantv1.IssueAitCreditGrantsRequest{
	BatchId:       "term1_b1",
	TermId:        "term_2026_q4",
	BoundaryIndex: 1,
	Items: []*contractgrantv1.AitGrantItem{
		{
			GrantId:                             "term_2026_q4:1:acc_1001",
			TermId:                              "term_2026_q4",
			BoundaryIndex:                       1,
			AccountId:                           "acc_1001",
			WalletAddress:                       "0xabc...",
			CumulativeEligibleAitWeiSecondsDec:  "604800000000000000000000",
			GrantCredits:                        230136,
			FinalizedBlockNumber:                21500000,
		},
	},
})

// 2. 同时可调用 WalletEligibilityServiceClient.PullWalletEligibilityUpdates 按游标增量拉取绑定关系
```

#### 组合 2B：`合约后端` 使用 **TypeScript**（推送 7 天 Credits + 增量拉取钱包绑定资格）

```ts
import * as grpc from "@grpc/grpc-js";
import { contractGrantV1, grpcauth } from "@maxshot/proto";

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
  {
    batchId: "term1_b1",
    termId: "term_2026_q4",
    boundaryIndex: 1,
    items: [
      {
        grantId: "term_2026_q4:1:acc_1001",
        termId: "term_2026_q4",
        boundaryIndex: 1,
        accountId: "acc_1001",
        walletAddress: "0xabc...",
        cumulativeEligibleAitWeiSecondsDec: "604800000000000000000000",
        previouslyGrantedCredits: 0n,
        grantCredits: 230136n,
        finalizedBlockNumber: 21500000n,
        boundaryTimestampUnixSec: 1795670000n,
      },
    ],
  },
  new grpc.Metadata(),
  { interceptors: [hmac] },
  (err, res) => {
    if (err) throw err;
    console.log("Grant results:", res.results);
  },
);
```

#### 组合 2C：`Gateway 后端`（TS `billing-account-service`）暴露 `AitCreditGrantService` Server

```ts
import * as grpc from "@grpc/grpc-js";
import { contractGrantV1, grpcauth } from "@maxshot/proto";

const server = new grpc.Server({
  interceptors: [
    grpcauth.createHmacServerInterceptor({
      resolveSecret: (svc, kid) => lookupSecret(svc, kid),
      // 严格方法级 ACL：仅允许 contract-svc 调用发币入账接口
      allowedCallers: {
        "/maxshot.contract_grant.v1.AitCreditGrantService/IssueAitCreditGrants": ["contract-svc"],
        "/maxshot.contract_grant.v1.WalletEligibilityService/PullWalletEligibilityUpdates": ["contract-svc"],
      },
    }),
  ],
});

server.addService(contractGrantV1.AitCreditGrantServiceService, {
  async issueAitCreditGrants(call, callback) {
    // 执行四重强校验：1:1 绑定与条款 -> grantId 幂等 -> TOK-R25 公式复核 -> Reserve 硬顶
    callback(null, { results: [] });
  },
});
```

---

### 场景三：`Gateway 后端` 同机房内部调用（`gateway_internal.v1`）

- **Go `gateway-dataplane`（Client）** 调用 **`gatewayInternalV1.BillingExecutionService`**：依次执行 `AuthorizeAndHold` → `MarkSending` → `Settle`（失败写入本地持久化重试队列）。
- **TS `billing-account-service`（Server）** 挂载 `gatewayInternalV1.BillingExecutionServiceService`，在同一 PostgreSQL `billing` Schema 事务内完成零信任费率重算、预算校验与 6 桶扣减。

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

**版本铁律**：禁止直接修改 `gen/` 目录下的生成代码；任何 `.proto` 修改必须执行 `make check` 确保 `gen/` 同步且零破坏性变更，合并 `main` 后打语义化版本 Tag（如 `v0.1.0` → `v0.2.0`）。
