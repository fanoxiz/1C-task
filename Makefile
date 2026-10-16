DATABASE_URL ?= postgres://onec:onec@127.0.0.1:54320/onec?sslmode=disable
export DATABASE_URL

.PHONY: build fmt fmt-check vet test test-race test-web test-integration check db migrate run-mobius run-loki up down

build:
	go build -o bin/mobius ./cmd/mobius
	go build -o bin/loki ./cmd/loki

fmt:
	gofmt -w cmd internal migrations web tests

fmt-check:
	@test -z "$$(gofmt -l cmd internal migrations web tests)" || { gofmt -l cmd internal migrations web tests; exit 1; }

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

test-web:
	node --test tests/web/*.test.mjs

test-integration:
	@test -n "$(TEST_DATABASE_URL)" || { echo 'Set TEST_DATABASE_URL to a dedicated empty test database'; exit 1; }
	go test -tags=integration -count=1 ./tests/integration/...

check: fmt-check vet test test-race

db:
	docker compose up -d --wait postgres

migrate:
	go run ./cmd/mobius migrate

run-mobius:
	go run ./cmd/mobius

run-loki:
	go run ./cmd/loki

up:
	docker compose up -d --build --wait

down:
	docker compose down
