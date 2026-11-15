BUF ?= buf

.PHONY: tools generate lint breaking test check

tools:
	go install github.com/bufbuild/buf/cmd/buf@v1.75.0
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2

generate:
	$(BUF) generate

lint:
	$(BUF) lint

breaking:
	$(BUF) breaking --against '.git#branch=main'

test:
	go vet ./...
	go test -race ./...

check: lint generate test
	git diff --exit-code -- gen
