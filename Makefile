APP    := ai-app-server
ENGINE := ai-automation-engine
MOCK_RECOMMENDER := mock-partner-recommender
MOCK_NL := mock-partner-nl-agent
GOBIN  := $(shell go env GOPATH)/bin
IMAGE  := ai-agent-proto:local

.PHONY: build run-app run-engine run-mock-recommender run-mock-nl test swag fmt lint verify docker-build docker-up docker-down docker-logs

build:
	go build -o bin/$(APP) ./cmd/$(APP)
	go build -o bin/$(ENGINE) ./cmd/$(ENGINE)
	go build -o bin/$(MOCK_RECOMMENDER) ./mock/partner/recommender/cmd/recommender
	go build -o bin/$(MOCK_NL) ./mock/partner/nlagent/cmd/nl-agent

# All start with no configuration at all. Settings come from conf/setup.env when
# that file exists and from built-in defaults otherwise, so a fresh checkout
# runs and serves everything that does not touch a cloud.
run-app:
	go run ./cmd/$(APP)

# The automation engine, :8091. It runs its workflows through the AI-App MCP
# server and uses no LLM; start run-app too.
run-engine:
	go run ./cmd/$(ENGINE)

# Partner institution recommender (mock implementation), not part of this
# prototype. Point AIAPP_RECOMMENDER_URL of run-app at http://127.0.0.1:8092.
run-mock-recommender:
	go run ./mock/partner/recommender/cmd/recommender

# Partner institution natural language agent (mock implementation), not part of
# this prototype. :8093; it calls the automation engine tools over MCP, so start
# run-engine too. The AI-App service forwards /aiapp/partner/nl/* to it.
run-mock-nl:
	go run ./mock/partner/nlagent/cmd/nl-agent

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
