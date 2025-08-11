#!/bin/bash

# Test multi-step fibonacci program generation and execution
# Arguments: $1 = output file (from klein), $2 = error file

output_file="$1"
error_file="$2"

# Use the local copy of extract_response utility
EXTRACT_RESPONSE="./extract_response.sh"

echo "Testing AI-generated multi-step Fibonacci program..."

# Extract only the response content using the utility script
response_content=$("$EXTRACT_RESPONSE" "$output_file")

# Check if the response contains mentions of both phases of the task
if ! echo "$response_content" | grep -i -q "fibonacci.*10" && ! echo "$response_content" | grep -i -q "10.*fibonacci"; then
    echo "✗ Basic Fibonacci generation task not found in klein response"
    echo "Response content was:"
    echo "$response_content"
    exit 1
fi

if ! echo "$response_content" | grep -i -q "command.*line.*arg\|argument"; then
    echo "✗ Command line argument task not found in klein response"
    echo "Response content was:"
    echo "$response_content"
    exit 1
fi

echo "✓ Both phases (Fibonacci generation, command line args) found in response"

# =====================================
# PHASE 1 VALIDATION: Basic Fibonacci
# =====================================
echo ""
echo "🔍 Validating Phase 1: Basic Fibonacci program..."

# Check if main.go file was created
if [ ! -f "main.go" ]; then
    echo "✗ Phase 1 FAILED: main.go file not found - AI did not create the required file"
    echo "Available files:"
    ls -la
    exit 1
fi

echo "✓ Phase 1: main.go file found"

# Check if main.go contains basic Go program structure
if ! grep -q "package main" main.go; then
    echo "✗ Phase 1 FAILED: main.go missing 'package main' declaration"
    cat main.go
    exit 1
fi

if ! grep -q "func main" main.go; then
    echo "✗ Phase 1 FAILED: main.go missing 'func main()' function"
    cat main.go
    exit 1
fi

echo "✓ Phase 1: Go program structure looks correct"

# Try to run the program (Phase 1 should work without arguments)
echo "Testing Phase 1: Running program without arguments..."
if ! go run main.go > step1_output.txt 2> step1_error.txt; then
    echo "✗ Phase 1 FAILED: Program doesn't run without arguments"
    echo "Compilation/runtime errors:"
    cat step1_error.txt
    echo "Generated code was:"
    cat main.go
    rm -f step1_output.txt step1_error.txt
    exit 1
fi

echo "✓ Phase 1: Program runs successfully without arguments"

# Check the output format and values for Phase 1
phase1_output=$(cat step1_output.txt)
echo "Phase 1 output: $phase1_output"

# Expected first 10 Fibonacci numbers: 0 1 1 2 3 5 8 13 21 34
# Accept variations: some might start with 1 1 instead of 0 1
if echo "$phase1_output" | grep -q "0 1 1 2 3 5 8 13 21 34" ||
   echo "$phase1_output" | grep -q "1 1 2 3 5 8 13 21 34" ||
   echo "$phase1_output" | grep -q "0, 1, 1, 2, 3, 5, 8, 13, 21, 34" ||
   echo "$phase1_output" | grep -q "1, 1, 2, 3, 5, 8, 13, 21, 34"; then
    echo "✅ Phase 1 PASSED: Fibonacci sequence is correct!"
else
    echo "✗ Phase 1 FAILED: Fibonacci sequence is incorrect"
    echo "Expected: 0 1 1 2 3 5 8 13 21 34 (or starting with 1 1...)"
    echo "Got: $phase1_output"
    
    # Still check if it contains some valid Fibonacci numbers
    if echo "$phase1_output" | grep -q "1.*2.*3.*5.*8.*13"; then
        echo "⚠️  Phase 1 PARTIAL: Contains some Fibonacci numbers"
    else
        echo "✗ Phase 1 FAILED: No recognizable Fibonacci sequence found"
        rm -f step1_output.txt step1_error.txt
        exit 1
    fi
fi

# =====================================
# PHASE 2 VALIDATION: Command Line Args
# =====================================
echo ""
echo "🔍 Validating Phase 2: Command line argument support..."

# Check if the program accepts arguments by testing with 5
echo "Testing Phase 2: Running program with argument '5'..."
if ! go run main.go 5 > step2_test5.txt 2> step2_error5.txt; then
    echo "✗ Phase 2 FAILED: Program doesn't accept command line arguments"
    echo "Error when running 'go run main.go 5':"
    cat step2_error5.txt
    echo "✅ Phase 1 PASSED, but Phase 2 FAILED"
    rm -f step1_output.txt step1_error.txt step2_test5.txt step2_error5.txt
    exit 1
