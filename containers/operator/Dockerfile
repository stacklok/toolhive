# Build the binary
FROM golang as builder

USER root

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

ARG CODEDIR \
VERSION \
COMMIT \
BUILD_DATE

# Copy the entire Go module structure
COPY . .

# Build
RUN CGO_ENABLED=0 LDFLAGS="-s -w \
-X github.com/stacklok/toolhive/pkg/versions.Version=${VERSION} \
-X github.com/stacklok/toolhive/pkg/versions.Commit=${COMMIT} \
-X github.com/stacklok/toolhive/pkg/versions.BuildDate=${BUILD_DATE} \
-X github.com/stacklok/toolhive/pkg/versions.BuildType=release" \
go build -ldflags "${LDFLAGS}" -o main ./${CODEDIR}/main.go

# Use minimal base image to package the binary
FROM registry.access.redhat.com/ubi10/ubi-minimal:10.1-1763362715

COPY --from=builder /workspace/main /
COPY LICENSE /licenses/LICENSE

USER 1001

ENTRYPOINT ["/main"]
