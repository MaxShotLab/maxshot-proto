import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import * as grpc from "@grpc/grpc-js";

import {
  AitCreditGrantServiceClient,
  AitCreditGrantServiceService,
  GrantItemStatus,
  IssueAitCreditGrantsRequest,
  type AitCreditGrantServiceServer,
} from "../../gen/ts/maxshot/contract_grant/v1/contract_grant.js";
import {
  ProviderSyncServiceClient,
  ProviderSyncServiceService,
  SyncEventKind,
  type ProviderSyncServiceServer,
} from "../../gen/ts/maxshot/provider_sync/v1/provider_sync.js";
import {
  buildCanonicalString,
  createHmacClientInterceptor,
  createHmacServerInterceptor,
  payloadHashHex,
  signCanonical,
  verifySignature,
} from "./index.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

interface GoldenVector {
  name: string;
  full_method: string;
  service_id: string;
  key_id: string;
  secret: string;
  timestamp: string;
  nonce: string;
  batch_id?: string;
  term_id?: string;
  boundary_index?: number;
  payload_hex: string;
  expected_payload_sha256: string;
  expected_canonical: string;
  expected_signature: string;
}

test("TS matches shared golden_vectors.json and Go protobuf wire bytes", () => {
  const raw = fs.readFileSync(
    path.resolve(__dirname, "../../testdata/golden_vectors.json"),
    "utf8",
  );
  const parsed = JSON.parse(raw) as { vectors: GoldenVector[] };

  for (const v of parsed.vectors) {
    if (v.batch_id) {
      const encoded = Buffer.from(
        IssueAitCreditGrantsRequest.encode({
          batchId: v.batch_id,
          termId: v.term_id ?? "",
          boundaryIndex: v.boundary_index ?? 0,
          items: [],
        }).finish(),
      );
      assert.equal(
        encoded.toString("hex"),
        v.payload_hex,
        `ts-proto wire bytes must match Go for ${v.name}`,
      );
    }

    const buf = Buffer.from(v.payload_hex, "hex");
    const pHash = payloadHashHex(buf);
    assert.equal(pHash, v.expected_payload_sha256);

    const canonical = buildCanonicalString(
      v.full_method,
      v.service_id,
      v.key_id,
      v.timestamp,
      v.nonce,
      pHash,
    );
    assert.equal(canonical, v.expected_canonical);

    const sig = signCanonical(v.secret, canonical);
    assert.equal(sig, v.expected_signature);
    assert.equal(verifySignature(v.secret, canonical, v.expected_signature), true);
  }
});

