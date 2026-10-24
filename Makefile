VERSION ?= $(shell cat VERSION)

.PHONY: fmt test vet race fuzz load security build extension compose-config local-up local-down worker-up worker-down vault-up vault-down keycloak-up keycloak-down k8s-validate k8s-apply k3d-up k3d-down k3d-image backup-restore-smoke staging-load-smoke runtime-isolation-check simulation check production-readiness live-metadata-check live-openai-e2e clean

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

extension:
	cd extensions/vscode && npm install --no-audit --no-fund && npm run compile

compose-config:
	HARNESSMESH_MCP_TOKEN=make-mcp-token \
	HARNESSMESH_BRIDGE_TOKEN=make-bridge-token \
	HARNESSMESH_PROVIDER_TOKEN=make-provider-token \
	HARNESSMESH_UID=$$(id -u) HARNESSMESH_GID=$$(id -g) \
		docker compose config --quiet

local-up:
	docker compose -f compose.yaml -f compose.production.yaml --profile mcp --profile bridge --profile console up -d --build

local-down:
	docker compose -f compose.yaml -f compose.production.yaml --profile mcp --profile bridge --profile console down

worker-up:
	docker compose -f compose.yaml -f compose.production.yaml --profile worker up -d --build harnessmesh-worker

worker-down:
	docker compose -f compose.yaml -f compose.production.yaml --profile worker down

vault-up:
	docker compose --profile vault up -d harnessmesh-vault

vault-down:
	docker compose --profile vault down

keycloak-up:
	docker compose --profile keycloak up -d harnessmesh-keycloak

keycloak-down:
	docker compose --profile keycloak down

k8s-validate:
	kubectl kustomize deploy/k8s >/dev/null

k8s-apply:
	kubectl apply -k deploy/k8s

k3d-up:
	k3d cluster create harnessmesh --agents $${K3D_AGENTS:-1} --wait
	$(MAKE) k3d-image
	$(MAKE) k8s-apply

k3d-image:
	docker build -t harnessmesh:local .
	k3d image import harnessmesh:local -c harnessmesh

k3d-down:
	k3d cluster delete harnessmesh

backup-restore-smoke:
	./scripts/backup-restore-smoke.sh

staging-load-smoke:
	./scripts/staging-load-smoke.sh

runtime-isolation-check:
	./scripts/runtime-isolation-check.sh

simulation:
	go test ./internal/simulation

check: fmt test vet build

production-readiness:
	./scripts/production-readiness.sh

openresponses-compliance:
	./scripts/openresponses-compliance.sh

chatgpt-mcp-bootstrap:
	./scripts/start-chatgpt-mcp.sh

chatgpt-compose-up:
	./scripts/start-chatgpt-compose.sh

chatgpt-compose-down:
	./scripts/stop-chatgpt-compose.sh

chatgpt-frp-up:
	./scripts/start-chatgpt-frp.sh

chatgpt-frp-down:
	./scripts/stop-chatgpt-frp.sh

live-metadata-check:
	./scripts/live-metadata-check.sh

live-openai-e2e:
	@test "$(HARNESSMESH_ALLOW_LIVE_OPENAI_INFERENCE)" = "1" || (echo "Refusing live OpenAI inference: set HARNESSMESH_ALLOW_LIVE_OPENAI_INFERENCE=1 explicitly."; exit 2)
	@echo "Live OpenAI inference is intentionally not part of the deterministic gate."

clean:
	rm -rf bin
