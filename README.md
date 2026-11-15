# go-backend-proto

gRPC contracts and the small runtime around them for the `go-backend-*` family. Protos live here, generated Go code is committed under `gen/go`, and `grpcx` makes every service speak gRPC the same way.

```sh
go get github.com/ahmedbaig/go-backend-proto
```

Request flow this is built for:

```
browser ──REST──▶ service (go-backend-template) ──gRPC──▶ go-backend-db-handler
                      │                                        │
                      └── X-Request-ID / X-Correlation-ID ─────┴── same IDs in every log line
```

## Contracts

| Package | What |
|---|---|
| `ahmedbaig.dbhandler.v1` | `DBHandlerService`: Query, Transaction, KeyValue (Redis), Documents (Mongo), Track and DailyActiveUsers (analytics), Capabilities |
| `ahmedbaig.events.v1` | `Event` envelope for Redis pub/sub and SSE: id, topic, type, data, correlation and request IDs, time |
| `ahmedbaig.common.v1` | `ErrorInfo`: the `httpx` error (code, message, actions, field errors, HTTP status) carried as a gRPC status detail |

Routing fields (database, service, method, store, op) are typed. Payloads are JSON bytes, because models are generated per database and the [go-backend-db](https://github.com/ahmedbaig/go-backend-db) types already own their shape and validation. Callers use `go-backend-db` either way; only the transport changes.

## grpcx

```go
srv := grpcx.NewServer(grpcx.ServerOptions{
	Name:          "db-handler",
	Logger:        log.Logger,
	ServiceTokens: cfg.ServiceTokens,
})
dbv1.RegisterDBHandlerServiceServer(srv, impl)
srv.MarkServing(dbv1.DBHandlerService_ServiceDesc.ServiceName)
go srv.Serve(lis)
defer srv.Shutdown(ctx)

conn, err := grpcx.Dial("db-handler:9090", grpcx.ClientOptions{ServiceToken: token})
client := dbv1.NewDBHandlerServiceClient(conn)
```

Server side, every call gets:

1. **IDs**: `x-request-id` and `x-correlation-id` metadata go onto the context with the same keys `httpx` uses, so `httpx.RequestID(ctx)`, `httpx.LoggerFrom(ctx)` and log lines work unchanged. Missing IDs are generated. Both are echoed in response headers.
2. **User**: `x-user` metadata (the `types.User` header format) becomes `types.UserFrom(ctx)`. Malformed users are rejected.
3. **Panic recovery** to `Internal`, with the stack in the log.
4. **Service token**: `authorization: Bearer <token>` checked in constant time when tokens are configured. Health and reflection skip it.
5. **Access log**: one line per call with method, code and duration.
6. **Errors**: any `httpx.Error` (or plain error) is turned into a gRPC status with an `ErrorInfo` detail. Internal error text never leaves the server.

Client side, `Dial` adds the IDs, the user and the token from the context to every call. `grpcx.FromStatus(err)` turns a status back into the original `httpx.Error`, so a 404 from db-handler is still a 404 with the same code and message at the REST edge.

Health: the standard `grpc.health.v1` service is registered. `grpcx.HealthCheck(conn, service)` returns a `monitoring.CheckFunc` for the prober.

## eventsx

```go
ev, _ := eventsx.New(ctx, "posts", "posts.created", post)
b, _ := eventsx.Marshal(ev)
p, _ := eventsx.Decode[Post](ev)
```

`Marshal` writes camelCase JSON with `data` as a plain object, which is what the SSE endpoints send to the browser as-is.

## Working on the protos

```sh
make tools      # buf, protoc-gen-go, protoc-gen-go-grpc at pinned versions
make generate   # buf generate into gen/go
make lint       # buf lint (STANDARD)
make breaking   # buf breaking against main
make check      # lint, generate, test, fail if gen/ is stale
```

CI runs lint, a breaking-change check on pull requests, and fails if `gen/` doesn't match the protos.

Adding a service: put `proto/ahmedbaig/<name>/v1/<name>.proto` in place (package `ahmedbaig.<name>.v1`, service name ending in `Service`, one request and response message per RPC), run `make generate`, commit both.
