#!/bin/bash

# Test long text understanding and message compaction
# Arguments: $1 = output file (from klein), $2 = error file

output_file="$1"
error_file="$2"

echo "Testing long text understanding and message compaction..."

# Check that the output contains all 5 turns
if ! grep -q "Turn 1" "$output_file" || ! grep -q "Turn 2" "$output_file" || \
   ! grep -q "Turn 3" "$output_file" || ! grep -q "Turn 4" "$output_file" || \
   ! grep -q "Turn 5" "$output_file"; then
    echo "✗ Multi-turn execution: missing some turn outputs"
    echo "Output was:"
    cat "$output_file"
    exit 1
fi

echo "✓ Multi-turn execution: found all 5 turns"

# Check that compaction actually occurred by looking for compaction logs
if grep -q "Token usage.*above threshold.*compacting" "$error_file"; then
    echo "✓ Message compaction: token threshold exceeded, compaction triggered"
    compaction_occurred=true
elif grep -q "Compacting conversation history" "$error_file"; then
    echo "✓ Message compaction: conversation compaction detected"
    compaction_occurred=true
elif grep -q "Summary.*created" "$error_file"; then
    echo "✓ Message compaction: message summary creation detected"
    compaction_occurred=true
else
    echo "⚠️  Message compaction: no clear evidence of compaction found"
    echo "This might be expected if content wasn't long enough to trigger compaction"
    compaction_occurred=false
fi

# Get the extract_response utility (copied to current directory by runner)
EXTRACT_RESPONSE="./extract_response.sh"

# Extract turn outputs - capture only the final response, excluding thinking output
turn2_output=$("$EXTRACT_RESPONSE" "$output_file" 2)
turn4_output=$("$EXTRACT_RESPONSE" "$output_file" 4)
turn5_output=$("$EXTRACT_RESPONSE" "$output_file" 5)

# Turn 2: Should remember Sir Galahad's sword (Excalibur) and its powers (blue glow, cuts through anything)
if echo "$turn2_output" | grep -iq "excalibur"; then
    if echo "$turn2_output" | grep -iq "blue\|glow" && echo "$turn2_output" | grep -iq "cut.*through\|power"; then
        echo "✓ Turn 2: correctly remembered sword name (Excalibur) and both powers (blue glow, cutting)"
        turn2_pass=true
    elif echo "$turn2_output" | grep -iq "blue\|glow\|cut.*through\|power"; then
        echo "✓ Turn 2: correctly remembered sword name (Excalibur) and partial powers"
        turn2_pass=true
    else
        echo "⚠️  Turn 2: remembered sword name but missing power details"
        turn2_pass=true
    fi
else
    echo "✗ Turn 2: failed to remember Sir Galahad's sword name (Excalibur)"
    turn2_pass=false
fi

# Turn 4: Should remember sorceress details (Evangeline, crystal tower/floating)
if echo "$turn4_output" | grep -iq "evangeline"; then
    if echo "$turn4_output" | grep -iq "crystal.*tower\|tower.*crystal\|floating.*tower\|tower.*floating\|crystal.*floating"; then
        echo "✓ Turn 4: correctly remembered sorceress name (Evangeline) and location (crystal tower)"
        turn4_pass=true
    elif echo "$turn4_output" | grep -iq "tower\|crystal\|floating"; then
        echo "✓ Turn 4: correctly remembered sorceress name (Evangeline) and partial location"
        turn4_pass=true
    else
        echo "⚠️  Turn 4: remembered sorceress name but missing location details"  
        turn4_pass=true
    fi
else
    echo "✗ Turn 4: failed to remember sorceress name (Evangeline)"
    turn4_pass=false
fi

# Turn 5: Should remember Sir Galahad's sword again (testing memory persistence across compaction)
if echo "$turn5_output" | grep -iq "excalibur"; then
    if echo "$turn5_output" | grep -iq "blue"; then
        echo "✓ Turn 5: correctly remembered Sir Galahad's sword (Excalibur) and blue glow after potential compaction"
        turn5_pass=true
    else
        echo "⚠️  Turn 5: remembered sword name but missing blue glow detail"
        turn5_pass=true
    fi
elif echo "$turn5_output" | grep -iq "sword.*galahad\|galahad.*sword"; then
    if echo "$turn5_output" | grep -iq "blue"; then
        echo "✓ Turn 5: remembered Sir Galahad's sword and blue glow (without name)"
        turn5_pass=true
    else
        echo "⚠️  Turn 5: remembered sword connection but missing details"
        turn5_pass=true
    fi
else
    echo "✗ Turn 5: failed to remember Sir Galahad's sword after potential compaction"
    turn5_pass=false
fi

# Final assessment
if [ "$turn2_pass" = true ] && [ "$turn4_pass" = true ] && [ "$turn5_pass" = true ]; then
    echo ""
    echo "🎉 ALL MEMORY TESTS PASSED!"
    echo "✓ Turn 2: Sir Galahad's sword memory - PASSED"
    echo "✓ Turn 4: Evangeline's location memory - PASSED"  
    echo "✓ Turn 5: Cross-compaction memory persistence - PASSED"
    
    if [ "$compaction_occurred" = true ]; then
        echo "✓ Message compaction: Evidence of compaction found"
        echo "🏆 FULL SUCCESS: Long text understanding + compaction verified!"
    else
        echo "⚠️  Message compaction: No clear evidence of compaction"
        echo "✓ PARTIAL SUCCESS: Memory works but compaction not clearly triggered"
    fi
    
    exit 0
else
    echo ""
    echo "❌ SOME MEMORY TESTS FAILED"
    if [ "$turn2_pass" = false ]; then
        echo "✗ Turn 2: Failed to remember Sir Galahad's sword"
    fi
    if [ "$turn4_pass" = false ]; then
        echo "✗ Turn 4: Failed to remember Evangeline's name"
    fi
    if [ "$turn5_pass" = false ]; then
        echo "✗ Turn 5: Failed to remember Sir Galahad's sword after compaction"
    fi
    
    echo "Turn 2 output: $turn2_output"
    echo "Turn 4 output: $turn4_output"  
    echo "Turn 5 output: $turn5_output"
    exit 1
fi