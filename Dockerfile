FROM golang:1.24.1-alpine AS builder

RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o vt ./cmd/vibetool

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
RUN adduser -D -g '' appuser
WORKDIR /app
COPY --from=builder /app/vt .
USER appuser
ENTRYPOINT ["/app/vt"] 