fi

phase2_output5=$(cat step2_test5.txt)
echo "Phase 2 output with arg '5': $phase2_output5"

# Expected first 5 Fibonacci numbers: 0 1 1 2 3 (or 1 1 2 3 5)
if echo "$phase2_output5" | grep -q "0 1 1 2 3" ||
   echo "$phase2_output5" | grep -q "1 1 2 3 5" ||
   echo "$phase2_output5" | grep -q "0, 1, 1, 2, 3" ||
   echo "$phase2_output5" | grep -q "1, 1, 2, 3, 5"; then
    echo "✓ Phase 2: Program correctly handles argument '5'"
else
    echo "✗ Phase 2 FAILED: Incorrect output for 5 numbers"
    echo "Expected: first 5 Fibonacci numbers"
    echo "Got: $phase2_output5"
    echo "✅ Phase 1 PASSED, but Phase 2 FAILED"
    rm -f step1_output.txt step1_error.txt step2_test5.txt step2_error5.txt
    exit 1
fi

# Test with 15 to ensure it works with larger numbers
echo "Testing Phase 2: Running program with argument '15'..."
if ! go run main.go 15 > step2_test15.txt 2> step2_error15.txt; then
    echo "✗ Phase 2 FAILED: Program fails with larger argument '15'"
    echo "Error:"
    cat step2_error15.txt
    echo "✅ Step 1 PASSED, ⚠️ Step 2 PARTIAL (works with 5 but not 15)"
    rm -f step1_output.txt step1_error.txt step2_test5.txt step2_error5.txt step2_test15.txt step2_error15.txt
    exit 1
fi

phase2_output15=$(cat step2_test15.txt)
echo "Phase 2 output with arg '15': $phase2_output15"

# Check if output has 15 numbers (count words, handling trailing spaces)
num_count=$(echo "$phase2_output15" | wc -w | xargs)
if [ "$num_count" -eq 15 ]; then
    echo "✓ Phase 2: Program correctly generates 15 numbers"
elif echo "$phase2_output15" | grep -o "," | wc -l | grep -q "14"; then
    echo "✓ Phase 2: Program correctly generates 15 numbers (comma-separated)"
else
    echo "⚠️ Phase 2 PARTIAL: Generated $num_count numbers instead of 15"
    echo "Output: $phase2_output15"
fi

# Final validation - check that default (no args) still works
echo "Testing Phase 2: Verifying default behavior still works..."
if ! go run main.go > step2_default.txt 2> step2_default_error.txt; then
    echo "✗ Phase 2 FAILED: Default behavior broken after modification"
    echo "✅ Phase 1 PASSED initially, but Phase 2 modification broke default behavior"
    rm -f step1_output.txt step1_error.txt step2_test5.txt step2_error5.txt step2_test15.txt step2_error15.txt step2_default.txt step2_default_error.txt
    exit 1
fi

phase2_default_output=$(cat step2_default.txt)
echo "Phase 2 default output: $phase2_default_output"

if echo "$phase2_default_output" | grep -q "0 1 1 2 3 5 8 13 21 34" ||
   echo "$phase2_default_output" | grep -q "1 1 2 3 5 8 13 21 34"; then
    echo "✓ Phase 2: Default behavior (10 numbers) still works"
else
    echo "✗ Phase 2 FAILED: Default behavior changed unexpectedly"
    echo "Expected 10 numbers by default, got: $phase2_default_output"
    echo "✅ Phase 1 PASSED, ⚠️ Phase 2 PARTIAL (args work but default broken)"
    # Cleanup
    rm -f step1_output.txt step1_error.txt step2_test5.txt step2_error5.txt step2_test15.txt step2_error15.txt step2_default.txt step2_default_error.txt main.go
    exit 1
fi

echo ""
echo "🎉 ALL TESTS PASSED!"
echo "✅ Phase 1: Basic Fibonacci generation - PASSED"
echo "✅ Phase 2: Command line argument support - PASSED"
echo "✓ AI successfully completed both phases of the Fibonacci challenge!"

# Cleanup
rm -f step1_output.txt step1_error.txt step2_test5.txt step2_error5.txt step2_test15.txt step2_error15.txt step2_default.txt step2_default_error.txt main.go

exit 0