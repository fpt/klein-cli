#!/bin/bash

# Test research scenario - knowledge-based response
# Arguments: $1 = output file, $2 = error file

output_file="$1"
error_file="$2"

# Use the local copy of extract_response utility
EXTRACT_RESPONSE="./extract_response.sh"

# Extract only the response content using the utility script
response_content=$("$EXTRACT_RESPONSE" "$output_file")

# Check that response contains clean architecture concepts
if echo "$response_content" | grep -iq "SOLID\|dependency\|abstraction\|separation\|clean\|architecture\|principle"; then
    echo "✓ Research scenario provided relevant clean architecture information"
    exit 0
else
    echo "✗ Research scenario response missing expected clean architecture concepts"
    echo "Expected: mentions of SOLID, dependency, abstraction, separation, etc."
    echo "Response content was:"
    echo "$response_content"
    if [ -s "$error_file" ]; then
        echo "Errors:"
        cat "$error_file"
    fi
    exit 1
fi