test("TS loopback gRPC server + client interceptors enforce HMAC, ACL, and streaming", async () => {
  const fixedUnixSec = 1795670000;
  const secrets: Record<string, string> = {
    "contract-svc:k1": "secret-contract-k1",
    "gateway-dp:k1": "secret-gateway-k1",
  };

  const server = new grpc.Server({
    interceptors: [
      createHmacServerInterceptor({
        resolveSecret: (svc, kid) => secrets[`${svc}:${kid}`],
        allowedCallers: {
          "/maxshot.contract_grant.v1.AitCreditGrantService/IssueAitCreditGrants": [
            "contract-svc",
          ],
          "/maxshot.provider_sync.v1.ProviderSyncService/*": ["gateway-dp"],
        },
        nowUnixSec: () => fixedUnixSec,
      }),
    ],
  });

  const grantImpl: Partial<AitCreditGrantServiceServer> = {
    issueAitCreditGrants(call, callback) {
      callback(null, {
        results: [
          {
            grantId: call.request.batchId,
            status: GrantItemStatus.GRANT_ITEM_STATUS_CREDITED,
            detail: "ok",
            creditedAmount: 230136n,
            issuedAtUnixSec: 1795670000n,
            expiresAtUnixSec: 1796274800n,
          },
        ],
      });
    },
  };

  const providerImpl: Partial<ProviderSyncServiceServer> = {
    watchSyncEvents(call) {
      call.write({
        eventKind: SyncEventKind.SYNC_EVENT_KIND_CATALOG_PUBLISHED,
        latestCatalogVersion: "cat-ts-v1",
        latestCredentialRevision: "cred-rev-1",
        publishedAtUnixMs: 1795670000000n,
      });
      call.end();
    },
  };

  server.addService(
    AitCreditGrantServiceService,
    grantImpl as AitCreditGrantServiceServer,
  );
  server.addService(
    ProviderSyncServiceService,
    providerImpl as ProviderSyncServiceServer,
  );

  const port = await new Promise<number>((resolve, reject) => {
    server.bindAsync(
      "127.0.0.1:0",
      grpc.ServerCredentials.createInsecure(),
      (err, boundPort) => (err ? reject(err) : resolve(boundPort)),
    );
  });

  try {
    // 1. Authorized unary call succeeds
    const contractClient = new AitCreditGrantServiceClient(
      `127.0.0.1:${port}`,
      grpc.credentials.createInsecure(),
    );
    const unaryResp = await new Promise<{ results: { grantId: string }[] }>(
      (resolve, reject) => {
        contractClient.issueAitCreditGrants(
          { batchId: "batch-ts-1", termId: "t1", boundaryIndex: 1, items: [] },
          new grpc.Metadata(),
          {
            interceptors: [
              createHmacClientInterceptor({
                serviceId: "contract-svc",
                keyId: "k1",
                secret: "secret-contract-k1",
                nowUnixSec: () => fixedUnixSec,
              }),
            ],
          },
          (err, res) => (err ? reject(err) : resolve(res)),
        );
      },
    );
    assert.equal(unaryResp.results[0]?.grantId, "batch-ts-1");
    contractClient.close();

    // 2. Disallowed caller triggers PERMISSION_DENIED
    const gwGrantClient = new AitCreditGrantServiceClient(
      `127.0.0.1:${port}`,
      grpc.credentials.createInsecure(),
    );
    await assert.rejects(
      () =>
        new Promise((resolve, reject) => {
          gwGrantClient.issueAitCreditGrants(
            { batchId: "batch-denied", termId: "t1", boundaryIndex: 1, items: [] },
            new grpc.Metadata(),
            {
              interceptors: [
                createHmacClientInterceptor({
                  serviceId: "gateway-dp",
                  keyId: "k1",
                  secret: "secret-gateway-k1",
                  nowUnixSec: () => fixedUnixSec,
                }),
              ],
            },
            (err, res) => (err ? reject(err) : resolve(res)),
          );
        }),
      (err: grpc.ServiceError) => err.code === grpc.status.PERMISSION_DENIED,
    );
    gwGrantClient.close();

    // 3. Server-streaming call succeeds under wildcard ACL
    const providerClient = new ProviderSyncServiceClient(
      `127.0.0.1:${port}`,
      grpc.credentials.createInsecure(),
    );
    const versions: string[] = [];
    await new Promise<void>((resolve, reject) => {
      const stream = providerClient.watchSyncEvents(
        {
          gatewayNodeId: "gw-1",
          nodeRole: "dataplane",
          lastSeenCatalogVersion: "",
          lastSeenCredentialRevision: "",
        },
        new grpc.Metadata(),
        {
          interceptors: [
            createHmacClientInterceptor({
              serviceId: "gateway-dp",
              keyId: "k1",
              secret: "secret-gateway-k1",
              nowUnixSec: () => fixedUnixSec,
            }),
          ],
        },
      );
      stream.on("data", (chunk: { latestCatalogVersion: string }) => {
        if (chunk.latestCatalogVersion) versions.push(chunk.latestCatalogVersion);
      });
      stream.on("error", reject);
      stream.on("end", () => resolve());
    });
    assert.deepEqual(versions, ["cat-ts-v1"]);
    providerClient.close();
  } finally {
    await new Promise<void>((resolve) => server.tryShutdown(() => resolve()));
  }
});
