#!/bin/bash

set -e  # Exit on any error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Filtering env vars (comma-separated names, empty = all)
#   TESTS="fibonacci,web_search"   — run only these testcases
#   BACKENDS="ollama_qwen3_8b,anthropic" — run only these backends
#
# Example:
#   BACKENDS="ollama_gpt_oss_20b,ollama_qwen3_4b,ollama_qwen3_8b,ollama_qwen3_14b" \
#   TESTS="fibonacci,web_search,coding" \
#   CLI=output/klein ./testsuite/matrix_runner.sh

# Check if CLI is set
if [ -z "$CLI" ]; then
    echo "Error: CLI environment variable is not set"
    echo "Usage: CLI=path/to/klein ./testsuite/matrix_runner.sh"
    echo ""
    echo "Optional filters (comma-separated names):"
    echo "  TESTS=fibonacci,web_search      run only matching testcases"
    echo "  BACKENDS=ollama_qwen3_8b,anthropic  run only matching backends"
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
touch "$result_file"

echo -e "${BLUE}🧪 Running Klein Matrix Integration Tests${NC}"
echo -e "${BLUE}Binary: $CLI${NC}"
echo -e "${BLUE}Results will be saved to: $result_file${NC}"
[ -n "$TESTS" ]    && echo -e "${CYAN}Test filter:    $TESTS${NC}"
[ -n "$BACKENDS" ] && echo -e "${CYAN}Backend filter: $BACKENDS${NC}"
echo ""

# Function to log both to console and file
log_both() {
    echo -e "$1" | tee -a "$result_file"
}

log_both "=== Klein Matrix Integration Test Results ==="
log_both "Timestamp: $(date)"
log_both "Binary: $CLI"
log_both "TESTS filter:    ${TESTS:-(all)}"
log_both "BACKENDS filter: ${BACKENDS:-(all)}"
log_both ""

# Helper: return 0 if name is in comma-separated list (or list is empty)
in_filter() {
    local name="$1"
    local filter="$2"
    [ -z "$filter" ] && return 0
    echo "$filter" | tr ',' '\n' | grep -qx "$name"
}

# Function to check if a backend is available based on API keys / ollama
is_backend_available() {
    local backend_name="$1"
    case "$backend_name" in
        ollama*)
            return 0
            ;;
        openai)
            if [ -n "$OPENAI_API_KEY" ]; then
                return 0
            else
                log_both "${YELLOW}⚠️  Skipping $backend_name: OPENAI_API_KEY not set${NC}"
                return 1
            fi
            ;;
        anthropic)
            if [ -n "$ANTHROPIC_API_KEY" ]; then
                return 0
            else
                log_both "${YELLOW}⚠️  Skipping $backend_name: ANTHROPIC_API_KEY not set${NC}"
                return 1
            fi
            ;;
        gemini)
            if [ -n "$GEMINI_API_KEY" ]; then
                return 0
            else
                log_both "${YELLOW}⚠️  Skipping $backend_name: GEMINI_API_KEY not set${NC}"
                return 1
            fi
            ;;
        *)
            log_both "${YELLOW}⚠️  Unknown backend $backend_name, assuming available${NC}"
            return 0
            ;;
    esac
}

# Collect testcases (apply TESTS filter)
testcase_names=""
for testcase_dir in $(find "${script_dir}/testcases" -maxdepth 1 -type d | grep -v "/testcases$" | sort); do
    name=$(basename "$testcase_dir")
    in_filter "$name" "$TESTS" || continue
    [ -f "$testcase_dir/prompt.txt" ] || continue
    [ -x "$testcase_dir/check.sh" ]  || continue
    testcase_names="$testcase_names $name"
done
testcase_names="${testcase_names# }"  # trim leading space

# Collect backends (apply BACKENDS filter + availability check)
available_backends=""
for backend_file in $(find "${script_dir}/backends" -maxdepth 1 -name "*.json" | sort); do
    name=$(basename "$backend_file" .json)
    in_filter "$name" "$BACKENDS" || continue
    is_backend_available "$name" || continue
    available_backends="$available_backends $name"
done
available_backends="${available_backends# }"

