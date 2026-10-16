.PHONY: build fmt test up down reup db-clear

build:
	go build -buildvcs=false -o bin/mobius ./cmd/mobius
	go build -buildvcs=false -o bin/loki ./cmd/loki

fmt:
	gofmt -w cmd internal migrations web tests

test:
	@test -n "$$TEST_DATABASE_URL" || { echo 'Set TEST_DATABASE_URL to a dedicated empty test database'; exit 1; }
	go vet ./...
	go test -race -tags=integration -count=1 ./...
	node --test tests/web/*.test.mjs

up:
	docker compose up -d --build --wait

down:
	docker compose down

reup:
	$(MAKE) down
	$(MAKE) up

db-clear:
	docker compose down --volumes
