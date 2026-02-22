#!/bin/bash

# Test web_search - agent fetches a Wikipedia page and identifies who Ujiie Naomoto served
# Ujiie Naomoto (1512–1571) became a retainer of Oda Nobunaga.
# The Wikipedia stub page mentions Oda Nobunaga but not the Saitō clan.
# Arguments: $1 = output file, $2 = error file

output_file="$1"
error_file="$2"

EXTRACT_RESPONSE="./extract_response.sh"

response_content=$("$EXTRACT_RESPONSE" "$output_file")

# Must mention Oda Nobunaga (the Wikipedia stub page content)
has_oda=false

if echo "$response_content" | grep -iq "Oda\|Nobunaga"; then
    has_oda=true
fi

if $has_oda; then
    echo "✓ Web search test: correctly identified that Ujiie Naomoto served Oda Nobunaga"
    exit 0
else
    echo "✗ Web search test: response did not mention Oda Nobunaga"
    echo "Response content was:"
    echo "$response_content"
    if [ -s "$error_file" ]; then
        echo "Errors:"
        cat "$error_file"
    fi
    exit 1
fi
