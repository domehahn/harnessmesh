.PHONY: fmt test vet build check clean

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

test:
	go test ./...

vet:
	go vet ./...

build:
	mkdir -p bin
	go build -trimpath -o bin/harnessmesh ./cmd/harnessmesh

check: fmt test vet build

clean:
	rm -rf bin
