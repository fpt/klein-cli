#!/bin/bash

# Test code generation scenario
# Arguments: $1 = output file, $2 = error file

output_file="$1"
error_file="$2"

echo "Testing AI-generated Go code..."

# Check if add.go file was created
if [ ! -f "add.go" ]; then
    echo "✗ FAILED: add.go file not found - AI did not create the required file"
    echo "Available files:"
    ls -la
    exit 1
fi

echo "✓ add.go file found"

# Check if add.go contains the required Go code structure
if ! grep -q "package main" add.go; then
    echo "✗ FAILED: add.go missing 'package main' declaration"
    echo "File contents:"
    cat add.go
    exit 1
fi

if ! grep -q "func add(" add.go; then
    echo "✗ FAILED: add.go missing 'func add(' function declaration"
    echo "File contents:"
    cat add.go
    exit 1
fi

if ! grep -q "return" add.go; then
    echo "✗ FAILED: add.go missing 'return' statement"
    echo "File contents:"
    cat add.go
    exit 1
fi

if ! grep -q "int" add.go; then
    echo "✗ FAILED: add.go missing 'int' type declaration"
    echo "File contents:"
    cat add.go
    exit 1
fi

echo "✓ add.go contains proper Go code structure"

# Verify it compiles
if ! go build add.go > /dev/null 2>&1; then
    echo "✗ FAILED: Generated Go code doesn't compile"
    echo "Compilation errors:"
    go build add.go
    echo "File contents:"
    cat add.go
    exit 1
fi

echo "✓ Generated Go code compiles successfully"

echo ""
echo "🎉 ALL TESTS PASSED!"
echo "✅ AI successfully created working Go code with add function"

exit 0