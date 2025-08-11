#!/bin/bash

# Test multi-turn prompt file with memory persistence between turns  
# Arguments: $1 = output file (from -f prompts.txt), $2 = error file

output_file="$1"
error_file="$2"

# Use the local copy of extract_response utility
EXTRACT_RESPONSE="./extract_response.sh"

echo "Testing multi-turn prompts with memory persistence between turns..."

# Check that the output contains both turns
if grep -iq "Turn 1" "$output_file" && grep -iq "Turn 2" "$output_file"; then
    echo "✓ Multi-turn execution: found both Turn 1 and Turn 2 outputs"
else
    echo "✗ Multi-turn execution: missing turn outputs"
    echo "Output was:"
    cat "$output_file"
    exit 1
fi

# Extract Turn 1 response content using the utility script
turn1_content=$("$EXTRACT_RESPONSE" "$output_file" 1)

# Check that Turn 1 correctly answered the frog legs question
if echo "$turn1_content" | grep -iq "4\|four"; then
    echo "✓ Turn 1: correctly answered frog legs question"
else
    echo "✗ Turn 1: no correct answer about frog legs found"
    echo "Turn 1 content was:"
    echo "$turn1_content"
    exit 1
fi

# Extract Turn 2 response content using the utility script
turn2_content=$("$EXTRACT_RESPONSE" "$output_file" 2)

# Check that Turn 2 DOES remember frog context (memory persistence between turns)
# The second turn should remember frogs and answer about frog arms (none/no arms)
if echo "$turn2_content" | grep -iq "frog\|amphibian"; then
    # AI remembered the frog context, now check if it correctly answered about arms
    if echo "$turn2_content" | grep -iq "no.*arms\|don't.*have.*arms\|zero.*arms\|without.*arms\|no.*front.*limbs\|don't have arms"; then
        echo "✓ Turn 2: correctly remembers frog context and answers 'no arms'"
        echo "✓ Memory persistence: conversation context properly maintained between turns"
        exit 0
    elif echo "$turn2_content" | grep -iq "2\|two.*arms\|two.*front"; then
        echo "✓ Turn 2: correctly remembers frog context and answers '2 arms'"
        echo "✓ Memory persistence: conversation context properly maintained between turns"
        exit 0
    else
        echo "⚠️  Turn 2: remembers frogs but unclear answer about arms"
        echo "Turn 2 content was:"
        echo "$turn2_content"
        # Still pass since memory persistence is working
        exit 0
    fi
elif echo "$turn2_content" | grep -iq "unclear\|clarification\|what.*arms\|human.*arms\|context"; then
    echo "✗ Turn 2: forgot frog context (no memory persistence)"
    echo "Expected: AI should remember frogs from Turn 1 and answer about frog arms"
    echo "Turn 2 content was:"
    echo "$turn2_content"
    exit 1
else
    echo "⚠️  Turn 2: response unclear about frog context"
    echo "Turn 2 content was:"
    echo "$turn2_content"
    # Check if it answered about arms in general without mentioning frogs
    if echo "$turn2_content" | grep -iq "arms"; then
        echo "✗ Turn 2: gave generic arms answer without frog context (memory not persistent)"
        exit 1
    else
        echo "⚠️  Turn 2: unclear response, assuming memory issue"
        exit 1
    fi
fi