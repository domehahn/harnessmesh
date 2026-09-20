VERSION ?= $(shell cat VERSION)

.PHONY: fmt test vet race fuzz load security build check clean

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

test:
	go test ./...

vet:
	go vet ./...

race:
	go test -race ./...

fuzz:
	go test ./internal/knowledge -run FuzzArchiveSearchDoesNotPanic

load:
	go test ./internal/knowledge -bench . -benchtime=1s -run '^$$'

security:
	go vet ./...
	govulncheck ./...

build:
	mkdir -p bin
	go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/harnessmesh ./cmd/harnessmesh

check: fmt test vet build

clean:
	rm -rf bin
