import crypto from "node:crypto";
import * as grpc from "@grpc/grpc-js";
import {
  AitCreditGrantServiceService,
  WalletEligibilityServiceService,
} from "../../gen/ts/maxshot/contract_grant/v1/contract_grant.js";
import { BillingExecutionServiceService } from "../../gen/ts/maxshot/gateway_internal/v1/execution.js";
import { ProviderSyncServiceService } from "../../gen/ts/maxshot/provider_sync/v1/provider_sync.js";

export const PROTOCOL_VERSION = "v1";
export const HEADER_SERVICE_ID = "x-ms-service-id";
export const HEADER_KEY_ID = "x-ms-key-id";
export const HEADER_TIMESTAMP = "x-ms-timestamp";
export const HEADER_NONCE = "x-ms-nonce";
export const HEADER_SIGNATURE = "x-ms-signature";
export const DEFAULT_MAX_SKEW_SEC = 300;
export const DEFAULT_NONCE_TTL_MS = 600_000;
export const EMPTY_PAYLOAD_HASH =
  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";

export function payloadHashHex(payload: Uint8Array | Buffer): string {
  return crypto.createHash("sha256").update(payload).digest("hex");
}

export function buildCanonicalString(
  fullMethod: string,
  serviceId: string,
  keyId: string,
  timestamp: string,
  nonce: string,
  payloadSha256Hex: string,
): string {
  return [
    PROTOCOL_VERSION,
    fullMethod,
    serviceId,
    keyId,
    timestamp,
    nonce,
    payloadSha256Hex,
  ].join("\n");
}

export function signCanonical(secret: string, canonical: string): string {
  return crypto.createHmac("sha256", secret).update(canonical, "utf8").digest("hex");
}

export function verifySignature(
  secret: string,
  canonical: string,
  signatureHex: string,
): boolean {
  try {
    const expected = Buffer.from(signCanonical(secret, canonical), "hex");
    const provided = Buffer.from(signatureHex, "hex");
    if (expected.length === 0 || expected.length !== provided.length) {
      return false;
    }
    return crypto.timingSafeEqual(expected, provided);
  } catch {
    return false;
  }
}

export interface ClientHmacConfig {
  serviceId: string;
  keyId: string;
  secret: string;
  nowUnixSec?: () => number;
}

/**
 * Creates a @grpc/grpc-js Client Interceptor that signs both Unary (including serialized payload SHA-256)
 * and Streaming (handshake) calls identically to Go's grpcauth.UnaryClientInterceptor / StreamClientInterceptor.
 */
export function createHmacClientInterceptor(config: ClientHmacConfig): grpc.Interceptor {
  const nowSec = config.nowUnixSec ?? (() => Math.floor(Date.now() / 1000));

  return (options, nextCall) => {
    const methodDef = options.method_definition;
    const fullMethod = methodDef.path;
    const isUnary = !methodDef.requestStream && !methodDef.responseStream;

    return new grpc.InterceptingCall(nextCall(options), {
      start(metadata, listener, next) {
        if (!isUnary) {
          const ts = String(nowSec());
          const nonce = crypto.randomBytes(16).toString("hex");
          const canonical = buildCanonicalString(
            fullMethod,
            config.serviceId,
            config.keyId,
            ts,
            nonce,
            EMPTY_PAYLOAD_HASH,
          );
          metadata.set(HEADER_SERVICE_ID, config.serviceId);
          metadata.set(HEADER_KEY_ID, config.keyId);
          metadata.set(HEADER_TIMESTAMP, ts);
          metadata.set(HEADER_NONCE, nonce);
          metadata.set(HEADER_SIGNATURE, signCanonical(config.secret, canonical));
          next(metadata, listener);
          return;
        }
        // For unary calls, defer metadata emission until sendMessage supplies the request object
        // so we can compute the exact serialized protobuf SHA-256 before starting wire transmission.
        (this as unknown as { _deferredStart?: { metadata: grpc.Metadata; listener: grpc.Listener; next: (md: grpc.Metadata, l: grpc.Listener) => void } })._deferredStart = {
          metadata,
          listener,
          next,
        };
      },
      sendMessage(message, next) {
        const deferred = (this as unknown as { _deferredStart?: { metadata: grpc.Metadata; listener: grpc.Listener; next: (md: grpc.Metadata, l: grpc.Listener) => void } })._deferredStart;
        if (deferred) {
          (this as unknown as { _deferredStart?: undefined })._deferredStart = undefined;
          const serialized: Buffer = methodDef.requestSerialize(message);
          const ts = String(nowSec());
          const nonce = crypto.randomBytes(16).toString("hex");
          const canonical = buildCanonicalString(
            fullMethod,
            config.serviceId,
            config.keyId,
            ts,
            nonce,
            payloadHashHex(serialized),
          );
          deferred.metadata.set(HEADER_SERVICE_ID, config.serviceId);
          deferred.metadata.set(HEADER_KEY_ID, config.keyId);
          deferred.metadata.set(HEADER_TIMESTAMP, ts);
          deferred.metadata.set(HEADER_NONCE, nonce);
          deferred.metadata.set(HEADER_SIGNATURE, signCanonical(config.secret, canonical));
          deferred.next(deferred.metadata, deferred.listener);
        }
        next(message);
      },
    });
  };
}

