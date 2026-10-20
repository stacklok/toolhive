# Build environment for Anthropic's OSS Scanner (https://github.com/anthropics/oss-scanner).
# The scanner builds this image online and then audits ToolHive inside it offline,
# so this file must fetch everything the build and the tests need.
# Keep the Go version at or above go.mod's: GOTOOLCHAIN=local won't fetch one.
FROM golang:1.27

ENV GOTOOLCHAIN=local

COPY . /src
WORKDIR /src

RUN go mod download && go mod verify
RUN go build ./... \
    && go build -o /usr/local/bin/thv ./cmd/thv \
    && go build -o /usr/local/bin/thv-operator ./cmd/thv-operator \
    && go build -o /usr/local/bin/thv-proxyrunner ./cmd/thv-proxyrunner \
    && go build -o /usr/local/bin/vmcp ./cmd/vmcp

# Unit tests only: e2e and operator integration tests need a container runtime or
# envtest. The run compiles and caches every test binary for offline use; keep the
# image even if a test fails so one regression does not block the audit.
RUN go test $(go list ./... | grep -v '/test/e2e' | grep -v '/cmd/thv-operator/test-integration') \
    || echo "WARNING: go test reported failures"

ENV GOPROXY=off \
    GOSUMDB=off