if [ -z "$testcase_names" ]; then
    echo -e "${YELLOW}No testcases matched filter '${TESTS:-(all)}'${NC}"
    exit 0
fi
if [ -z "$available_backends" ]; then
    echo -e "${YELLOW}No backends matched filter '${BACKENDS:-(all)}' or all require missing API keys${NC}"
    exit 0
fi

testcase_count=$(echo "$testcase_names" | wc -w | tr -d ' ')
backend_count=$(echo "$available_backends" | wc -w | tr -d ' ')
total_combinations=$((testcase_count * backend_count))

log_both "${BLUE}📊 Test Matrix:${NC}"
log_both "Testcases (${testcase_count}): $testcase_names"
log_both "Backends  (${backend_count}): $available_backends"
log_both "Total combinations: $total_combinations"
log_both ""

# result_map: key "backend:testcase" → "PASS" or "FAIL"
# Stored as a flat list of "backend:testcase:result" strings (bash 3.2 compat)
result_entries=""

total_tests=0
passed_tests=0
failed_tests=0

for backend_name in $available_backends; do
    backend_file="${script_dir}/backends/${backend_name}.json"
    for testcase_name in $testcase_names; do
        total_tests=$((total_tests + 1))
        log_both "${CYAN}Running: $testcase_name × $backend_name${NC}"

        if "${script_dir}/runner.sh" "$testcase_name" "$backend_name" > /tmp/matrix_test_output 2>&1; then
            log_both "${GREEN}✅ PASS: $testcase_name × $backend_name${NC}"
            passed_tests=$((passed_tests + 1))
            result_entries="$result_entries ${backend_name}:${testcase_name}:PASS"
        else
            log_both "${RED}❌ FAIL: $testcase_name × $backend_name${NC}"
            cat /tmp/matrix_test_output | tee -a "$result_file"
            failed_tests=$((failed_tests + 1))
            result_entries="$result_entries ${backend_name}:${testcase_name}:FAIL"
        fi
        rm -f /tmp/matrix_test_output
        log_both ""
    done
done

# ── Tabular summary ────────────────────────────────────────────────────────────
log_both "${BLUE}📊 Result Matrix:${NC}"

# Column width: max testcase name length + 2
col_w=4
for t in $testcase_names; do
    len=${#t}
    [ $len -gt $col_w ] && col_w=$len
done
col_w=$((col_w + 2))

# Row label width: max backend name length + 2
lbl_w=8
for b in $available_backends; do
    len=${#b}
    [ $len -gt $lbl_w ] && lbl_w=$len
done
lbl_w=$((lbl_w + 2))

# Header row
header=$(printf "%-${lbl_w}s" "")
for t in $testcase_names; do
    header="$header$(printf "%-${col_w}s" "$t")"
done
log_both "$header"

# Separator
sep=$(printf '%*s' $((lbl_w + col_w * testcase_count)) '' | tr ' ' '-')
log_both "$sep"

# Data rows
for b in $available_backends; do
    row=$(printf "%-${lbl_w}s" "$b")
    for t in $testcase_names; do
        result="?"
        for entry in $result_entries; do
            if [ "$entry" = "${b}:${t}:PASS" ]; then
                result="✅"
                break
            elif [ "$entry" = "${b}:${t}:FAIL" ]; then
                result="❌"
                break
            fi
        done
        row="$row$(printf "%-${col_w}s" "$result")"
    done
    log_both "$row"
done
log_both ""

# ── Final counts ───────────────────────────────────────────────────────────────
log_both "${BLUE}📊 Summary:${NC}"
log_both "Total: $total_tests  ${GREEN}Passed: $passed_tests${NC}  ${RED}Failed: $failed_tests${NC}"
[ $total_tests -gt 0 ] && log_both "Success rate: $(( (passed_tests * 100) / total_tests ))%"
log_both "Completed: $(date)"
log_both "Results saved to: $result_file"

if [ $failed_tests -eq 0 ]; then
    log_both "${GREEN}🎉 All matrix tests passed!${NC}"
    exit 0
else
    log_both "${RED}💥 Some matrix tests failed!${NC}"
    exit 1
fi
