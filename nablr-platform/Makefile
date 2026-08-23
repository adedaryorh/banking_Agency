.PHONY: up down dev-identity dev-gateway dev-transfers dev-vas dev-notification dev-worker \
	migrate migrate-identity migrate-notification migrate-transfers generate sqlc proto proto-identity proto-notification proto-transfers \
	swagger swagger-identity swagger-gateway swagger-notification

POSTGRES_USER ?= postgres
IDENTITY_DB ?= nabla_identity
NOTIFICATION_DB ?= nabla_notification
TRANSFERS_DB ?= nabla_transfers

# Development
up:
	docker-compose up -d

down:
	docker-compose down

dev-identity:
	cd services/identity-svc && air

dev-gateway:
	cd services/api-gateway && air

dev-transfers:
	cd services/transfers-svc && air

dev-vas:
	cd services/vas-svc && air

dev-notification:
	cd services/notification-svc && air


dev-worker:
	cd services/transfers-svc && set -a && source .env && set +a && go run ./cmd/worker

# Database migrations
migrate: migrate-identity migrate-notification migrate-transfers

migrate-identity: swagger-identity
	docker compose exec -T postgres psql \
		-U $(POSTGRES_USER) \
		-d $(IDENTITY_DB) \
		-v ON_ERROR_STOP=1 \
		-f /schemas/identity-schema.sql

migrate-notification:
	docker compose exec -T postgres psql \
		-U $(POSTGRES_USER) \
		-d $(NOTIFICATION_DB) \
		-v ON_ERROR_STOP=1 \
		-f /schemas/notification-schema.sql

migrate-transfers:
	docker compose exec -T postgres psql \
		-U $(POSTGRES_USER) \
		-d $(TRANSFERS_DB) \
		-v ON_ERROR_STOP=1 \
		-f /schemas/transfers-schema.sql

# Code Generation
generate: sqlc proto swagger

sqlc:
	cd services/identity-svc && sqlc generate
	cd services/notification-svc && sqlc generate
	cd services/api-gateway && sqlc generate 2>/dev/null || true
	cd services/transfers-svc && sqlc generate 2>/dev/null || true
	cd services/vas-svc && sqlc generate 2>/dev/null || true

proto: proto-identity proto-notification proto-transfers

proto-identity:
	@command -v protoc >/dev/null || \
		(echo "protoc is not installed" && exit 1)
	@command -v protoc-gen-go >/dev/null || \
		(echo "protoc-gen-go is not installed" && exit 1)
	@command -v protoc-gen-go-grpc >/dev/null || \
		(echo "protoc-gen-go-grpc is not installed" && exit 1)
	protoc \
		-I services/identity-svc \
		--go_out=services/identity-svc \
		--go_opt=paths=source_relative \
		--go-grpc_out=services/identity-svc \
		--go-grpc_opt=paths=source_relative \
		services/identity-svc/proto/identity/identity.proto

proto-notification:
	@command -v protoc >/dev/null || \
		(echo "protoc is not installed" && exit 1)
	@command -v protoc-gen-go >/dev/null || \
		(echo "protoc-gen-go is not installed" && exit 1)
	@command -v protoc-gen-go-grpc >/dev/null || \
		(echo "protoc-gen-go-grpc is not installed" && exit 1)
	protoc \
		-I services/notification-svc \
		--go_out=services/notification-svc \
		--go_opt=paths=source_relative \
		--go-grpc_out=services/notification-svc \
		--go-grpc_opt=paths=source_relative \
		services/notification-svc/proto/notification/notification.proto

# transfers-svc carries its OWN in-module copies of the identity and
# notification contracts (proto/identity, proto/notification) so it can generate
# gRPC CLIENTS: the Dockerfile builds this service as a single Go module with no
# access to sibling modules, so the stubs must live under nabla/transfers-svc.
# It also owns its server contract (proto/transfers). All three regenerate here,
# source-relative, into the service tree. Keep the client mirrors in sync with
# the upstream identity-svc / notification-svc .proto files before regenerating.
proto-transfers:
	@command -v protoc >/dev/null || \
		(echo "protoc is not installed" && exit 1)
	@command -v protoc-gen-go >/dev/null || \
		(echo "protoc-gen-go is not installed" && exit 1)
	@command -v protoc-gen-go-grpc >/dev/null || \
		(echo "protoc-gen-go-grpc is not installed" && exit 1)
	protoc \
		-I services/transfers-svc \
		--go_out=services/transfers-svc \
		--go_opt=paths=source_relative \
		--go-grpc_out=services/transfers-svc \
		--go-grpc_opt=paths=source_relative \
		services/transfers-svc/proto/transfers/transfers.proto \
		services/transfers-svc/proto/identity/identity.proto \
		services/transfers-svc/proto/notification/notification.proto

swagger: swagger-identity swagger-gateway swagger-notification

swagger-identity:
	cd services/identity-svc && go run github.com/swaggo/swag/cmd/swag init \
		-g main.go \
		-d cmd,internal/models \
		-o docs \
		--parseInternal

swagger-gateway:
	cd services/api-gateway && go run github.com/swaggo/swag/cmd/swag init \
		-g main.go \
		-d cmd \
		-o docs \
		--parseInternal

swagger-notification:
	cd services/notification-svc && go run github.com/swaggo/swag/cmd/swag init \
		-g main.go \
		-d cmd,internal/providers \
		-o docs \
		--parseInternal
