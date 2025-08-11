#!/bin/bash

set -e  # Exit on any error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Check if CLI is set
if [ -z "$CLI" ]; then
    echo "Error: CLI environment variable is not set"
    echo "Usage: CLI=path/to/klein ./testsuite/runner.sh"
    exit 1
fi

# Check if the binary exists
if [ ! -x "$CLI" ]; then
    echo "Error: CLI binary '$CLI' does not exist or is not executable"
    exit 1
fi

# Create timestamped result file
timestamp=$(date +"%Y%m%d_%H%M%S")
script_dir="$(cd "$(dirname "$0")" && pwd)"
result_file="${script_dir}/results/test_results_${timestamp}.txt"
results_dir="${script_dir}/results"
mkdir -p "$results_dir"

# Create the result file immediately to avoid tee errors
touch "$result_file"

echo -e "${BLUE}🧪 Running Klein Matrix Integration Tests${NC}"
echo -e "${BLUE}Binary: $CLI${NC}"
echo -e "${BLUE}Results will be saved to: $result_file${NC}"
echo ""

# Function to log both to console and file
log_both() {
    echo -e "$1" | tee -a "$result_file"
}

# Start logging
log_both "=== Klein Matrix Integration Test Results ==="
log_both "Timestamp: $(date)"
log_both "Binary: $CLI"
log_both "Test Runner: $(basename "$0")"
log_both ""

# Function to run a single test case with a specific backend using runner.sh
run_matrix_test() {
    local testcase_name="$1"
    local backend_name="$2"
    
    log_both "${CYAN}Running: $testcase_name × $backend_name${NC}"
    
    # Use the runner.sh script which handles all the logic (cleanup, workdir, validation, etc.)
    echo "  Running: ${script_dir}/runner.sh $testcase_name $backend_name"
    if "${script_dir}/runner.sh" "$testcase_name" "$backend_name" > /tmp/matrix_test_output 2>&1; then
        log_both "${GREEN}✅ PASS: $testcase_name × $backend_name${NC}"
        rm -f /tmp/matrix_test_output
        return 0
    else
        log_both "${RED}❌ FAIL: $testcase_name × $backend_name${NC}"
        log_both "Output:"
        cat /tmp/matrix_test_output | tee -a "$result_file"
        rm -f /tmp/matrix_test_output
        return 1
    fi
}

# Function to check if a backend is available based on API keys
is_backend_available() {
    local backend_file="$1"
    local backend_name=$(basename "$backend_file" .json)
    
    case "$backend_name" in
        "ollama")
            # Ollama variants are always available
            return 0
            ;;
"openai")
            # Check for OPENAI_API_KEY
            if [ -n "$OPENAI_API_KEY" ]; then
                return 0
            else
                log_both "${YELLOW}⚠️  Skipping $backend_name: OPENAI_API_KEY not set${NC}"
                return 1
            fi
            ;;
        "gemini")
            # Check for GEMINI_API_KEY
            if [ -n "$GEMINI_API_KEY" ]; then
                return 0
            else
                log_both "${YELLOW}⚠️  Skipping $backend_name: GEMINI_API_KEY not set${NC}"
                return 1
            fi
            ;;
        *)
            # Unknown backend, assume available
            log_both "${YELLOW}⚠️  Unknown backend $backend_name, assuming available${NC}"
            return 0
            ;;
    esac
}

# Find all testcases and backends
testcase_dirs=$(find "${script_dir}/testcases" -maxdepth 1 -type d -name "*" | grep -v "/testcases$" | sort)
all_backend_files=$(find "${script_dir}/backends" -maxdepth 1 -name "*.json" | sort)

# Convert to names for runner.sh compatibility
testcase_names=""
for testcase_dir in $testcase_dirs; do
    testcase_name=$(basename "$testcase_dir")
    if [ -z "$testcase_names" ]; then
        testcase_names="$testcase_name"
    else
        testcase_names="$testcase_names $testcase_name"
    fi
done

if [ -z "$testcase_dirs" ]; then
    echo -e "${YELLOW}No testcases found in testsuite/testcases/${NC}"
    exit 0
fi

if [ -z "$all_backend_files" ]; then
    echo -e "${YELLOW}No backend configurations found in testsuite/backends/${NC}"
    exit 0
fi

# Filter available backends
available_backends=""
for backend_file in $all_backend_files; do
    if is_backend_available "$backend_file"; then
        if [ -z "$available_backends" ]; then
            available_backends="$backend_file"
        else
            available_backends="$available_backends $backend_file"
        fi
    fi
done

if [ -z "$available_backends" ]; then
    echo -e "${YELLOW}No available backends (all require missing API keys)${NC}"
    exit 0
fi

# Count testcases and available backends
testcase_count=$(echo "$testcase_names" | wc -w)
backend_count=$(echo "$available_backends" | wc -w)
total_combinations=$((testcase_count * backend_count))

log_both "${BLUE}📊 Test Matrix:${NC}"
log_both "Testcases: $testcase_count"
log_both "Available backends: $backend_count"
log_both "Total combinations: $total_combinations"
log_both ""

# Show which backends are available
log_both "${BLUE}🔧 Available Backends:${NC}"
for backend_file in $available_backends; do
    backend_name=$(basename "$backend_file" .json)
    log_both "  ✓ $backend_name"
done
log_both ""

total_tests=0
passed_tests=0
failed_tests=0

# Run matrix tests
for testcase_name in $testcase_names; do
    for backend_file in $available_backends; do
        backend_name=$(basename "$backend_file" .json)
        total_tests=$((total_tests + 1))
        if run_matrix_test "$testcase_name" "$backend_name"; then
            passed_tests=$((passed_tests + 1))
        else
            failed_tests=$((failed_tests + 1))
        fi
        log_both ""
    done
done

# Print summary
log_both "${BLUE}📊 Matrix Test Summary:${NC}"
log_both "Total combinations: $total_tests"
log_both "${GREEN}Passed: $passed_tests${NC}"
if [ $failed_tests -gt 0 ]; then
    log_both "${RED}Failed: $failed_tests${NC}"
else
    log_both "Failed: $failed_tests"
fi
log_both ""

# Calculate success rate
if [ $total_tests -gt 0 ]; then
    success_rate=$(( (passed_tests * 100) / total_tests ))
    log_both "Success Rate: ${success_rate}%"
fi

# Add final timestamp
log_both "Completed: $(date)"
log_both "Results saved to: $result_file"

if [ $failed_tests -eq 0 ]; then
    log_both "${GREEN}🎉 All matrix tests passed!${NC}"
    exit 0
else
    log_both "${RED}💥 Some matrix tests failed!${NC}"
    exit 1
fi