export interface ReplayGuard {
  seenOrRecord(serviceId: string, nonce: string, ttlMs: number): boolean;
}

export class MemoryReplayGuard implements ReplayGuard {
  private readonly entries = new Map<string, number>();
  constructor(private readonly nowMs: () => number = () => Date.now()) {}

  seenOrRecord(serviceId: string, nonce: string, ttlMs: number): boolean {
    const now = this.nowMs();
    if (this.entries.size > 4096) {
      for (const [k, exp] of this.entries.entries()) {
        if (exp <= now) this.entries.delete(k);
      }
    }
    const key = `${serviceId}:${nonce}`;
    const existing = this.entries.get(key);
    if (existing !== undefined && existing > now) {
      return true;
    }
    this.entries.set(key, now + ttlMs);
    return false;
  }
}

export interface ServerHmacConfig {
  resolveSecret: (serviceId: string, keyId: string) => string | undefined;
  allowedCallers?: Record<string, readonly string[]>;
  maxSkewSec?: number;
  nonceTtlMs?: number;
  replayGuard?: ReplayGuard;
  nowUnixSec?: () => number;
  extraServices?: readonly grpc.ServiceDefinition[];
}

type RequestSerializer = (value: unknown) => Buffer;

function collectSerializers(
  defs: readonly Record<string, { path: string; requestSerialize: (v: never) => Buffer }>[],
): Map<string, RequestSerializer> {
  const map = new Map<string, RequestSerializer>();
  for (const svc of defs) {
    for (const method of Object.values(svc)) {
      if (method && typeof method.path === "string" && typeof method.requestSerialize === "function") {
        map.set(method.path, method.requestSerialize as unknown as RequestSerializer);
      }
    }
  }
  return map;
}

const BUILTIN_SERIALIZERS = collectSerializers([
  ProviderSyncServiceService,
  WalletEligibilityServiceService,
  AitCreditGrantServiceService,
  BillingExecutionServiceService,
]);

function isCallerAllowed(
  acl: Record<string, readonly string[]>,
  fullMethod: string,
  callerId: string,
): boolean {
  const exact = acl[fullMethod];
  if (exact && exact.includes(callerId)) return true;
  const slashIdx = fullMethod.lastIndexOf("/");
  if (slashIdx > 0) {
    const wildcard = `${fullMethod.slice(0, slashIdx)}/*`;
    const wild = acl[wildcard];
    if (wild && wild.includes(callerId)) return true;
  }
  return false;
}

function firstStringMeta(md: grpc.Metadata, key: string): string {
  const vals = md.get(key);
  if (!vals || vals.length === 0) return "";
  const v = vals[0];
  return typeof v === "string" ? v : v.toString("utf8");
}

/**
 * Verifies inbound gRPC metadata + payload hash on TypeScript @grpc/grpc-js servers.
 * Returns the authenticated caller serviceId or throws a grpc.ServiceError.
 */
