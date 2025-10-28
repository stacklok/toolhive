# Build the binary
FROM registry.access.redhat.com/ubi10/go-toolset:1.24 as builder

USER root

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

ARG TARGETOS \ 
TARGETARCH \
CODEDIR \
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
GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
go build -ldflags "${LDFLAGS}" -o main ./${CODEDIR}/main.go

# Use micro base image to package the binary
FROM registry.access.redhat.com/ubi10/ubi-micro:10.0

COPY --from=builder /workspace/main /
COPY LICENSE /licenses/LICENSE

USER 1001

ENTRYPOINT ["/main"]
