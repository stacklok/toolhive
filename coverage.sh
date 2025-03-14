#!/bin/bash
set -e

# Create a .tools directory for local tools (should be in .gitignore)
TOOLS_DIR=".tools"
mkdir -p "$TOOLS_DIR"

# Install grcov locally if not already installed
GRCOV_PATH="$TOOLS_DIR/grcov"
if [ ! -f "$GRCOV_PATH" ]; then
    echo "Installing grcov locally..."
    cargo install grcov --root "$TOOLS_DIR" --no-track
    # The binary will be in $TOOLS_DIR/bin/grcov, move it to $TOOLS_DIR/grcov for simplicity
    mv "$TOOLS_DIR/bin/grcov" "$GRCOV_PATH"
    rm -rf "$TOOLS_DIR/bin" "$TOOLS_DIR/.crates.toml" "$TOOLS_DIR/.crates2.json"
fi

# Create the directory for coverage data
mkdir -p target/coverage

# Clean previous coverage data
rm -rf target/coverage/*

# Set environment variables for coverage
export CARGO_INCREMENTAL=0
export RUSTFLAGS="-Cinstrument-coverage"
export LLVM_PROFILE_FILE="target/coverage/%p-%m.profraw"

# Run the tests
echo "Running tests with coverage instrumentation..."
cargo test --lib --no-fail-fast

# Generate the coverage report
echo "Generating coverage report..."
"$GRCOV_PATH" . \
    --binary-path ./target/debug/ \
    -s . \
    -t html \
    --branch \
    --ignore-not-existing \
    --ignore "/*" \
    --ignore "target/*" \
    --ignore "tests/*" \
    --llvm-path /usr/bin \
    -o target/coverage/html

echo "Coverage report generated at target/coverage/html/index.html"

# Generate a summary report
echo "Generating summary report..."
"$GRCOV_PATH" . \
    --binary-path ./target/debug/ \
    -s . \
    -t markdown \
    --branch \
    --ignore-not-existing \
    --ignore "/*" \
    --ignore "target/*" \
    --ignore "tests/*" \
    --llvm-path /usr/bin \
    -o target/coverage/summary.md

echo "Summary report generated at target/coverage/summary.md"
echo "Summary:"
cat target/coverage/summary.md

# Add .tools to .gitignore if it's not already there
if ! grep -q "^\.tools$" .gitignore 2>/dev/null; then
    echo "Adding .tools to .gitignore"
    echo ".tools" >> .gitignore
fi