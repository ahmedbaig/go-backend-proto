package grpcx

import (
	"context"
	"crypto/tls"

	"github.com/ahmedbaig/go-backend-http"
	"github.com/ahmedbaig/go-backend-types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

type ClientOptions struct {
	ServiceToken string
	TLS          *tls.Config
	Extra        []grpc.DialOption
}

func Dial(target string, o ClientOptions) (*grpc.ClientConn, error) {
	creds := insecure.NewCredentials()
	if o.TLS != nil {
		creds = credentials.NewTLS(o.TLS)
	}
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithChainUnaryInterceptor(UnaryClient(o.ServiceToken)),
		grpc.WithChainStreamInterceptor(StreamClient(o.ServiceToken)),
	}, o.Extra...)
	return grpc.NewClient(target, opts...)
}

func Outgoing(ctx context.Context, token string) context.Context {
	var pairs []string
	rid, cid := httpx.RequestID(ctx), httpx.CorrelationID(ctx)
	if rid == "" {
		rid = newID()
	}
	if cid == "" {
		cid = rid
	}
	pairs = append(pairs, MDRequestID, rid, MDCorrelationID, cid)
	if u, ok := types.UserFrom(ctx); ok {
		pairs = append(pairs, MDUser, u.Header())
	}
	if token != "" {
		pairs = append(pairs, MDAuthorization, "Bearer "+token)
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...)
}

func UnaryClient(token string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(Outgoing(ctx, token), method, req, reply, cc, opts...)
	}
}

func StreamClient(token string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(Outgoing(ctx, token), desc, cc, method, opts...)
	}
}

func HealthCheck(conn grpc.ClientConnInterface, service string) func(ctx context.Context) error {
	c := healthpb.NewHealthClient(conn)
	return func(ctx context.Context) error {
		res, err := c.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
		if err != nil {
			return err
		}
		if res.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			return &notServing{status: res.GetStatus().String()}
		}
		return nil
	}
}

type notServing struct{ status string }

func (n *notServing) Error() string { return "grpc health: " + n.status }
