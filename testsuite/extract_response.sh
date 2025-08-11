#!/bin/bash

# Utility script to extract only the response content from klein output, excluding thinking blocks
# Usage: extract_response.sh <output_file> [turn_number]
# 
# For single-turn responses (most tests):
#   extract_response.sh output.txt
#
# For multi-turn responses (memory_state test):
#   extract_response.sh output.txt 1
#   extract_response.sh output.txt 2
#
# Returns: The content from the response header ("klein (<model>)") to the turn delimiter, excluding thinking output

output_file="$1"
turn_number="$2"

if [ -z "$output_file" ]; then
    echo "Usage: extract_response.sh <output_file> [turn_number]" >&2
    echo "Examples:" >&2
    echo "  extract_response.sh output.txt        # Single response" >&2
    echo "  extract_response.sh output.txt 2      # Turn 2 response" >&2
    exit 1
fi

if [ ! -f "$output_file" ]; then
    echo "Error: Output file '$output_file' not found" >&2
    exit 1
fi

if [ -z "$turn_number" ]; then
    # Single response mode - extract from 'klein (' header to delimiter
    sed -n '/klein (/,/^────────────────────────────────────────────────────────────$/p' "$output_file" | \
    sed '$d'  # Remove the delimiter line
else
    # Multi-turn mode - extract specific turn's response section
    awk "/Turn $turn_number/,/^────────────────────────────────────────────────────────────\$/" "$output_file" | \
    sed -n '/klein (/,/^────────────────────────────────────────────────────────────$/p' | \
    sed '$d'  # Remove the delimiter line
fi
