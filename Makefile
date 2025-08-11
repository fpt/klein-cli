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

install: ## Install the application
	go install ./klein

test: ## Run tests
	go test ./...

lint: ## Run linters
	golangci-lint run

fmt: ## Format code
	gofmt -s -w .

fix: ## Fix code issues
	golangci-lint run --fix

integ: build ## Matrix integration test (testcases × backends)
	CLI=output/klein ./testsuite/matrix_runner.sh

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