export function verifyIncomingCall(
  config: ServerHmacConfig,
  fullMethod: string,
  metadata: grpc.Metadata,
  serializedPayload?: Uint8Array | Buffer,
): string {
  const nowSec = config.nowUnixSec ?? (() => Math.floor(Date.now() / 1000));
  const maxSkew = config.maxSkewSec ?? DEFAULT_MAX_SKEW_SEC;
  const nonceTtl = config.nonceTtlMs ?? DEFAULT_NONCE_TTL_MS;

  const serviceId = firstStringMeta(metadata, HEADER_SERVICE_ID);
  const keyId = firstStringMeta(metadata, HEADER_KEY_ID);
  const tsStr = firstStringMeta(metadata, HEADER_TIMESTAMP);
  const nonce = firstStringMeta(metadata, HEADER_NONCE);
  const sig = firstStringMeta(metadata, HEADER_SIGNATURE);

  const unauth = (details: string): never => {
    const err = new Error(details) as grpc.ServiceError;
    err.code = grpc.status.UNAUTHENTICATED;
    err.details = details;
    throw err;
  };
  const denied = (details: string): never => {
    const err = new Error(details) as grpc.ServiceError;
    err.code = grpc.status.PERMISSION_DENIED;
    err.details = details;
    throw err;
  };

  if (!serviceId || !keyId || !tsStr || !nonce || !sig) {
    return unauth("missing hmac auth headers");
  }
  if (config.allowedCallers && !isCallerAllowed(config.allowedCallers, fullMethod, serviceId)) {
    return denied(`caller "${serviceId}" not permitted for ${fullMethod}`);
  }
  const tsNum = Number(tsStr);
  if (!Number.isInteger(tsNum) || Math.abs(nowSec() - tsNum) > maxSkew) {
    return unauth("timestamp outside allowed skew window");
  }
  const secret = config.resolveSecret(serviceId, keyId);
  if (!secret) {
    return unauth("unknown service_id or key_id");
  }
  const bodyHash = serializedPayload ? payloadHashHex(serializedPayload) : EMPTY_PAYLOAD_HASH;
  const canonical = buildCanonicalString(fullMethod, serviceId, keyId, tsStr, nonce, bodyHash);
  if (!verifySignature(secret, canonical, sig)) {
    return unauth("invalid hmac signature");
  }
  if (config.replayGuard && config.replayGuard.seenOrRecord(serviceId, nonce, nonceTtl)) {
    return unauth("replayed nonce rejected");
  }
  return serviceId;
}

/**
 * Creates a @grpc/grpc-js ServerInterceptor (available in @grpc/grpc-js >= 1.10)
 * that enforces HMAC + ACL + Skew + Nonce replay protection for all services on a TS gRPC Server.
 */
export function createHmacServerInterceptor(config: ServerHmacConfig): grpc.ServerInterceptor {
  const guard = config.replayGuard ?? new MemoryReplayGuard();
  const resolvedCfg: ServerHmacConfig = { ...config, replayGuard: guard };
  const serializers = new Map(BUILTIN_SERIALIZERS);
  if (config.extraServices) {
    for (const [k, v] of collectSerializers(
      config.extraServices as unknown as readonly Record<
        string,
        { path: string; requestSerialize: (v: never) => Buffer }
      >[],
    )) {
      serializers.set(k, v);
    }
  }

  return (methodDescriptor, call) => {
    const fullMethod = methodDescriptor.path;
    const isUnary = !methodDescriptor.requestStream && !methodDescriptor.responseStream;
    let incomingMeta: grpc.Metadata | undefined;

    return new grpc.ServerInterceptingCall(call, {
      start(next) {
        next({
          onReceiveMetadata(metadata, mdNext) {
            incomingMeta = metadata;
            if (!isUnary) {
              try {
                verifyIncomingCall(resolvedCfg, fullMethod, metadata);
              } catch (err) {
                const svcErr = err as grpc.ServiceError;
                call.sendStatus({
                  code: svcErr.code ?? grpc.status.UNAUTHENTICATED,
                  details: svcErr.details ?? svcErr.message,
                  metadata: new grpc.Metadata(),
                });
                return;
              }
            }
            mdNext(metadata);
          },
          onReceiveMessage(message, msgNext) {
            if (isUnary) {
              try {
                const serialize = serializers.get(fullMethod);
                if (!serialize) {
                  throw Object.assign(new Error(`missing serializer for ${fullMethod}`), {
                    code: grpc.status.INTERNAL,
                  });
                }
                const rawBytes = serialize(message);
                verifyIncomingCall(
                  resolvedCfg,
                  fullMethod,
                  incomingMeta ?? new grpc.Metadata(),
                  rawBytes,
                );
              } catch (err) {
                const svcErr = err as grpc.ServiceError;
                call.sendStatus({
                  code: svcErr.code ?? grpc.status.UNAUTHENTICATED,
                  details: svcErr.details ?? svcErr.message,
                  metadata: new grpc.Metadata(),
                });
                return;
              }
            }
            msgNext(message);
          },
        });
      },
    });
  };
}
