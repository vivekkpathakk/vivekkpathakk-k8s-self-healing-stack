# Stage 1: build (keep GO_VERSION in step with the "go" line in go.mod)
ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/healer ./cmd/healer

# Stage 2: minimal runtime, non-root (dashboard template is embedded in the binary)
FROM alpine:3.22
RUN apk --no-cache add ca-certificates && adduser -D -u 10001 healer
USER 10001
COPY --from=builder /out/healer /usr/local/bin/healer
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/healer"]
