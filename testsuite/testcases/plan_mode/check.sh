#!/bin/bash

# Test plan mode: EnterPlanMode → plan → ExitPlanMode → implementation.
# Validates that the agent used plan mode AND produced a correct
# three-approach arithmetic-progression sum in Go.
#
# Arguments: $1 = output file (full klein log), $2 = error file

output_file="$1"
error_file="$2"

echo "Testing plan mode with arithmetic progression implementation..."

# ─── 1. Verify plan mode tools were invoked ──────────────────────────────────

echo ""
echo "Checking plan mode tool usage..."

if grep -q "EnterPlanMode" "$output_file" 2>/dev/null; then
    echo "✓ EnterPlanMode was called"
else
    echo "✗ EnterPlanMode was NOT called — agent skipped planning phase"
    exit 1
fi

if grep -q "ExitPlanMode" "$output_file" 2>/dev/null; then
    echo "✓ ExitPlanMode was called (plan was presented)"
else
    echo "✗ ExitPlanMode was NOT called — agent never submitted a plan"
    exit 1
fi

# ─── 2. Verify the output file exists ────────────────────────────────────────

echo ""
echo "Checking generated file..."

if [ ! -f "sum_ap.go" ]; then
    echo "✗ sum_ap.go not found"
    echo "Available files:"
    ls -la
    exit 1
fi
echo "✓ sum_ap.go found"

# ─── 3. Check it compiles ────────────────────────────────────────────────────

if go build -o sum_ap_bin ./sum_ap.go 2>compile_errors.txt; then
    echo "✓ Compiles successfully"
else
    echo "✗ Compilation failed:"
    cat compile_errors.txt
    rm -f compile_errors.txt
    exit 1
fi
rm -f compile_errors.txt

# ─── 4. Verify at least two distinct implementations are present ──────────────

echo ""
echo "Checking implementation approaches..."

has_iterative=false
has_recursive=false
has_equation=false

# Iterative: a for loop that accumulates a sum
if grep -qE "for .* range|for [a-zA-Z]+ := 0" sum_ap.go; then
    echo "✓ Iterative (for loop) approach found"
    has_iterative=true
else
    echo "✗ Iterative approach not detected"
fi

# Recursive: a function that calls itself
if grep -qE "[a-zA-Z]+\([^)]*\)[^{]*\{[^}]*[a-zA-Z]+\(" sum_ap.go || \
   grep -E "func [a-zA-Z]+\(" sum_ap.go | wc -l | grep -qv "^[01]$"; then
    # simpler check: look for a function that references itself
    funcs=$(grep -oE "^func ([a-zA-Z_][a-zA-Z0-9_]*)\(" sum_ap.go | sed 's/func \([^(]*\)(.*/\1/')
    for fn in $funcs; do
        if grep -c "$fn(" sum_ap.go 2>/dev/null | grep -q "^[2-9]\|^[1-9][0-9]"; then
            echo "✓ Recursive approach found (function '$fn' calls itself)"
            has_recursive=true
            break
        fi
    done
    if [ "$has_recursive" = false ]; then
        echo "✗ Recursive approach not detected"
    fi
fi

# Closed-form: uses multiplication/division in a single expression (no loop, no recursion)
if grep -qE "\* *\(|n *\*|float|int64" sum_ap.go; then
    echo "✓ Closed-form equation approach found"
    has_equation=true
else
    echo "✗ Closed-form equation not detected"
fi

# Require at least 2 of the 3 approaches
approaches=0
[ "$has_iterative" = true ] && approaches=$((approaches+1))
[ "$has_recursive" = true ] && approaches=$((approaches+1))
[ "$has_equation" = true ]  && approaches=$((approaches+1))

if [ $approaches -lt 2 ]; then
    echo "✗ Only $approaches approach(es) detected — need at least 2 of: iterative, recursive, equation"
    exit 1
fi
echo "✓ $approaches/3 approaches implemented"

# ─── 5. Run and verify output contains 55 ────────────────────────────────────

echo ""
echo "Running program..."

if ./sum_ap_bin > run_output.txt 2>run_errors.txt; then
    echo "✓ Program runs successfully"
    echo "Output:"
    cat run_output.txt
else
    echo "✗ Runtime error:"
    cat run_errors.txt
    rm -f sum_ap_bin run_output.txt run_errors.txt
    exit 1
fi

# n=10, a1=1, d=1 → sum = 10*(2*1+(10-1)*1)/2 = 10*11/2 = 55
if grep -q "55" run_output.txt; then
    echo "✓ Correct result 55 found in output"
else
    echo "✗ Expected sum 55 not found in output"
    echo "Got:"
    cat run_output.txt
    rm -f sum_ap_bin run_output.txt run_errors.txt
    exit 1
fi

# ─── Cleanup and pass ────────────────────────────────────────────────────────

rm -f sum_ap_bin run_output.txt run_errors.txt

echo ""
echo "📋 Plan Mode Assessment:"
echo "========================"
echo "✓ Agent entered plan mode (EnterPlanMode)"
echo "✓ Agent submitted a plan (ExitPlanMode)"
echo "✓ Implementation compiles and produces correct output"
echo "✓ $approaches/3 approaches present"
echo ""
echo "🏆 PASS: Plan mode + arithmetic progression implementation successful"
exit 0
