# Test Suite

This directory contains the test suite for klein, designed to validate the AI agent's capabilities across different scenarios and backends.

## Overview

The test suite runs klein with specific prompts and validates that the AI correctly completes multi-step tasks while respecting filesystem security constraints.

## Directory Structure

```
testsuite/
├── README.md              # This file
├── runner.sh              # Main test runner script
├── matrix_runner.sh       # Run tests across multiple backends
├── backends/              # Backend configuration files
│   ├── gemini.json
│   ├── ollama.json
│   └── openai.json
├── testcases/             # Individual test cases
│   ├── code_scenario/     # Simple code generation test
│   ├── fibonacci_test/    # Multi-step Fibonacci implementation
│   ├── memory_state/      # Memory and state management test
│   └── research_scenario/ # Web research capabilities
└── results/               # Test execution results
```

## Key Features

### Working Directory Isolation
- Each test runs in its own testcase directory (e.g., `/path/to/testsuite/testcases/fibonacci_test/`)
- AI file operations are **restricted** to the testcase directory only
- Prevents contamination of the project root with test artifacts

### Filesystem Security
- Uses enhanced `FileSystemToolManager.resolvePath()` method
- Rejects absolute paths outside working directory
- AI cannot escape testcase boundaries even with absolute paths

### Clean Test Environment
- **Pre-cleanup**: `git clean -fd` removes all non-git managed files AND directories before each test
- **Post-cleanup**: `git clean -fd` removes generated artifacts after successful tests
- **Preserves**: git-tracked files (`prompt.txt`, `check.sh`)
- **Failed tests**: Leave artifacts for debugging
- **Test Independence**: Each test starts with a completely clean slate - no false positives from previous test artifacts

## Usage

### Single Test Execution
```bash
# Build the binary first
go build -o output/klein ./klein

# Run a specific test with default backend (ollama)
CLI=output/klein ./testsuite/runner.sh fibonacci_test

# Run with specific backend
CLI=output/klein ./testsuite/runner.sh fibonacci_test gemini
CLI=output/klein ./testsuite/runner.sh code_scenario openai
```

### Matrix Testing (All Backends)
```bash
# Run all testcases against all backends
CLI=output/klein ./testsuite/matrix_runner.sh
```

### List Available Options
```bash
# Show available testcases and backends
CLI=output/klein ./testsuite/runner.sh
```

## Test Cases

### fibonacci_test
**Purpose**: Multi-step development workflow validation
**Steps**:
1. Create basic Fibonacci generator (`main.go`)
2. Add command-line argument support
3. Create comprehensive unit tests (`main_test.go`)

**Validation**: Each step is individually validated with compilation and execution tests

### code_scenario  
**Purpose**: Simple code generation
**Task**: Create a Go function that adds two integers
**Validation**: Checks for proper function signature, return statement, and int types

### memory_state
**Purpose**: Conversation memory and state management
**Task**: Tests how AI maintains context across interactions

### research_scenario
**Purpose**: Web research capabilities  
**Task**: Information gathering and web tool usage

## Backend Configurations

### ollama.json
- **Model**: gpt-oss:latest
- **Features**: Native tool calling with thinking
- **Token Limit**: 2000 tokens

### openai.json
- **Model**: gpt-4o
- **Features**: Native tool calling, structured output, vision
- **Token Limit**: 2000 tokens

### gemini.json
- **Model**: gemini-2.5-flash-lite
- **Features**: Native schema, structured output
- **Token Limit**: 2000 tokens

## Security Model

### Directory Restriction
```bash
# AI working directory is restricted to testcase directory
--workdir /path/to/testsuite/testcases/fibonacci_test/
```

### Path Resolution Security
- **Relative paths**: Resolved within working directory
- **Absolute paths**: Validated to be within working directory or rejected
- **Escape attempts**: `../../../file` blocked by path validation

### Example Security Enforcement
```
✅ Allowed: main.go → /path/to/fibonacci_test/main.go
✅ Allowed: ./utils/helper.go → /path/to/fibonacci_test/utils/helper.go  
❌ Rejected: /Users/.../klein/add.go (outside working directory)
❌ Rejected: ../../../add.go (would escape working directory)
```

## Development Workflow

### Adding New Test Cases
1. Create directory: `testsuite/testcases/my_test/`
2. Add `prompt.txt`: Contains the task description
3. Add `check.sh`: Validation script that checks AI output
4. Make `check.sh` executable: `chmod +x check.sh`

### Test Case Structure
```
testcases/my_test/
├── prompt.txt    # Task description for AI (git-tracked)
└── check.sh      # Validation script (git-tracked)
```

### Validation Script Format
```bash
#!/bin/bash
# Arguments: $1 = output file, $2 = error file
output_file="$1"
error_file="$2"

# Your validation logic here
if grep -q "expected_pattern" "$output_file"; then
    echo "✓ Test passed"
    exit 0
else
    echo "✗ Test failed"
    exit 1
fi
```

## Implementation Details

### Working Directory Management
- **No `os.Chdir()`**: Main process stays in project root
- **Tool-only restriction**: Only filesystem tools are restricted
- **Path validation**: `FileSystemToolManager` enforces boundaries

### Multi-turn Execution
- **File mode**: `-f` flag disables session memory between turns
- **Clean slate**: Each prompt executes independently
- **Step validation**: Each step can be individually verified

### Error Handling
- **Compilation errors**: Captured and reported in check scripts
- **Runtime errors**: Validated through execution tests
- **Tool errors**: Filesystem access violations logged and rejected

## Troubleshooting

### Common Issues
1. **"file access denied"**: AI trying to escape working directory
2. **"binary not found"**: Run `go build` first to create `output/klein`
3. **Test timeout**: Some backends can be slow on complex tasks
4. **Permission errors**: Ensure `check.sh` scripts are executable

### Debug Mode
Add debug output to see what's happening:
```bash
# Enable verbose logging
export DEBUG=1
CLI=output/klein ./testsuite/runner.sh fibonacci_test gemini
```

### Manual Testing
Test components individually:
```bash
# Test binary directly
./output/klein --workdir /path/to/testcase "your prompt"

# Test validation script
cd testsuite/testcases/fibonacci_test
./check.sh output_file error_file
```

This test suite ensures that klein works correctly across different LLM backends while maintaining security and isolation.