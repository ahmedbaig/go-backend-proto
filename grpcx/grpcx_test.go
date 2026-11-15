package grpcx_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"

	"github.com/ahmedbaig/go-backend-http"
	dbv1 "github.com/ahmedbaig/go-backend-proto/gen/go/ahmedbaig/dbhandler/v1"
	"github.com/ahmedbaig/go-backend-proto/grpcx"
	"github.com/ahmedbaig/go-backend-types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type fake struct {
	dbv1.UnimplementedDBHandlerServiceServer
}

func (fake) Query(ctx context.Context, q *dbv1.QueryRequest) (*dbv1.QueryResponse, error) {
	switch q.GetMethod() {
	case "missing":
		return nil, httpx.E(httpx.CodeNotFound).WithMessage("no such post")
	case "invalid":
		return nil, httpx.E(httpx.CodeValidation).WithFields(httpx.FieldError{Field: "title", Rule: "min", Message: "too short"})
	case "panic":
		panic("boom")
	case "plain":
		return nil, errors.New("disk on fire")
	}
	u, _ := types.UserFrom(ctx)
	out, _ := json.Marshal(map[string]any{
		"requestId":     httpx.RequestID(ctx),
		"correlationId": httpx.CorrelationID(ctx),
		"user":          u.ID,
	})
	return &dbv1.QueryResponse{DataJson: out}, nil
}

func start(t *testing.T, tokens []string) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer(grpcx.ServerOptions{
		Name:          "test",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		ServiceTokens: tokens,
	})
	dbv1.RegisterDBHandlerServiceServer(srv, fake{})
	srv.MarkServing(dbv1.DBHandlerService_ServiceDesc.ServiceName)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpcx.Dial("passthrough:///bufnet", grpcx.ClientOptions{
		ServiceToken: "secret",
		Extra: []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestIDsAndUserPropagate(t *testing.T) {
	c := dbv1.NewDBHandlerServiceClient(start(t, []string{"secret"}))
	ctx := httpx.WithIDs(context.Background(), "req-1", "corr-1")
	ctx = types.WithUser(ctx, types.User{ID: 42})
	var hdr metadata.MD
	res, err := c.Query(ctx, &dbv1.QueryRequest{Method: "echo"}, grpc.Header(&hdr))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		RequestID     string `json:"requestId"`
		CorrelationID string `json:"correlationId"`
		User          int64  `json:"user"`
	}
	if err := json.Unmarshal(res.GetDataJson(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "req-1" || got.CorrelationID != "corr-1" || got.User != 42 {
		t.Fatalf("got %+v", got)
	}
	if v := hdr.Get(grpcx.MDCorrelationID); len(v) != 1 || v[0] != "corr-1" {
		t.Fatalf("header %v", hdr)
	}
}

func TestMissingIDsAreGenerated(t *testing.T) {
	c := dbv1.NewDBHandlerServiceClient(start(t, nil))
	res, err := c.Query(context.Background(), &dbv1.QueryRequest{Method: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(res.GetDataJson(), &got)
	if got["requestId"] == "" || got["requestId"] != got["correlationId"] {
		t.Fatalf("got %v", got)
	}
}

func TestServiceTokenRequired(t *testing.T) {
	conn := start(t, []string{"other"})
	_, err := dbv1.NewDBHandlerServiceClient(conn).Query(context.Background(), &dbv1.QueryRequest{Method: "echo"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v", err)
	}
	if err := grpcx.HealthCheck(conn, "")(context.Background()); err != nil {
		t.Fatalf("health should skip auth: %v", err)
	}
}

func TestErrorsRoundTrip(t *testing.T) {
	c := dbv1.NewDBHandlerServiceClient(start(t, nil))
	cases := []struct {
		method string
		code   httpx.Code
		status int
		grpc   codes.Code
	}{
		{"missing", httpx.CodeNotFound, http.StatusNotFound, codes.NotFound},
		{"invalid", httpx.CodeValidation, http.StatusBadRequest, codes.InvalidArgument},
		{"panic", httpx.CodeInternal, http.StatusInternalServerError, codes.Internal},
		{"plain", httpx.CodeInternal, http.StatusInternalServerError, codes.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			_, err := c.Query(context.Background(), &dbv1.QueryRequest{Method: tc.method})
			if status.Code(err) != tc.grpc {
				t.Fatalf("grpc code %v", status.Code(err))
			}
			e := grpcx.FromStatus(err)
			if e.Code != tc.code || e.Status != tc.status {
				t.Fatalf("got %s %d", e.Code, e.Status)
			}
			if tc.method == "missing" && e.Message != "no such post" {
				t.Fatalf("message %q", e.Message)
			}
			if tc.method == "invalid" && (len(e.Fields) != 1 || e.Fields[0].Field != "title") {
				t.Fatalf("fields %+v", e.Fields)
			}
			if tc.method == "plain" && e.Message == "disk on fire" {
				t.Fatal("internal error text leaked")
			}
		})
	}
}

func TestUnavailableMapsTo503(t *testing.T) {
	e := grpcx.FromStatus(status.Error(codes.Unavailable, "connection refused"))
	if e.Status != http.StatusServiceUnavailable {
		t.Fatalf("got %d", e.Status)
	}
}

func TestInvalidUserRejected(t *testing.T) {
	c := dbv1.NewDBHandlerServiceClient(start(t, nil))
	ctx := metadata.AppendToOutgoingContext(context.Background(), grpcx.MDUser, "not json")
	_, err := c.Query(ctx, &dbv1.QueryRequest{Method: "echo"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v", err)
	}
}
