.PHONY: all build test test-unit test-e2e clean coverage

# Default target
all: build test

# Build the project
build:
	cargo build

# Run all tests
test: test-unit test-e2e

# Run unit tests only
test-unit:
	cargo test --lib

# Run e2e tests only
test-e2e:
	cargo test --test e2e

# Run BDD tests only
test-bdd:
	cargo test --test bdd_tests

# Run e2e tests with specific feature or tag
test-e2e-feature:
	@if [ -z "$(FEATURE)" ]; then \
		echo "Usage: make test-e2e-feature FEATURE=<feature_name_or_tag>"; \
		echo "Available features:"; \
		ls tests/e2e/features/*.feature | xargs -n1 basename | sed 's/\.feature//'; \
		echo ""; \
		echo "Available tags:"; \
		echo "  @server     - Server lifecycle tests"; \
		echo "  @cli        - CLI command tests"; \
		echo "  @transport  - Transport mechanism tests"; \
		echo "  @sse        - SSE transport tests"; \
		echo "  @stdio      - stdio transport tests"; \
		echo "  @permissions - Permission profile tests"; \
		echo "  @security   - Security constraint tests"; \
		echo "  @protocol   - MCP protocol tests"; \
		echo "  @mcp        - MCP-related tests"; \
		exit 1; \
	fi
	CUCUMBER_FILTER=$(FEATURE) cargo test --test e2e

# Run e2e tests with JUnit reports
test-e2e-junit:
	mkdir -p target/cucumber-reports
	CUCUMBER_JUNIT_OUTPUT=target/cucumber-reports/junit.xml cargo test --test e2e

# Run e2e tests with verbose output
test-e2e-verbose:
	RUST_LOG=debug cargo test --test e2e

# Clean the project
clean:
	cargo clean

# Run code coverage
coverage:
	./coverage.sh

# Help target
help:
	@echo "Available targets:"
	@echo "  all          - Build and test the project (default)"
	@echo "  build        - Build the project"
	@echo "  test         - Run all tests"
	@echo "  test-unit    - Run unit tests only"
	@echo "  test-e2e     - Run e2e tests only"
	@echo "  test-bdd     - Run BDD-style tests only"
	@echo "  test-e2e-feature FEATURE=<name> - Run specific e2e feature"
	@echo "  test-e2e-junit - Run e2e tests with JUnit reports"
	@echo "  test-e2e-verbose - Run e2e tests with verbose output"
	@echo "  coverage     - Generate code coverage report"
	@echo "  clean        - Clean the project"
	@echo "  help         - Show this help message"