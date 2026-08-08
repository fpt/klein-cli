run: ## Run the application
	go run ./klein

run-anthropic: ## Run the application with Anthropic backend
	go run ./klein -b anthropic

run-openai: ## Run the application with OpenAI backend
	go run ./klein -b openai

run-gemini: ## Run the application with Gemini backend
	go run ./klein -b gemini

build: ## Build the application
	go build -o output/klein ./klein

build-all: build ## Build all binaries (the gateway is now `klein claw`)

install: ## Install the application
	go install ./klein

proto: ## Generate protobuf + Connect-gRPC Go code
	rm -rf internal/gen
	buf generate

test: ## Run tests
	go test ./...

vet: ## Run go vet static analysis
	go vet ./...

lint: vet ## Run linters (go vet + golangci-lint)
	golangci-lint run

fmt: ## Format code
	gofmt -s -w .

fix: ## Fix code issues
	golangci-lint run --fix

integ: build ## Matrix integration test (testcases × backends)
	CLI=output/klein BACKENDS="openai,anthropic,gemini" ./testsuite/matrix_runner.sh

# The bare `go test` skips these, so an unset GALLIUM_BIN would look like a pass;
# fail loudly instead, and say how to get a binary. Point it at a fresh build:
# a stale `make install`ed gallium fails for the wrong reason.
test-gallium: ## App-server integration test against a real gallium (GALLIUM_BIN=...)
	@test -n "$(GALLIUM_BIN)" || { \
		echo "set GALLIUM_BIN=/path/to/gallium"; \
		echo "  build one: cargo build --release -p gallium-agent --no-default-features"; \
		echo "  (in a rs-gallium checkout; no model backends needed)"; \
		exit 1; }
	GALLIUM_BIN="$(GALLIUM_BIN)" go test ./pkg/agentserver/ -run Gallium -v

test-capabilities: ## Capability testing
	go build -o output/test-capabilities ./cmd/test-capabilities
	./output/test-capabilities

zip: ## Create a minimal zip archive of source files (excludes build outputs and .klein)
	@echo "Creating minimal source archive..."
	@mkdir -p output
	zip -r output/klein-source.zip . \
		-x "output/*" \
		-x ".klein/*" \
		-x "*.zip" \
		-x ".git/*" \
		-x "*.log" \
		-x "*.tmp" \
		-x "*~" \
		-x ".DS_Store" \
		-x ".claude/*" \
		-x "klein" \
		-x "testsuite/results/*"
	@echo "Archive created: output/klein-source.zip"
	@echo "Archive size: $$(du -h output/klein-source.zip | cut -f1)"

help: ## Display this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "%-20s %s\n", $$1, $$2}'
