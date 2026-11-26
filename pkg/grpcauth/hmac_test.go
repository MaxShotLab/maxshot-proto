package grpcauth_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	commonv1 "github.com/MaxShotLab/maxshot-proto/gen/go/maxshot/common/v1"
	contractgrantv1 "github.com/MaxShotLab/maxshot-proto/gen/go/maxshot/contract_grant/v1"
	gatewayinternalv1 "github.com/MaxShotLab/maxshot-proto/gen/go/maxshot/gateway_internal/v1"
	providersyncv1 "github.com/MaxShotLab/maxshot-proto/gen/go/maxshot/provider_sync/v1"
	"github.com/MaxShotLab/maxshot-proto/pkg/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type goldenFile struct {
	Version string         `json:"version"`
	Vectors []goldenVector `json:"vectors"`
}

type goldenVector struct {
	Name                  string `json:"name"`
	FullMethod            string `json:"full_method"`
	ServiceID             string `json:"service_id"`
	KeyID                 string `json:"key_id"`
	Secret                string `json:"secret"`
	Timestamp             string `json:"timestamp"`
	Nonce                 string `json:"nonce"`
	BatchID               string `json:"batch_id"`
	TermID                string `json:"term_id"`
	BoundaryIndex         int32  `json:"boundary_index"`
	PayloadHex            string `json:"payload_hex"`
	ExpectedPayloadSHA256 string `json:"expected_payload_sha256"`
	ExpectedCanonical     string `json:"expected_canonical"`
	ExpectedSignature     string `json:"expected_signature"`
}

func TestGoldenVectorsAndProtobufWireParity(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/golden_vectors.json")
	if err != nil {
		t.Fatalf("read golden_vectors.json: %v", err)
	}
	var gf goldenFile
	if err := json.Unmarshal(raw, &gf); err != nil {
		t.Fatalf("unmarshal golden_vectors.json: %v", err)
	}

	for _, v := range gf.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			payload, err := hex.DecodeString(v.PayloadHex)
			if err != nil {
				t.Fatalf("decode payload_hex: %v", err)
			}
			// Verify Go deterministic protobuf wire bytes match golden payload_hex for unary vector
			if v.BatchID != "" {
				msg := &contractgrantv1.IssueAitCreditGrantsRequest{
					BatchId:       v.BatchID,
					TermId:        v.TermID,
					BoundaryIndex: v.BoundaryIndex,
				}
				marshaled, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
				if err != nil {
					t.Fatalf("marshal msg: %v", err)
				}
				if hex.EncodeToString(marshaled) != v.PayloadHex {
					t.Fatalf("wire bytes mismatch: got %x want %s", marshaled, v.PayloadHex)
				}
			}

			gotHash := grpcauth.PayloadHashHex(payload)
			if gotHash != v.ExpectedPayloadSHA256 {
				t.Fatalf("payload sha256 mismatch: got %s want %s", gotHash, v.ExpectedPayloadSHA256)
			}
			gotCanonical := grpcauth.BuildCanonicalString(v.FullMethod, v.ServiceID, v.KeyID, v.Timestamp, v.Nonce, gotHash)
			if gotCanonical != v.ExpectedCanonical {
				t.Fatalf("canonical mismatch:\ngot:  %q\nwant: %q", gotCanonical, v.ExpectedCanonical)
			}
			gotSig := grpcauth.SignCanonical(v.Secret, gotCanonical)
			if gotSig != v.ExpectedSignature {
				t.Fatalf("signature mismatch: got %s want %s", gotSig, v.ExpectedSignature)
			}
			if !grpcauth.VerifySignature(v.Secret, gotCanonical, v.ExpectedSignature) {
				t.Fatalf("VerifySignature returned false for valid golden signature")
			}
		})
	}
}

type mockGrantServer struct {
	contractgrantv1.UnimplementedAitCreditGrantServiceServer
	seenCaller string
}

