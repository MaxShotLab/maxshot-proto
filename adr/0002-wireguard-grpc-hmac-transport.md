# ADR-0002：跨仓异地组网与统一 `gRPC + TLS + HMAC` 双向鉴权规范

- **状态**：已采纳（2026-11-26）
- **决策**：
  1. 跨地域/跨云部署的后端仓库（`Gateway 后端`、`Provider 后端`、`合约后端`）通过 WireGuard 虚拟专网（Headscale / WireGuard Mesh）互通，内部 `gRPC` 端口不对公网暴露。
  2. 所有服务间 `gRPC` 调用强制叠加 TLS 与应用层 `HMAC-SHA256` 签名校验（规范化原文：`v1\n{full_method}\n{service_id}\n{key_id}\n{timestamp}\n{nonce}\n{payload_sha256}`），校验 ±300 秒时间窗、10 分钟 Nonce 防重放与方法级 `service_id` 白名单 ACL。
  3. 上游大模型 API Key 由 `Provider 后端` 使用 `Gateway 后端` 公钥进行非对称混合信封加密后经专用 `gRPC` 接口下发，仅 `Gateway 后端` 持有私钥在内存解密。
  4. `合约后端` 实施公读私写双平面隔离：面向 `Gateway 前端` 独立开放公网只读 HTTPS API，面向 `Gateway 后端` 发币入账走私网 `gRPC + HMAC`。
