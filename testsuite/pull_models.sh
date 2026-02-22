#!/bin/bash
# Pull candidate Ollama models for integration testing.
# Criteria: tool-capable, below 40B, newer than gpt-oss:20b (Aug 2025).
#
# Usage: ./testsuite/pull_models.sh

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BLUE='\033[0;34m'
NC='\033[0m'

# Candidates: "model|rationale" pairs
# qwen3 size comparison (vs gpt-oss:20b baseline), then other candidates
ENTRIES=(
    "qwen3:4b|4B · confirmed tool-capable · 2.6 GB"
    "qwen3:8b|8B · confirmed tool-capable · 5.2 GB"
    "qwen3:14b|14B · confirmed tool-capable · 9.0 GB"
    "rnj-1:8b|8B dense · Dec 2025 · top BFCL tool-use leaderboard · 5.1 GB"
    "glm-4.7-flash|30B-A3B MoE · Jan 2026 (newest) · 128K context · 19 GB"
    "nemotron-3-nano:30b|30B-A3.5B MoE · Sep-Dec 2025 · 1M context · strong agentic · 24 GB"
    "lfm2.5-thinking|1.2B · Jan 2026 · thinking model · tools tag · tiny"
)

TOTAL=${#ENTRIES[@]}

echo -e "${BLUE}╔══════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║        Ollama Model Pull Script                      ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${CYAN}Models to pull ($TOTAL total):${NC}"
i=1
for entry in "${ENTRIES[@]}"; do
    model="${entry%%|*}"
    note="${entry##*|}"
    echo -e "  ${i}. ${YELLOW}${model}${NC} — ${note}"
    i=$((i + 1))
done
echo ""

# Check ollama is running
if ! ollama list &>/dev/null; then
    echo -e "${RED}Error: ollama is not running. Start it with: ollama serve${NC}"
    exit 1
fi

# Track results
PASSED=()
FAILED=()
SKIPPED=()

i=1
for entry in "${ENTRIES[@]}"; do
    model="${entry%%|*}"
    note="${entry##*|}"

    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "[$i/$TOTAL] ${YELLOW}${model}${NC}"
    echo -e "      ${note}"
    echo ""

    if ollama list 2>/dev/null | awk '{print $1}' | grep -qx "$model"; then
        echo -e "  ${GREEN}✓ Already present — skipping pull${NC}"
        SKIPPED+=("$model")
        echo ""
        i=$((i + 1))
        continue
    fi

    echo -e "  Pulling..."
    if ollama pull "$model"; then
        echo -e "  ${GREEN}✓ Done${NC}"
        PASSED+=("$model")
    else
        echo -e "  ${RED}✗ Pull failed for $model${NC}"
        FAILED+=("$model")
    fi
    echo ""
    i=$((i + 1))
done

echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}Summary${NC}"
echo ""
if [ ${#PASSED[@]} -gt 0 ]; then
    echo -e "${GREEN}Pulled (${#PASSED[@]}):${NC}"
    for m in "${PASSED[@]}"; do echo "  • $m"; done
fi
if [ ${#SKIPPED[@]} -gt 0 ]; then
    echo -e "${YELLOW}Already present (${#SKIPPED[@]}):${NC}"
    for m in "${SKIPPED[@]}"; do echo "  • $m"; done
fi
if [ ${#FAILED[@]} -gt 0 ]; then
    echo -e "${RED}Failed (${#FAILED[@]}):${NC}"
    for m in "${FAILED[@]}"; do echo "  • $m"; done
fi
echo ""

# Print ready-to-run test commands
READY=("${PASSED[@]}" "${SKIPPED[@]}")
if [ ${#READY[@]} -gt 0 ]; then
    echo -e "${CYAN}Run integration tests (build first: go build -o output/klein ./klein):${NC}"
    for m in "${READY[@]}"; do
        # Derive a backend-like label for the model
        label=$(echo "$m" | tr ':' '-')
        echo "  CLI=output/klein ./testsuite/matrix_runner.sh  # or:"
        echo "  CLI=output/klein ./testsuite/runner.sh web_search ollama   # edit backends/ollama.json to model=$m"
        break  # Show pattern once; specific per-model commands follow
    done
    echo ""
    echo -e "${CYAN}Per-model one-liners (set model in settings then run):${NC}"
    for m in "${READY[@]}"; do
        echo "  CLI=output/klein ./testsuite/runner.sh fibonacci ollama    # after: set model=$m in backends/ollama.json"
    done
fi
