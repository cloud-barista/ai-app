APP    := ai-app-server
AGENT  := ai-agent
MOCK_RECOMMENDER := mock-partner-recommender
GOBIN  := $(shell go env GOPATH)/bin
IMAGE  := ai-agent-proto:local

.PHONY: build run-app run-agent run-mock-recommender test swag fmt lint verify docker-build docker-up docker-down docker-logs

build:
	go build -o bin/$(APP) ./cmd/$(APP)
	go build -o bin/$(AGENT) ./cmd/$(AGENT)
	go build -o bin/$(MOCK_RECOMMENDER) ./mock/partner/recommender/cmd/recommender

# Both start with no configuration at all. Settings come from conf/setup.env when
# that file exists and from built-in defaults otherwise, so a fresh checkout
# runs and serves everything that does not touch a cloud.
run-app:
	go run ./cmd/$(APP)

# The agent reaches the tools through the AI-App MCP server; start run-app too.
run-agent:
	go run ./cmd/$(AGENT)

# Partner institution recommender (mock implementation), not part of this
# prototype. Point AIAPP_RECOMMENDER_URL of run-app at http://127.0.0.1:8092.
run-mock-recommender:
	go run ./mock/partner/recommender/cmd/recommender

test:
	go test ./...

swag:
	$(GOBIN)/swag init -g cmd/$(APP)/main.go -o api/docs --parseInternal

fmt:
	gofmt -w .

lint:
	$(GOBIN)/golangci-lint run

verify: fmt build test lint

docker-build:
	docker build -t $(IMAGE) .

# Also needs no configuration. Copy .env.example to .env only to point at a real
# CB-Tumblebug or to supply a planning model.
docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f
