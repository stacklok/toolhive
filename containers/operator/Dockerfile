# Build the binary
FROM registry.access.redhat.com/ubi10/ubi:10.0-1760519443 as builder

USER root

# As per https://discussion.fedoraproject.org/t/fedora-doesnt-provide-latest-version-of-go/146930/9
WORKDIR /etc/yum.repos.d
RUN dnf install -y wget
RUN wget https://copr.fedorainfracloud.org/coprs/g/go-sig/golang-rawhide/repo/epel-10/group_go-sig-golang-rawhide-epel-10.repo
RUN dnf install -y golang

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
GOOS=linux GOARCH=amd64 go build -ldflags "${LDFLAGS}" -o main ./${CODEDIR}/main.go

# Use micro base image to package the binary
FROM  --platform=linux/amd64 registry.access.redhat.com/ubi10/ubi-micro:10.0

COPY --from=builder /workspace/main /
COPY LICENSE /licenses/LICENSE

USER 1001

ENTRYPOINT ["/main"]
