#!/bin/bash

# Test web_search - agent fetches a Wikipedia page and identifies who Ujiie Naomoto served
# Ujiie Naomoto (1512–1571) served the Saitō clan of Mino province, then became a retainer
# of Oda Nobunaga. He was one of the "Mino Triumvirate".
# Arguments: $1 = output file, $2 = error file

output_file="$1"
error_file="$2"

EXTRACT_RESPONSE="./extract_response.sh"

response_content=$("$EXTRACT_RESPONSE" "$output_file")

# Must mention Saitō (first lord) AND Oda/Nobunaga (second lord)
has_saito=false
has_oda=false

if echo "$response_content" | grep -iq "Sait"; then
    has_saito=true
fi

if echo "$response_content" | grep -iq "Oda\|Nobunaga"; then
    has_oda=true
fi

if $has_saito && $has_oda; then
    echo "✓ Web search test: correctly identified that Ujiie Naomoto served the Saitō clan and Oda Nobunaga"
    exit 0
else
    echo "✗ Web search test: response did not identify Ujiie Naomoto's lords correctly"
    echo "Expected: mentions of Saitō (clan) AND Oda/Nobunaga"
    echo "has_saito=$has_saito, has_oda=$has_oda"
    echo "Response content was:"
    echo "$response_content"
    if [ -s "$error_file" ]; then
        echo "Errors:"
        cat "$error_file"
    fi
    exit 1
fi
