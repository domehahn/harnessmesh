VERSION ?= $(shell cat VERSION)

.PHONY: fmt test vet race fuzz load security build check production-readiness live-metadata-check live-openai-e2e clean

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

production-readiness:
	./scripts/production-readiness.sh

live-metadata-check:
	@echo "Metadata-only live checks are opt-in and must be run with a configured provider URL."
	@echo "This target performs no /responses inference."

live-openai-e2e:
	@test "$(HARNESSMESH_ALLOW_LIVE_OPENAI_INFERENCE)" = "1" || (echo "Refusing live OpenAI inference: set HARNESSMESH_ALLOW_LIVE_OPENAI_INFERENCE=1 explicitly."; exit 2)
	@echo "Live OpenAI inference is intentionally not part of the deterministic gate."

clean:
	rm -rf bin
