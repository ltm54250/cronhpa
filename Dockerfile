# Build the manager binary
FROM golang:1.16 as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY cron/ cron/
COPY lib/ lib/
COPY server/ server/


# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -a -o cronhpa-controller main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM alpine:3.12.0
RUN apk add --no-cache tzdata
WORKDIR /
COPY --from=builder /workspace/cronhpa-controller .
COPY docker-entrypoint.sh .
RUN chmod +x /docker-entrypoint.sh

ENTRYPOINT  ["/docker-entrypoint.sh"]
CMD ["/cronhpa-controller"]

