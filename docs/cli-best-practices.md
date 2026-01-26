# CLI Best Practices

This document describes best practices for adding and maintaining CLI commands in ToolHive. These guidelines ensure a consistent, user-friendly command-line experience across the entire application.

## Table of Contents

- [Core Principles](#core-principles)
- [Command Structure](#command-structure)
- [Command Design](#command-design)
- [Flags and Arguments](#flags-and-arguments)
- [Output and Formatting](#output-and-formatting)
- [Error Messages](#error-messages)
- [User Feedback](#user-feedback)
- [Testing CLI Commands](#testing-cli-commands)
- [Adding New Commands](#adding-new-commands)

## Core Principles

### 0. CLI as Thin Wrappers (Architecture)

**CRITICAL**: CLI commands must be thin wrappers around business logic in `pkg/` packages.

The CLI layer (`cmd/thv/app/`) is responsible **ONLY** for:
- Parsing flags and arguments
- Calling business logic from `pkg/` packages
- Formatting output (text/JSON)

All business logic must live in `pkg/` packages where it can be:
- Thoroughly unit tested
- Reused by other components (API, operator)
- Maintained independently of CLI concerns

```go
// ❌ Bad - Business logic in CLI
func listCmdFunc(cmd *cobra.Command, args []string) error {
    // Complex container queries, filtering, transformation...
    // 100+ lines of business logic here
}

// ✅ Good - CLI delegates to pkg/
func listCmdFunc(cmd *cobra.Command, args []string) error {
    ctx := cmd.Context()

    manager, err := workloads.NewManager(ctx)
    if err != nil {
        return fmt.Errorf("failed to create workload manager: %w", err)
    }

    workloadList, err := manager.ListWorkloads(ctx, listAll, listLabelFilter...)
    if err != nil {
        return fmt.Errorf("failed to list workloads: %w", err)
    }

    // CLI only handles formatting
    switch listFormat {
    case FormatJSON:
        return printJSONOutput(workloadList)
    default:
        printTextOutput(workloadList)
        return nil
    }
}
```

**Testing implication**: Test business logic with unit tests in `pkg/`, test CLI with E2E tests. See [Testing CLI Commands](#testing-cli-commands) section.

### 1. Silent Success
Commands should be quiet on success. Users should only see output when:
- Something requires their attention
- They explicitly request verbose output with `--debug`
- The operation takes more than 2-3 seconds (show progress)

```bash
# Good - silent success
$ thv run fetch

# Avoid - verbose success messages
$ thv run fetch
INFO: Checking container runtime...
INFO: Container runtime found...
Server 'fetch' is now running!
```

### 2. Consistency Across Commands
- Use the same flag names for similar functionality (e.g., `--format`, `--all`, `--group`)
- Follow established patterns for output formatting
- Maintain consistent command naming conventions

### 3. User-Centric Error Messages
- Provide actionable error messages with hints
- Guide users to relevant commands or documentation
- Never expose internal implementation details in errors

### 4. Progressive Disclosure
- Show minimal information by default
- Provide flags for more detailed output (`--debug`, `--format json`)
- Use `list` vs `status` pattern: list shows summary, status shows details

## Command Structure

### Basic Command Template

```go
var myCmd = &cobra.Command{
    Use:   "command-name [flags] REQUIRED_ARG [OPTIONAL_ARG]",
    Short: "Brief one-line description",
    Long: `Detailed description explaining:
- What the command does
- When to use it
- How it relates to other commands

Examples:
  # Common use case with explanation
  thv command-name arg1

  # Advanced use case
  thv command-name arg1 --flag value`,
    Args:              validateArgs,
    RunE:              commandFunc,
    ValidArgsFunction: completeArgs, // For shell completion
}
```

### Command Organization

Commands are organized in `cmd/thv/app/`:
- One file per command (e.g., `list.go`, `run.go`, `status.go`)
- Group related flags and validation logic with the command
- Register commands in `commands.go`

Reference: `cmd/thv/app/list.go`, `cmd/thv/app/run.go`

## Command Design

### Naming Conventions

#### Command Names
- Use verbs for actions: `run`, `stop`, `list`, `remove`
- Keep names short and memorable
- Avoid abbreviations and acronyms for the command name, reserve for aliases
  for situations where they are likely to be universally understood.
- Provide common aliases: `ls` for `list`, `rm` for `remove`

```go
var listCmd = &cobra.Command{
    Use:     "list",
    Aliases: []string{"ls"},
    Short:   "List running MCP servers",
    ...
}
```

#### Flag Names
- Use lowercase with hyphens: `--format`, `--remote-auth`
- Common flags should use consistent names:
  - `--all`: Show all items (including stopped/hidden)
  - `--format`: Output format (json/text)
  - `--group`: Filter/target by group
  - `--debug`: Enable debug logging
- Provide short flags sparingly, only for frequently used options

### Help Text

#### Short Description
- One line, under 80 characters
- Start with a verb
- Don't end with a period

```go
Short: "List running MCP servers",
```

#### Long Description
Structure the long description as:
1. Detailed explanation of what the command does
2. When and why to use it
3. At least 2-3 practical examples with explanations

```go
Long: `List all MCP servers managed by ToolHive, including their status and configuration.

The list command shows running servers by default. Use --all to include stopped servers.

Examples:
  # List running MCP servers
  thv list

  # List all servers including stopped ones
  thv list --all

  # List servers in JSON format
  thv list --format json`,
```

### Arguments and Validation

#### Argument Specifications

Use Cobra's built-in validators when possible:
```go
Args: cobra.ExactArgs(1),     // Exactly one argument
Args: cobra.MinimumNArgs(1),  // At least one argument
Args: cobra.MaximumNArgs(2),  // At most two arguments
Args: cobra.RangeArgs(1, 3),  // Between 1 and 3 arguments
```

For custom validation:
```go
Args: func(cmd *cobra.Command, args []string) error {
    if len(args) < 1 {
        return fmt.Errorf("requires at least one argument")
    }
    // Additional validation...
    return nil
},
```

#### PreRunE Validation

Use `PreRunE` for flag validation that should happen before the command runs:

```go
func init() {
    myCmd.PreRunE = chainPreRunE(
        validateGroupFlag(),
        ValidateFormat(&formatVar, FormatJSON, FormatText),
        validateCustomLogic,
    )
}

func validateCustomLogic(cmd *cobra.Command, args []string) error {
    // Validation logic here
    return nil
}
```

Reference: `cmd/thv/app/flag_helpers.go` (chainPreRunE pattern)

## Flags and Arguments

### Common Flag Patterns

#### Format Flag
Use the helper function for consistent format flags:

```go
var outputFormat string

func init() {
    AddFormatFlag(myCmd, &outputFormat, FormatJSON, FormatText)
    myCmd.PreRunE = ValidateFormat(&outputFormat, FormatJSON, FormatText)
}
```

Reference: `cmd/thv/app/flag_helpers.go`

#### All Flag
For commands that can operate on all items:

```go
var showAll bool

func init() {
    AddAllFlag(myCmd, &showAll, false, "Show all items")
}
```

#### Group Flag
For filtering by group:

```go
var groupName string

func init() {
    AddGroupFlag(myCmd, &groupName, false)
    myCmd.PreRunE = validateGroupFlag()
}
```

### Flag Organization

```go
var (
    // Group related flags together
    listAll         bool
    listFormat      string
    listLabelFilter []string
    listGroupFilter string
)

func init() {
    // Add flags in logical order
    AddAllFlag(listCmd, &listAll, true, "Show all workloads")
    AddFormatFlag(listCmd, &listFormat, FormatJSON, FormatText, "mcpservers")
    listCmd.Flags().StringArrayVarP(&listLabelFilter, "label", "l", []string{},
        "Filter workloads by labels (format: key=value)")
    AddGroupFlag(listCmd, &listGroupFilter, false)
}
```

### Mutually Exclusive Flags

Use Cobra's built-in mechanism:

```go
func init() {
    myCmd.Flags().BoolVar(&flagA, "flag-a", false, "Description")
    myCmd.Flags().BoolVar(&flagB, "flag-b", false, "Description")

    myCmd.MarkFlagsMutuallyExclusive("flag-a", "flag-b")
}
```

### Hidden Flags

Hide flags that are for internal use or advanced scenarios:

```go
func init() {
    myCmd.Flags().StringVar(&internalFlag, "internal-flag", "", "Internal use")
    if err := myCmd.Flags().MarkHidden("internal-flag"); err != nil {
        logger.Warnf("Error hiding flag: %v", err)
    }
}
```

## Output and Formatting

### User-Facing Output vs Logs

Distinguish between:
- **User-facing output**: Information the user requested (use `fmt.Println`, `fmt.Printf`)
- **Operational logs**: Diagnostic information (use `logger.Debugf`, `logger.Warnf`, etc.)

```go
// Good - user-facing output
fmt.Printf("Workload %s removed successfully\n", name)

// Good - operational log
logger.Debugf("Attempting to connect to runtime at %s", socketPath)

// Bad - don't use logger for user-facing output
logger.Infof("Workload %s removed successfully", name)
```

### Format Support

Commands that output data should support both text and JSON formats:

```go
func commandFunc(cmd *cobra.Command, args []string) error {
    // ... get data ...

    switch format {
    case FormatJSON:
        return printJSONOutput(data)
    default:
        printTextOutput(data)
        return nil
    }
}
```

#### JSON Output

```go
func printJSONOutput(data interface{}) error {
    // Ensure non-nil slices to avoid null in JSON
    if data == nil {
        data = []YourType{}
    }

    // Sort for deterministic output
    sortData(data)

    jsonData, err := json.MarshalIndent(data, "", "  ")
    if err != nil {
        return fmt.Errorf("failed to marshal JSON: %w", err)
    }

    fmt.Println(string(jsonData))
    return nil
}
```

#### Text Output

Use `text/tabwriter` for aligned columns:

```go
func printTextOutput(workloads []Workload) {
    w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)

    // Print header
    if _, err := fmt.Fprintln(w, "NAME\tSTATUS\tURL\tPORT"); err != nil {
        logger.Warnf("Failed to write header: %v", err)
        return
    }

    // Print rows
    for _, item := range workloads {
        if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%d\n",
            item.Name, item.Status, item.URL, item.Port); err != nil {
            logger.Debugf("Failed to write row: %v", err)
        }
    }

    // Flush output
    if err := w.Flush(); err != nil {
        logger.Errorf("Failed to flush output: %v", err)
    }
}
```

Reference: `cmd/thv/app/list.go` (printTextOutput, printJSONOutput)

### Empty State Messages

Handle empty results gracefully:

```go
if len(items) == 0 {
    if filterApplied {
        fmt.Printf("No items found matching filter '%s'\n", filter)
    } else {
        fmt.Println("No items found")
    }
    return nil
}
```

### Visual Indicators

Use Unicode symbols sparingly and consistently:
- `⚠️` for warnings or issues requiring attention
- Use color only when writing to a TTY (check with `isatty` package)

```go
status := string(workload.Status)
if workload.Status == runtime.WorkloadStatusUnauthenticated {
    status = "⚠️  " + status
}
```

## Error Messages

### Constructing Error Messages

Follow the guidelines in `docs/error-handling.md`:

```go
// Good - descriptive with context
return fmt.Errorf("failed to start workload %s: %w", name, err)

// Good - actionable error with hint
return fmt.Errorf("group '%s' does not exist. Hint: use 'thv group list' to see available groups", groupName)

// Avoid - vague error
return fmt.Errorf("operation failed")

// Avoid - exposing internal details
return fmt.Errorf("database query failed: SELECT * FROM workloads WHERE id = %d", id)
```

### Error Message Guidelines

1. **Be specific**: Explain what operation failed
2. **Provide context**: Include relevant identifiers (names, IDs)
3. **Be actionable**: Suggest how to fix the issue
4. **Guide users**: Reference relevant commands or documentation
5. **Preserve error chains**: Use `%w` to wrap errors

### Validation Error Messages

```go
func validateArgs(cmd *cobra.Command, args []string) error {
    if len(args) < 1 {
        return fmt.Errorf(
            "at least one workload name must be provided. " +
            "Hint: use 'thv list' to see available workloads")
    }

    if hasFlag && len(args) > 0 {
        return fmt.Errorf(
            "no arguments should be provided when --all flag is set. " +
            "Hint: remove the workload names or remove the flag")
    }

    return nil
}
```

Reference: `cmd/thv/app/rm.go` (validateRmArgs)

### Common Error Patterns

```go
// Not found errors
if errors.Is(err, runtime.ErrWorkloadNotFound) {
    return fmt.Errorf("workload '%s' not found. Hint: use 'thv list' to see running workloads", name)
}

// Permission errors
if errors.Is(err, os.ErrPermission) {
    return fmt.Errorf("permission denied accessing %s. Hint: check file permissions or run with appropriate privileges", path)
}

// Configuration errors
if err := config.Load(); err != nil {
    return fmt.Errorf("failed to load configuration: %w. Hint: run 'thv config init' to create a new configuration", err)
}
```

## User Feedback

### Progress Indication

Show progress for long-running operations (> 2-3 seconds):

```go
// For operations like image pulls
fmt.Printf("Pulling image %s...\n", imageName)
logger.Infof("Pulling image %s...", imageName)

// For operations with known progress
fmt.Printf("Processing %d of %d items...\n", current, total)
```

### Confirmation Messages

For destructive operations, provide clear confirmation:

```go
// Single item
fmt.Printf("Workload %s removed successfully\n", name)

// Multiple items
if len(names) == 1 {
    fmt.Printf("Workload %s removed successfully\n", names[0])
} else {
    fmt.Printf("Workloads %s removed successfully\n", strings.Join(names, ", "))
}

// Bulk operations
fmt.Printf("Successfully removed %d workload(s) from group '%s'\n", count, groupName)
```

Reference: `cmd/thv/app/rm.go` (confirmation messages)

### Status Updates

For operations with multiple steps:

```go
// Use DEBUG logging for steps
logger.Debugf("Checking container runtime...")
logger.Debugf("Starting container...")
logger.Debugf("Waiting for health check...")

// Only show to user if they use --debug flag
```

## Shell Completion

### Auto-completion Support

Provide completion functions for arguments:

```go
var myCmd = &cobra.Command{
    Use:               "command [arg]",
    ValidArgsFunction: completeMyArgs,
    ...
}

func completeMyArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
    // Only complete the first argument
    if len(args) > 0 {
        return nil, cobra.ShellCompDirectiveNoFileComp
    }

    // Get available options
    options, err := getAvailableOptions(cmd.Context())
    if err != nil {
        return nil, cobra.ShellCompDirectiveError
    }

    return options, cobra.ShellCompDirectiveNoFileComp
}
```

Reference: `cmd/thv/app/common.go` (completeMCPServerNames)

### Completion for Common Patterns

```go
// Workload names
ValidArgsFunction: completeMCPServerNames,

// File paths
ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
    return nil, cobra.ShellCompDirectiveDefault // Allows file completion
},

// No completion
ValidArgsFunction: cobra.NoFileCompletions,
```

## Testing CLI Commands

### Testing Philosophy

**CLI commands should be thin wrappers around business logic in `pkg/`.** The CLI layer (`cmd/thv/app/`) is responsible only for:
- Parsing flags and arguments
- Formatting output (text/JSON)
- Calling business logic in `pkg/` packages

**Minimize unit tests for CLI code. Instead, rely heavily on end-to-end (E2E) tests.**

### Why E2E Tests Over Unit Tests?

1. **CLI is a thin layer**: Most CLI code is glue code that calls into `pkg/`. Unit testing this adds little value.
2. **E2E tests verify real behavior**: They test the actual user experience with the compiled binary.
3. **Better coverage with less code**: One E2E test exercises the entire stack (CLI → pkg → runtime).
4. **Catch integration issues**: E2E tests catch problems that unit tests miss (flag parsing, output formatting, error propagation).

### Where to Put Business Logic

```go
// ❌ Bad - Business logic in CLI command
func listCmdFunc(cmd *cobra.Command, args []string) error {
    // Complex business logic here
    containers, err := runtime.ListContainers()
    if err != nil {
        return err
    }

    var workloads []Workload
    for _, c := range containers {
        // Complex transformation logic
        workload := transformContainerToWorkload(c)
        workloads = append(workloads, workload)
    }

    // More complex filtering and processing...

    printOutput(workloads)
    return nil
}

// ✅ Good - Business logic in pkg/, CLI is thin
func listCmdFunc(cmd *cobra.Command, args []string) error {
    ctx := cmd.Context()

    // Call business logic from pkg/
    manager, err := workloads.NewManager(ctx)
    if err != nil {
        return fmt.Errorf("failed to create workload manager: %w", err)
    }

    workloadList, err := manager.ListWorkloads(ctx, listAll, listLabelFilter...)
    if err != nil {
        return fmt.Errorf("failed to list workloads: %w", err)
    }

    // CLI only handles output formatting
    switch listFormat {
    case FormatJSON:
        return printJSONOutput(workloadList)
    default:
        printTextOutput(workloadList)
        return nil
    }
}
```

### When to Use Unit Tests in CLI

Use unit tests sparingly for CLI code, only for:

1. **Output formatting logic** - Test JSON/text output functions
2. **Flag validation** - Test custom argument validation functions
3. **Helper functions** - Test utilities like `chainPreRunE` or format validators

```go
// Example: Testing output formatting
func TestPrintJSONOutput(t *testing.T) {
    data := []core.Workload{{Name: "test", Status: "running"}}

    // Capture stdout
    oldStdout := os.Stdout
    r, w, _ := os.Pipe()
    os.Stdout = w

    err := printJSONOutput(data)

    w.Close()
    os.Stdout = oldStdout

    var buf bytes.Buffer
    io.Copy(&buf, r)

    // Verify valid JSON
    var result []core.Workload
    if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
        t.Errorf("invalid JSON output: %v", err)
    }

    // Verify content
    if len(result) != 1 || result[0].Name != "test" {
        t.Errorf("unexpected output: %v", result)
    }
}
```

Reference: `cmd/thv/app/common_test.go`, `cmd/thv/app/status_test.go`

### E2E Tests (Primary Testing Strategy)

End-to-end tests are in `test/e2e/`. These tests use the compiled binary and test complete user workflows:

```go
var _ = Describe("CLI E2E", func() {
    It("should run and list workloads", func() {
        // Run command - tests full stack
        cmd := exec.Command("thv", "run", "test-workload")
        err := cmd.Run()
        Expect(err).ToNot(HaveOccurred())

        // List command - tests output formatting
        cmd = exec.Command("thv", "list", "--format", "json")
        output, err := cmd.Output()
        Expect(err).ToNot(HaveOccurred())

        // Verify JSON output
        var workloads []Workload
        err = json.Unmarshal(output, &workloads)
        Expect(err).ToNot(HaveOccurred())
        Expect(workloads).To(HaveLen(1))
        Expect(workloads[0].Name).To(Equal("test-workload"))
    })

    It("should handle errors gracefully", func() {
        // Test error handling
        cmd := exec.Command("thv", "run", "nonexistent-workload")
        output, err := cmd.CombinedOutput()

        Expect(err).To(HaveOccurred())
        Expect(string(output)).To(ContainSubstring("not found"))
        Expect(string(output)).To(ContainSubstring("Hint:"))
    })
})
```

### Testing Business Logic in pkg/

Put business logic in `pkg/` packages and test it thoroughly with unit tests:

```go
// pkg/workloads/manager_test.go
func TestListWorkloads(t *testing.T) {
    ctx := context.Background()
    manager := NewManager(mockRuntime)

    workloads, err := manager.ListWorkloads(ctx, false)

    if err != nil {
        t.Errorf("unexpected error: %v", err)
    }

    if len(workloads) != 2 {
        t.Errorf("expected 2 workloads, got %d", len(workloads))
    }
}
```

### Testing Checklist

When adding a new CLI command:

- [ ] **Business logic is in `pkg/` packages** (not in `cmd/thv/app/`)
- [ ] **Unit tests exist for `pkg/` business logic** (thorough coverage)
- [ ] **E2E tests cover the CLI command** (primary verification)
- [ ] **Minimal unit tests for CLI-specific code** (output formatting, validation)
- [ ] **E2E tests verify**:
  - [ ] Successful command execution
  - [ ] Error handling with helpful messages
  - [ ] Both `--format json` and `--format text` output
  - [ ] Flag combinations and edge cases

## Adding New Commands

### Step-by-Step Process

1. **Create the command file**
   ```bash
   touch cmd/thv/app/mycommand.go
   ```

2. **Add SPDX header**
   ```go
   // SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
   // SPDX-License-Identifier: Apache-2.0
   ```

3. **Define the command**
   ```go
   var myCmd = &cobra.Command{
       Use:   "mycommand [args]",
       Short: "Brief description",
       Long:  `Detailed description with examples`,
       Args:  validateArgs,
       RunE:  myCommandFunc,
   }
   ```

4. **Add flags in init()**
   ```go
   func init() {
       myCmd.Flags().StringVar(&myFlag, "my-flag", "", "Description")
       myCmd.PreRunE = validateFlags
   }
   ```

5. **Implement the command function**
   ```go
   func myCommandFunc(cmd *cobra.Command, args []string) error {
       ctx := cmd.Context()

       // Command implementation

       return nil
   }
   ```

6. **Register in commands.go**
   ```go
   func NewRootCmd() *cobra.Command {
       // ...
       rootCmd.AddCommand(myCmd)
       // ...
   }
   ```

7. **Keep business logic in pkg/**
   ```go
   // Move complex logic to pkg/ packages
   // CLI should only parse flags, call pkg/ functions, and format output
   ```

8. **Update CLI documentation**
   ```bash
   task docs
   ```

9. **Write E2E tests** (primary testing)
   ```bash
   # Add tests to test/e2e/
   # Test the compiled binary with real workflows
   ```

10. **Write minimal unit tests** (only for output formatting/validation)
    ```go
    // Only if testing output formatting or flag validation helpers
    // Most testing should be E2E
    ```

### Checklist for New Commands

- [ ] Command has clear, descriptive name
- [ ] Short description is concise (< 80 chars)
- [ ] Long description includes examples
- [ ] Flags use consistent naming
- [ ] Validation is in PreRunE
- [ ] Supports --format flag (if outputting data)
- [ ] Silent on success
- [ ] Error messages are actionable
- [ ] Shell completion is provided
- [ ] **Business logic is in `pkg/` packages** (not in CLI layer)
- [ ] **E2E tests are written** (primary verification)
- [ ] Unit tests for output formatting/validation (if needed)
- [ ] Documentation is updated (task docs)

## Related Documentation

- [Logging Practices](logging.md) - Logging levels and when to use them
- [Error Handling](error-handling.md) - Error construction and handling patterns
- [CLAUDE.md](../CLAUDE.md) - Build commands and project overview
- [CONTRIBUTING.md](../CONTRIBUTING.md) - Commit message guidelines and PR process
