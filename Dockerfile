FROM golang:1.26-alpine AS build
RUN apk add --no-cache gcc musl-dev git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY VERSION ./VERSION
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 go test ./... \
 && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X main.version=$(cat VERSION)" -o /out/harnessmesh ./cmd/harnessmesh

FROM alpine:3.21
RUN apk add --no-cache ca-certificates git tzdata sqlite-libs sqlite bubblewrap \
 && addgroup -S harnessmesh \
 && adduser -S -G harnessmesh -u 10001 harnessmesh
COPY --from=build /out/harnessmesh /usr/local/bin/harnessmesh
COPY scripts/backup-sqlite.sh /usr/local/bin/harnessmesh-backup
COPY scripts/restore-sqlite.sh /usr/local/bin/harnessmesh-restore
RUN chmod 0755 /usr/local/bin/harnessmesh-backup /usr/local/bin/harnessmesh-restore
WORKDIR /workspace
USER 10001:10001
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/harnessmesh", "version"]
ENTRYPOINT ["/usr/local/bin/harnessmesh"]