func (s *mockGrantServer) IssueAitCreditGrants(ctx context.Context, req *contractgrantv1.IssueAitCreditGrantsRequest) (*contractgrantv1.IssueAitCreditGrantsResponse, error) {
	caller, _ := grpcauth.CallerServiceID(ctx)
	s.seenCaller = caller
	return &contractgrantv1.IssueAitCreditGrantsResponse{
		Results: []*contractgrantv1.AitGrantItemResult{
			{GrantId: req.BatchId, Status: contractgrantv1.GrantItemStatus_GRANT_ITEM_STATUS_CREDITED},
		},
	}, nil
}

type mockProviderSyncServer struct {
	providersyncv1.UnimplementedProviderSyncServiceServer
}

func (s *mockProviderSyncServer) WatchSyncEvents(req *providersyncv1.WatchSyncEventsRequest, stream grpc.ServerStreamingServer[providersyncv1.WatchSyncEventsResponse]) error {
	return stream.Send(&providersyncv1.WatchSyncEventsResponse{
		EventKind:            providersyncv1.SyncEventKind_SYNC_EVENT_KIND_CATALOG_PUBLISHED,
		LatestCatalogVersion: "cat-v1",
	})
}

func TestLoopbackUnaryStreamACLAndSkew(t *testing.T) {
	fixedNow := time.Unix(1795670000, 0).UTC()
	secrets := map[string]string{
		"contract-svc:k1": "secret-contract-k1",
		"gateway-dp:k1":   "secret-gateway-k1",
	}

	srvCfg := grpcauth.ServerConfig{
		ResolveSecret: func(serviceID, keyID string) (string, bool) {
			v, ok := secrets[serviceID+":"+keyID]
			return v, ok
		},
		AllowedCallers: map[string][]string{
			"/maxshot.contract_grant.v1.AitCreditGrantService/IssueAitCreditGrants": {"contract-svc"},
			"/maxshot.provider_sync.v1.ProviderSyncService/*":                       {"gateway-dp"},
		},
		Now: func() time.Time { return fixedNow },
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer(
		grpc.UnaryInterceptor(grpcauth.UnaryServerInterceptor(srvCfg)),
		grpc.StreamInterceptor(grpcauth.StreamServerInterceptor(srvCfg)),
	)
	grantImpl := &mockGrantServer{}
	contractgrantv1.RegisterAitCreditGrantServiceServer(grpcSrv, grantImpl)
	providersyncv1.RegisterProviderSyncServiceServer(grpcSrv, &mockProviderSyncServer{})
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	// 1. Authorized unary call from contract-svc succeeds
	contractConn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(grpcauth.UnaryClientInterceptor(grpcauth.ClientConfig{
			ServiceID: "contract-svc",
			KeyID:     "k1",
			Secret:    "secret-contract-k1",
			Now:       func() time.Time { return fixedNow },
		})),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer contractConn.Close()

	grantClient := contractgrantv1.NewAitCreditGrantServiceClient(contractConn)
	resp, err := grantClient.IssueAitCreditGrants(context.Background(), &contractgrantv1.IssueAitCreditGrantsRequest{
		BatchId: "b1", TermId: "t1", BoundaryIndex: 1,
	})
	if err != nil {
		t.Fatalf("expected authorized call to succeed, got: %v", err)
	}
	if len(resp.Results) != 1 || grantImpl.seenCaller != "contract-svc" {
		t.Fatalf("unexpected response or caller: %+v caller=%q", resp, grantImpl.seenCaller)
	}

	// 2. Unauthorized caller (gateway-dp calling IssueAitCreditGrants) is rejected by ACL with PermissionDenied
	gwConn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(grpcauth.UnaryClientInterceptor(grpcauth.ClientConfig{
			ServiceID: "gateway-dp",
			KeyID:     "k1",
			Secret:    "secret-gateway-k1",
			Now:       func() time.Time { return fixedNow },
		})),
		grpc.WithStreamInterceptor(grpcauth.StreamClientInterceptor(grpcauth.ClientConfig{
			ServiceID: "gateway-dp",
			KeyID:     "k1",
			Secret:    "secret-gateway-k1",
			Now:       func() time.Time { return fixedNow },
		})),
	)
	if err != nil {
		t.Fatalf("dial gw: %v", err)
	}
	defer gwConn.Close()

	_, err = contractgrantv1.NewAitCreditGrantServiceClient(gwConn).IssueAitCreditGrants(
		context.Background(),
		&contractgrantv1.IssueAitCreditGrantsRequest{BatchId: "b2"},
	)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for gateway-dp on IssueAitCreditGrants, got: %v", err)
	}

	// 3. Server-streaming call (gateway-dp calling WatchSyncEvents) succeeds via wildcard ACL
	stream, err := providersyncv1.NewProviderSyncServiceClient(gwConn).WatchSyncEvents(
		context.Background(),
		&providersyncv1.WatchSyncEventsRequest{GatewayNodeId: "node-1", NodeRole: "dataplane"},
	)
	if err != nil {
		t.Fatalf("watch stream start: %v", err)
	}
	ev, err := stream.Recv()
	if err != nil || ev.GetLatestCatalogVersion() != "cat-v1" {
		t.Fatalf("watch stream recv failed: ev=%+v err=%v", ev, err)
	}

	// 4. Expired timestamp is rejected with Unauthenticated
	staleConn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(grpcauth.UnaryClientInterceptor(grpcauth.ClientConfig{
			ServiceID: "contract-svc",
			KeyID:     "k1",
			Secret:    "secret-contract-k1",
			Now:       func() time.Time { return fixedNow.Add(-10 * time.Minute) },
		})),
	)
	if err != nil {
		t.Fatalf("dial stale: %v", err)
	}
	defer staleConn.Close()

	_, err = contractgrantv1.NewAitCreditGrantServiceClient(staleConn).IssueAitCreditGrants(
		context.Background(),
		&contractgrantv1.IssueAitCreditGrantsRequest{BatchId: "b3"},
	)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated for stale timestamp, got: %v", err)
	}
}

