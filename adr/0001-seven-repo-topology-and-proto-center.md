# ADR-0001：采用“6 业务仓 + 1 契约架构中枢仓（`maxshot-proto`）”拓扑

- **状态**：已采纳（2026-11-26）
- **决策**：
  1. 业务侧按领域与前后端拆分为 6 个独立仓库：`maxshot-gateway`（Gateway 后端）、`maxshot-gateway-web`（Gateway 前端）、`maxshot-contracts`（智能合约）、`maxshot-contract-service`（合约后端）、`maxshot-provider-service`（Provider 后端）、`maxshot-provider-web`（Provider 前端）。
  2. 独立设立第 7 号无业务依赖底层仓 `maxshot-proto`，统一维护所有跨仓及 Gateway 内部 `.proto` 契约、预生成 `gen/go` 与 `gen/ts` 代码、双语言 `grpcauth` 拦截器与全局技术架构文档。
  3. `Provider 后端` 与 `合约后端` 保留 Go 或 TypeScript 的灵活选型空间；`maxshot-proto` 对所有服务对等提供 Go 与 TS 的 Client/Server Stubs 及鉴权中间件。
- **后果**：各团队可独立排期、独立 CI/CD 部署，通过 `buf breaking` 与语义化 Git Tag 消除手工复制 `.proto` 带来的跨仓协议漂移。
