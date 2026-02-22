#!/bin/bash

# Test planning capability with coordinated code changes
# Arguments: $1 = output file (from klein), $2 = error file

output_file="$1"
error_file="$2"

echo "Testing planning capability with coordinated refactoring..."

# Check if main.go exists and can be compiled
if [ ! -f "main.go" ]; then
    echo "✗ main.go file not found"
    exit 1
fi

echo "✓ main.go file found"

# Try to compile the code
if go build -o test_binary ./main.go 2>compile_errors.txt; then
    echo "✓ Code compiles successfully"
    compilation_success=true
else
    echo "✗ Code compilation failed"
    echo "Compilation errors:"
    cat compile_errors.txt
    compilation_success=false
fi

# Check for required changes in the code
echo ""
echo "Checking for required changes..."

# 1. Check if User.ID field changed from int to string
if grep -q "ID.*string" main.go; then
    echo "✓ User.ID field changed to string type"
    id_change=true
else
    echo "✗ User.ID field not changed to string type"
    id_change=false
fi

# 2. Check if UpdateUserAge was refactored to User method
if grep -q "func.*User.*UpdateAge" main.go && ! grep -q "UserService.*UpdateUserAge" main.go; then
    echo "✓ UpdateUserAge refactored to User.UpdateAge method"
    update_method=true
else
    echo "✗ UpdateUserAge not properly refactored to User method"
    update_method=false
fi

# 3. Check if main() function uses string IDs
if grep -q "AddUser.*\".*\".*\".*\"" main.go; then
    echo "✓ main() function updated to use string IDs"
    main_updated=true
else
    echo "✗ main() function not updated to use string IDs"
    main_updated=false
fi

# 5. Check if method calls were updated consistently
if grep -q "PrintUser" main.go && grep -q "\.UpdateAge" main.go; then
    echo "✓ Method calls updated consistently (PrintUser preserved, user.UpdateAge)"
    calls_updated=true
else
    echo "✗ Method calls not updated consistently"
    calls_updated=false
fi

# Try to run the compiled binary if compilation succeeded
if [ "$compilation_success" = true ]; then
    echo ""
    echo "Testing runtime execution..."
    if ./test_binary > runtime_output.txt 2>&1; then
        echo "✓ Code runs successfully"
        runtime_success=true
        echo "Runtime output:"
        cat runtime_output.txt | head -10
    else
        echo "✗ Code runtime failed"
        echo "Runtime errors:"
        cat runtime_output.txt
        runtime_success=false
    fi
else
    runtime_success=false
fi

# Clean up test files
rm -f test_binary compile_errors.txt runtime_output.txt

# Final assessment
all_changes_made=true
if [ "$id_change" = false ] || [ "$update_method" = false ] || \
   [ "$main_updated" = false ] || [ "$calls_updated" = false ]; then
    all_changes_made=false
fi

echo ""
echo "📋 Planning Capability Assessment:"
echo "================================="

if [ "$compilation_success" = true ] && [ "$runtime_success" = true ] && [ "$all_changes_made" = true ]; then
    echo "🎉 EXCELLENT STEP-BY-STEP PLANNING: All changes implemented correctly!"
    echo "✓ Compilation: SUCCESS"
    echo "✓ Runtime: SUCCESS" 
    echo "✓ STEP 1 - ID type change: COMPLETED"
    echo "✓ STEP 2 - User.UpdateAge method: COMPLETED"
    echo "✓ Code cleanup: COMPLETED"
    echo "✓ Consistency: MAINTAINED"
    echo ""
    echo "🏆 FULL SUCCESS: Step-by-step refactoring completed perfectly!"
    exit 0
elif [ "$compilation_success" = true ] && [ "$all_changes_made" = true ]; then
    echo "✅ GOOD PLANNING: All changes made, minor runtime issues"
    echo "✓ All required changes implemented"
    echo "⚠️  Runtime had some issues but structure is correct"
    echo ""
    echo "✓ PARTIAL SUCCESS: Planning capability demonstrated"
    exit 0
elif [ "$compilation_success" = true ]; then
    echo "⚠️  PARTIAL PLANNING: Some changes made, code compiles"
    echo "✓ Code compiles successfully"
    echo "✗ Not all required changes implemented"
    echo ""
    echo "⚠️  PARTIAL SUCCESS: Basic planning shown but incomplete"
    exit 1
else
    echo "❌ POOR PLANNING: Critical issues with coordinated changes"
    echo "✗ Code does not compile"
    echo "✗ Planning and coordination failed"
    echo ""
    echo "❌ FAILURE: Unable to coordinate multiple related changes"
    exit 1
fi
