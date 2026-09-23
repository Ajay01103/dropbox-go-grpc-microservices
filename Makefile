PROTO_DIR     := proto
AUTH_SVC      := services/auth
UPLOAD_SVC    := services/upload
METADATA_SVC  := services/metadata
SHARING_SVC   := services/sharing

# Do not globally export per-service .env values here; Goose variables can
# collide across services and cause migrations to run against the wrong DB.

.PHONY: help proto build build-auth build-upload build-metadata run-auth run-upload run-metadata tidy check-events scylla-up scylla-init-schema scylla-ui scylla-all scylla-shell rustfs-up rustfs-shell rustfs-logs dev-start docker-up docker-down docker-logs

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ─── Code Generation ──────────────────────────────────────────────────────────

proto: ## Generate proto code for all services and the v2 events contract
	cd $(PROTO_DIR)/auth && npx @bufbuild/buf generate
	@echo "✓ Auth proto generated"
	cd $(PROTO_DIR)/upload && npx @bufbuild/buf generate
	@echo "✓ Upload proto generated"
	cd $(PROTO_DIR)/metadata && npx @bufbuild/buf generate
	@echo "✓ Metadata proto generated"
	cd $(PROTO_DIR)/sharing && npx @bufbuild/buf generate
	@echo "✓ Sharing proto generated"
	cd $(PROTO_DIR)/events && npx @bufbuild/buf generate
	@echo "✓ Events v2 proto generated"

# ─── Build ────────────────────────────────────────────────────────────────────

build: build-auth build-upload build-metadata build-sharing ## Build all service binaries

build-auth: ## Build the Auth service binary
	cd $(AUTH_SVC) && go build -o ../../bin/auth ./cmd/
	@echo "✓ Auth service built"

build-upload: ## Build the Upload service binary
	cd $(UPLOAD_SVC) && go build -o ../../bin/upload ./cmd/
	@echo "✓ Upload service built"

build-metadata: ## Build the Metadata service binary
	cd $(METADATA_SVC) && go build -o ../../bin/metadata ./cmd/
	@echo "✓ Metadata service built"

build-sharing: ## Build the Sharing service binary
	cd $(SHARING_SVC) && go build -o ../../bin/sharing ./cmd/
	@echo "✓ Sharing service built"

# ─── Run ──────────────────────────────────────────────────────────────────────

run-auth: ## Start Auth service (requires ScyllaDB running - see scylla-up)
	cd $(AUTH_SVC) && go run ./cmd/

run-upload: ## Start Upload service (requires ScyllaDB + RustFS running)
	cd $(UPLOAD_SVC) && go run ./cmd/

run-metadata: ## Start Metadata service (requires ScyllaDB running)
	cd $(METADATA_SVC) && go run ./cmd/

run-sharing: ## Start Sharing service (requires ScyllaDB running)
	cd $(SHARING_SVC) && go run ./cmd/

tidy: ## Tidy Go modules
	cd $(AUTH_SVC) && go mod tidy
	go work sync

check-events: ## Fail if subject/stream/consumer string literals appear outside pkg/events
	@rg -n --glob '*.go' --glob '!**/*_test.go' \
	  -e '"(uploads|blocks|files|dlq)\.[a-z0-9_.>*-]+"' \
	  -e '"(UPLOAD_EVENTS|BLOCK_REFS(_CMD|_EVT)?|FILE_EVENTS|DLQ)"' \
	  -e '"(metadata-thumbnail-worker|upload-block-decrement-worker|metadata-purge-worker|thumbnail-v2|decref-v2|purge-completion-v2)"' \
	  services pkg/natsx \
	  && { echo "ERROR: NATS contract literals found outside pkg/events (docs/NATS-V2-REDESIGN.md A.5)."; exit 1; } \
	  || true

# ─── ScyllaDB Management ──────────────────────────────────────────────────────

scylla-up: ## Start ScyllaDB cluster in Docker (required before running auth service)
	podman compose up -d scylladb
	@echo "✓ ScyllaDB starting..."
	@echo "  Waiting for cluster to be ready (health check: 30s startup + up to 75s for retries)"
	@echo "  Monitor with: podman compose logs -f scylladb"

scylla-init-schema: ## Manually initialize ScyllaDB schema (usually auto-initializes on auth service startup)
	@echo "ScyllaDB schema automatically initializes when auth service starts."
	@echo "To manually initialize if needed, run:"
	@echo "  docker exec scylladb-dev cqlsh -u cassandra -p cassandra < services/auth/db/schema.cql"

scylla-all: scylla-up scylla-ui ## Start ScyllaDB + DBeaver Web UI

scylla-shell: ## Open interactive CQL shell to ScyllaDB
	podman exec -it scylladb-dev cqlsh -u cassandra -p cassandra

rustfs-up: ## Start local RustFS S3 + initialize uploads bucket
	podman compose up -d rustfs rustfs-init
	@echo "✓ RustFS starting..."
	@echo "  S3 API: http://localhost:9000"
	@echo "  Console: http://localhost:9001"
	@echo "  Credentials: rustfsadmin / rustfsadmin"

rustfs-shell: ## Open an interactive shell in the RustFS container
	podman exec -it rustfs-dev /bin/sh

rustfs-logs: ## Tail logs for RustFS and bucket init container
	podman compose logs -f rustfs rustfs-init

# ─── Docker ───────────────────────────────────────────────────────────────────

docker-up: ## Start all containers (detached)
	podman compose --env-file .env up -d

docker-down: ## Stop and remove containers
	podman compose down

docker-logs: ## Tail logs for all services
	podman compose logs -f
