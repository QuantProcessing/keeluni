.PHONY: dev test build up down logs smoke

dev:            ## run gateway + trader locally (builds web)
	scripts/dev.sh

test:           ## all offline tests (hedger DB tests run when HEDGER_TEST_DATABASE_URL is set)
	go test -short ./... -count=1
	cd web && npm test

build:
	cd web && npm ci && npm run build
	go build -o bin/gateway ./cmd/gateway
	go build -o bin/trader ./cmd/trader
	go build -o bin/hedger ./cmd/hedger
	go build -o bin/optioner ./cmd/optioner

up:
	docker compose up -d --build --force-recreate
	@echo "Open http://127.0.0.1:8080 — first-run password: docker compose logs trader | grep password"

down:
	docker compose down

logs:
	docker compose logs -f --tail=200

smoke:          ## public market checks against the running compose stack (no orders)
	CGO_ENABLED=0 GOOS=linux go build -o bin/smoke-linux ./cmd/smoke
	docker run --rm --network keeluni_internal -v keeluni-data:/data:ro \
	  -v $(CURDIR)/bin/smoke-linux:/smoke:ro --user 65532 gcr.io/distroless/static-debian12:nonroot \
	  /smoke -url http://gateway:8081 -token-file /data/gateway/token
