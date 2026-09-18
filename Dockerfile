FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go test ./... \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/harnessmesh ./cmd/harnessmesh

FROM alpine:3.21
RUN apk add --no-cache ca-certificates git \
 && addgroup -S harnessmesh \
 && adduser -S -G harnessmesh -u 10001 harnessmesh
COPY --from=build /out/harnessmesh /usr/local/bin/harnessmesh
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/harnessmesh"]
