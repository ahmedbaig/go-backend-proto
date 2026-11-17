package grpcx

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ahmedbaig/go-backend-http"
	"github.com/ahmedbaig/go-backend-logging"
	"github.com/ahmedbaig/go-backend-types"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

const (
	MDRequestID     = "x-request-id"
	MDCorrelationID = "x-correlation-id"
	MDUser          = "x-user"
	MDAuthorization = "authorization"
)

type ServerOptions struct {
	Name          string
	Logger        *slog.Logger
	ServiceTokens []string
	TLS           *tls.Config
	Reflection    bool
	Extra         []grpc.ServerOption
}

type Server struct {
	*grpc.Server
	Health *health.Server
}

func NewServer(o ServerOptions) *Server {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	chain := []grpc.UnaryServerInterceptor{
		unaryIDs(log),
		unaryRecover(),
		unaryAuth(o.ServiceTokens),
		unaryAccessLog(),
	}
	streams := []grpc.StreamServerInterceptor{
		streamIDs(log),
		streamRecover(),
		streamAuth(o.ServiceTokens),
	}
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(chain...),
		grpc.ChainStreamInterceptor(streams...),
	}
	if o.TLS != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(o.TLS)))
	}
	opts = append(opts, o.Extra...)
	s := grpc.NewServer(opts...)
	h := health.NewServer()
	healthpb.RegisterHealthServer(s, h)
	if o.Reflection {
		reflection.Register(s)
	}
	return &Server{Server: s, Health: h}
}

func (s *Server) MarkServing(services ...string) {
	s.Health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	for _, n := range services {
		s.Health.SetServingStatus(n, healthpb.HealthCheckResponse_SERVING)
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.Health.Shutdown()
	done := make(chan struct{})
	go func() {
		s.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.Stop()
		return ctx.Err()
	}
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9._:\-]{1,128}$`)

func newID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func incoming(ctx context.Context, log *slog.Logger) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	rid := first(md, MDRequestID)
	if !safeID.MatchString(rid) {
		rid = newID()
	}
	cid := first(md, MDCorrelationID)
	if !safeID.MatchString(cid) {
		cid = rid
	}
	ctx = httpx.WithIDs(logging.WithScope(ctx), rid, cid)
	ctx = logging.WithAttrs(ctx, slog.String("requestId", rid), slog.String("correlationId", cid))
	ctx = httpx.WithLogger(ctx, log)
	if raw := first(md, MDUser); raw != "" {
		u, err := types.ParseUser(raw)
		if err != nil {
			return ctx, status.Error(codes.Unauthenticated, "the user metadata is invalid")
		}
		ctx = types.WithUser(ctx, u)
		ctx = logging.WithAttrs(ctx, slog.Int64("userId", u.ID), slog.String("email", u.Email), slog.Any("roles", u.Roles))
	}
	_ = grpc.SetHeader(ctx, metadata.Pairs(MDRequestID, rid, MDCorrelationID, cid))
	return ctx, nil
}

func unaryIDs(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		ctx, err := incoming(ctx, log)
		if err != nil {
			return nil, err
		}
		res, err := next(ctx, req)
		return res, ToStatus(err)
	}
}

func streamIDs(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		ctx, err := incoming(ss.Context(), log)
		if err != nil {
			return err
		}
		return ToStatus(next(srv, &wrappedStream{ServerStream: ss, ctx: ctx}))
	}
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

func panicErr(ctx context.Context, method string, p any) error {
	httpx.LoggerFrom(ctx).ErrorContext(ctx, "grpc panic", slog.String("method", method), slog.Any("panic", p), slog.String("stack", string(debug.Stack())))
	return ToStatus(httpx.E(httpx.CodeInternal).Wrap(fmt.Errorf("panic: %v", p)))
}

func unaryRecover() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (res any, err error) {
		defer func() {
			if p := recover(); p != nil {
				err = panicErr(ctx, info.FullMethod, p)
			}
		}()
		return next(ctx, req)
	}
}

func streamRecover() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = panicErr(ss.Context(), info.FullMethod, p)
			}
		}()
		return next(srv, ss)
	}
}

func isHealth(method string) bool {
	return strings.HasPrefix(method, "/grpc.health.v1.Health/") || strings.HasPrefix(method, "/grpc.reflection.")
}

func checkToken(ctx context.Context, tokens []string) error {
	md, _ := metadata.FromIncomingContext(ctx)
	got, ok := strings.CutPrefix(first(md, MDAuthorization), "Bearer ")
	if !ok || got == "" {
		return status.Error(codes.Unauthenticated, "a service token is required")
	}
	for _, t := range tokens {
		if subtle.ConstantTimeCompare([]byte(got), []byte(t)) == 1 {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "the service token is not valid")
}

func unaryAuth(tokens []string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if len(tokens) > 0 && !isHealth(info.FullMethod) {
			if err := checkToken(ctx, tokens); err != nil {
				return nil, err
			}
		}
		return next(ctx, req)
	}
}

func streamAuth(tokens []string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		if len(tokens) > 0 && !isHealth(info.FullMethod) {
			if err := checkToken(ss.Context(), tokens); err != nil {
				return err
			}
		}
		return next(srv, ss)
	}
}

func unaryAccessLog() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		start := time.Now()
		res, err := next(ctx, req)
		code := status.Code(ToStatus(err))
		level := slog.LevelInfo
		switch {
		case isHealth(info.FullMethod):
			level = slog.LevelDebug
		case code == codes.Internal || code == codes.Unknown || code == codes.DataLoss:
			level = slog.LevelError
		}
		httpx.LoggerFrom(ctx).Log(ctx, level, "grpc", slog.String("method", info.FullMethod), slog.String("code", code.String()), slog.Duration("took", time.Since(start)))
		return res, err
	}
}
