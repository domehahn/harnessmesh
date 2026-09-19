FROM golang:1.23-alpine AS build
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
RUN apk add --no-cache ca-certificates git tzdata sqlite-libs \
 && addgroup -S harnessmesh \
 && adduser -S -G harnessmesh -u 10001 harnessmesh
COPY --from=build /out/harnessmesh /usr/local/bin/harnessmesh
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/harnessmesh"]
