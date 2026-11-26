package grpcauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	ProtocolVersion   = "v1"
	HeaderServiceID   = "x-ms-service-id"
	HeaderKeyID       = "x-ms-key-id"
	HeaderTimestamp   = "x-ms-timestamp"
	HeaderNonce       = "x-ms-nonce"
	HeaderSignature   = "x-ms-signature"
	DefaultMaxSkew    = 5 * time.Minute
	DefaultNonceTTL   = 10 * time.Minute
	EmptyPayloadHash  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

type callerContextKey struct{}

// CallerServiceID extracts the authenticated caller service_id from server context.
func CallerServiceID(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(callerContextKey{}).(string)
	return v, ok
}

// PayloadHashHex returns the lowercase hex SHA-256 of raw payload bytes.
func PayloadHashHex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// BuildCanonicalString constructs the exact byte-for-byte canonical string shared with TypeScript.
func BuildCanonicalString(fullMethod, serviceID, keyID, timestamp, nonce, payloadHashHex string) string {
	return strings.Join([]string{
		ProtocolVersion,
		fullMethod,
		serviceID,
		keyID,
		timestamp,
		nonce,
		payloadHashHex,
	}, "\n")
}

// SignCanonical computes the lowercase hex HMAC-SHA256 signature for a canonical string.
func SignCanonical(secret, canonical string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature performs constant-time verification against a secret.
func VerifySignature(secret, canonical, signatureHex string) bool {
	expectedBytes, err := hex.DecodeString(SignCanonical(secret, canonical))
	if err != nil {
		return false
	}
	providedBytes, err := hex.DecodeString(signatureHex)
	if err != nil {
		return false
	}
	return hmac.Equal(expectedBytes, providedBytes)
}

func marshalDeterministic(msg any) ([]byte, error) {
	if msg == nil {
		return nil, nil
	}
	pm, ok := msg.(proto.Message)
	if !ok {
		return nil, errors.New("grpcauth: message does not implement proto.Message")
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(pm)
}

func generateNonceHex() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ClientConfig configures outbound HMAC signing for Go gRPC clients.
type ClientConfig struct {
	ServiceID string
	KeyID     string
	Secret    string
	Now       func() time.Time
}

// UnaryClientInterceptor signs outbound unary gRPC requests (including deterministic payload SHA-256).
func UnaryClientInterceptor(cfg ClientConfig) grpc.UnaryClientInterceptor {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		raw, err := marshalDeterministic(req)
		if err != nil {
			return status.Errorf(codes.Internal, "grpcauth marshal: %v", err)
		}
		nonce, err := generateNonceHex()
		if err != nil {
			return status.Errorf(codes.Internal, "grpcauth nonce: %v", err)
		}
		ts := strconv.FormatInt(nowFn().UTC().Unix(), 10)
		canonical := BuildCanonicalString(method, cfg.ServiceID, cfg.KeyID, ts, nonce, PayloadHashHex(raw))
		sig := SignCanonical(cfg.Secret, canonical)

		ctx = metadata.AppendToOutgoingContext(ctx,
			HeaderServiceID, cfg.ServiceID,
			HeaderKeyID, cfg.KeyID,
			HeaderTimestamp, ts,
			HeaderNonce, nonce,
			HeaderSignature, sig,
		)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// StreamClientInterceptor signs the stream-opening handshake over TLS.
func StreamClientInterceptor(cfg ClientConfig) grpc.StreamClientInterceptor {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		nonce, err := generateNonceHex()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "grpcauth nonce: %v", err)
		}
		ts := strconv.FormatInt(nowFn().UTC().Unix(), 10)
		canonical := BuildCanonicalString(method, cfg.ServiceID, cfg.KeyID, ts, nonce, EmptyPayloadHash)
		sig := SignCanonical(cfg.Secret, canonical)

		ctx = metadata.AppendToOutgoingContext(ctx,
			HeaderServiceID, cfg.ServiceID,
			HeaderKeyID, cfg.KeyID,
			HeaderTimestamp, ts,
			HeaderNonce, nonce,
			HeaderSignature, sig,
		)
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// ReplayGuard prevents nonce reuse within the allowed clock skew window.
type ReplayGuard interface {
	SeenOrRecord(serviceID, nonce string, ttl time.Duration) bool
}

// MemoryReplayGuard is a concurrency-safe in-memory ReplayGuard with lazy expiry cleanup.
type MemoryReplayGuard struct {
	mu      sync.Mutex
	entries map[string]time.Time
	nowFn   func() time.Time
}

func NewMemoryReplayGuard(nowFn func() time.Time) *MemoryReplayGuard {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &MemoryReplayGuard{
		entries: make(map[string]time.Time),
		nowFn:   nowFn,
	}
}

func (g *MemoryReplayGuard) SeenOrRecord(serviceID, nonce string, ttl time.Duration) bool {
	now := g.nowFn()
	composite := serviceID + ":" + nonce
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(g.entries) > 4096 {
		for k, exp := range g.entries {
			if !exp.After(now) {
				delete(g.entries, k)
			}
		}
	}
	if exp, exists := g.entries[composite]; exists && exp.After(now) {
		return true
	}
	g.entries[composite] = now.Add(ttl)
	return false
}

// SecretResolver looks up the shared secret for a (serviceID, keyID) pair (supports active + previous key rotation).
type SecretResolver func(serviceID, keyID string) (string, bool)

// ServerConfig configures inbound HMAC verification and per-method caller ACLs.
type ServerConfig struct {
	ResolveSecret SecretResolver
	// AllowedCallers maps full gRPC method (or service prefix "/pkg.Svc/*") -> allowed caller service_ids.
	// If non-nil, unlisted methods or callers are rejected with codes.PermissionDenied.
	AllowedCallers map[string][]string
	MaxSkew        time.Duration
	NonceTTL       time.Duration
	ReplayGuard    ReplayGuard
	Now            func() time.Time
}

func (c *ServerConfig) verifyContext(ctx context.Context, fullMethod, payloadHash string) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "missing metadata")
	}
	svcID := firstMeta(md, HeaderServiceID)
	keyID := firstMeta(md, HeaderKeyID)
	tsStr := firstMeta(md, HeaderTimestamp)
	nonce := firstMeta(md, HeaderNonce)
	sig := firstMeta(md, HeaderSignature)
	if svcID == "" || keyID == "" || tsStr == "" || nonce == "" || sig == "" {
		return ctx, status.Error(codes.Unauthenticated, "missing hmac auth headers")
	}

	if c.AllowedCallers != nil && !isCallerAllowed(c.AllowedCallers, fullMethod, svcID) {
		return ctx, status.Errorf(codes.PermissionDenied, "caller %q not permitted for %s", svcID, fullMethod)
	}

	tsSec, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return ctx, status.Error(codes.Unauthenticated, "invalid timestamp")
	}
	nowFn := c.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	maxSkew := c.MaxSkew
	if maxSkew <= 0 {
		maxSkew = DefaultMaxSkew
	}
	diff := nowFn().UTC().Sub(time.Unix(tsSec, 0).UTC())
	if diff < -maxSkew || diff > maxSkew {
		return ctx, status.Error(codes.Unauthenticated, "timestamp outside allowed skew window")
	}

	secret, found := c.ResolveSecret(svcID, keyID)
	if !found || secret == "" {
		return ctx, status.Error(codes.Unauthenticated, "unknown service_id or key_id")
	}
	canonical := BuildCanonicalString(fullMethod, svcID, keyID, tsStr, nonce, payloadHash)
	if !VerifySignature(secret, canonical, sig) {
		return ctx, status.Error(codes.Unauthenticated, "invalid hmac signature")
	}

	if c.ReplayGuard != nil {
		ttl := c.NonceTTL
		if ttl <= 0 {
			ttl = DefaultNonceTTL
		}
		if c.ReplayGuard.SeenOrRecord(svcID, nonce, ttl) {
			return ctx, status.Error(codes.Unauthenticated, "replayed nonce rejected")
		}
	}

	return context.WithValue(ctx, callerContextKey{}, svcID), nil
}

