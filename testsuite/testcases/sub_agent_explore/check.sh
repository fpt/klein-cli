#!/bin/bash

# Test sub-agent exploration: verifies that spawn_agent was used and that
# the final response contains the correct values extracted from each file.
#
# Arguments: $1 = output file (full klein log), $2 = error file

output_file="$1"
error_file="$2"

echo "Testing sub-agent exploration..."

# ─── 1. Verify spawn_agent was called ────────────────────────────────────────

echo ""
echo "Checking spawn_agent usage..."

if grep -q "spawn_agent" "$output_file" 2>/dev/null; then
    echo "✓ spawn_agent tool was invoked"
else
    echo "✗ spawn_agent was NOT called — agent read files directly instead of spawning sub-agents"
    exit 1
fi

# Verify at least two sub-agent invocations (one per file at minimum)
spawn_count=$(grep -c "sub-agent" "$output_file" 2>/dev/null || echo 0)
if [ "$spawn_count" -ge 2 ]; then
    echo "✓ Multiple sub-agent invocations detected ($spawn_count references)"
else
    echo "✗ Expected multiple sub-agent invocations, found: $spawn_count"
    exit 1
fi

# ─── 2. Verify AppVersion from config.go ─────────────────────────────────────

echo ""
echo "Checking extracted values..."

if grep -q "3\.7\.2" "$output_file" 2>/dev/null; then
    echo "✓ AppVersion 3.7.2 found in output"
else
    echo "✗ AppVersion '3.7.2' not found in output"
    echo "Last lines of output:"
    tail -20 "$output_file"
    exit 1
fi

# ─── 3. Verify ListenPort from server.go ─────────────────────────────────────

if grep -q "9042" "$output_file" 2>/dev/null; then
    echo "✓ ListenPort 9042 found in output"
else
    echo "✗ ListenPort '9042' not found in output"
    echo "Last lines of output:"
    tail -20 "$output_file"
    exit 1
fi

# ─── 4. Verify User struct fields from users.go ──────────────────────────────

# At least two of the four fields must be mentioned
fields_found=0
for field in "Username" "Email" "Role" "ID"; do
    if grep -q "$field" "$output_file" 2>/dev/null; then
        echo "✓ User field '$field' found in output"
        fields_found=$((fields_found + 1))
    fi
done

if [ "$fields_found" -lt 2 ]; then
    echo "✗ Expected at least 2 User struct fields in output, found $fields_found"
    echo "Last lines of output:"
    tail -20 "$output_file"
    exit 1
fi
echo "✓ $fields_found/4 User struct fields reported"

# ─── Pass ─────────────────────────────────────────────────────────────────────

echo ""
echo "🤖 Sub-Agent Explore Assessment:"
echo "================================="
echo "✓ spawn_agent was used for file exploration"
echo "✓ AppVersion (3.7.2) correctly extracted from config.go"
echo "✓ ListenPort (9042) correctly extracted from server.go"
echo "✓ User struct fields correctly extracted from users.go"
echo ""
echo "🏆 PASS: Sub-agent exploration successful"
exit 0