func TestExtendedContractsRoundTrip(t *testing.T) {
	scope := &gatewayinternalv1.ResolvePrincipalScopeResponse{
		Authenticated:                  true,
		MaxConcurrentStreams:           4,
		RpmLimit:                       120,
		TpmLimit:                       200000,
		SearchPolicy:                   commonv1.SearchPolicy_SEARCH_POLICY_PREMIUM_ONLY,
		MediaSharesSubscriptionWindows: true,
		AitCoverageThrottledModelIds:   []string{"model-heavy-1"},
	}
	b, err := proto.Marshal(scope)
	if err != nil || len(b) == 0 {
		t.Fatalf("marshal ResolvePrincipalScopeResponse: %v", err)
	}

	offering := &providersyncv1.OfferingRecord{
		OfferingId:         "off-1",
		CustomerChargeMode: commonv1.CustomerChargeMode_CUSTOMER_CHARGE_MODE_PROVIDER_ACTUAL_COST,
		HoldBufferBps:      11500,
		MarkupMultiplierDec: "1.000000",
		MandatoryReasoningPolicy: &providersyncv1.MandatoryReasoningPolicy{
			Mandatory:        true,
			SupportedEfforts: []string{"low", "medium", "high"},
			DefaultEffort:    "medium",
		},
		CustomerRateCard: &providersyncv1.RateCard{
			RateCardVersion:     "rc-v1",
			MinimumChargeUsdDec: "0.010000",
			MediaPricingFormula: &providersyncv1.MediaPricingFormula{
				FormulaKind:       providersyncv1.MediaPricingFormulaKind_MEDIA_PRICING_FORMULA_KIND_VIDEO_TOKENS,
				VideoTokenFps:     24,
				VideoTokenDivisor: 1024,
			},
		},
	}
	if _, err := proto.Marshal(offering); err != nil {
		t.Fatalf("marshal OfferingRecord: %v", err)
	}

	grantReq := &contractgrantv1.IssueAitCreditGrantsRequest{
		BatchId:       "b-summary",
		TermId:        "t1",
		BoundaryIndex: 2,
		BoundarySummary: &contractgrantv1.BoundaryBatchSummary{
			BatchSequence:               1,
			TotalBatches:                1,
			BoundaryTotalGrantCredits:   500000,
			OnchainTotalStakedAitWeiDec: "1000000000000000000000",
			FinalizedBlockNumber:        22000000,
		},
	}
	if _, err := proto.Marshal(grantReq); err != nil {
		t.Fatalf("marshal IssueAitCreditGrantsRequest: %v", err)
	}
}