// UnaryServerInterceptor verifies inbound unary requests (ACL + skew + HMAC + payload SHA-256 + replay).
func UnaryServerInterceptor(cfg ServerConfig) grpc.UnaryServerInterceptor {
	if cfg.ReplayGuard == nil {
		cfg.ReplayGuard = NewMemoryReplayGuard(cfg.Now)
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		raw, err := marshalDeterministic(req)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "grpcauth marshal: %v", err)
		}
		authedCtx, err := cfg.verifyContext(ctx, info.FullMethod, PayloadHashHex(raw))
		if err != nil {
			return nil, err
		}
		return handler(authedCtx, req)
	}
}

// StreamServerInterceptor verifies inbound stream handshakes (ACL + skew + HMAC + replay).
func StreamServerInterceptor(cfg ServerConfig) grpc.StreamServerInterceptor {
	if cfg.ReplayGuard == nil {
		cfg.ReplayGuard = NewMemoryReplayGuard(cfg.Now)
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		authedCtx, err := cfg.verifyContext(ss.Context(), info.FullMethod, EmptyPayloadHash)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: authedCtx})
	}
}

type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

func firstMeta(md metadata.MD, key string) string {
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func isCallerAllowed(acl map[string][]string, fullMethod, callerID string) bool {
	if allowed, ok := acl[fullMethod]; ok {
		for _, id := range allowed {
			if id == callerID {
				return true
			}
		}
	}
	// Check service wildcard "/pkg.Service/*"
	if idx := strings.LastIndex(fullMethod, "/"); idx > 0 {
		wildcard := fmt.Sprintf("%s/*", fullMethod[:idx])
		if allowed, ok := acl[wildcard]; ok {
			for _, id := range allowed {
				if id == callerID {
					return true
				}
			}
		}
	}
	return false
}
