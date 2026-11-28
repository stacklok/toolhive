// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package apple implements the ToolHive container runtime and image manager
// backed by Apple's macOS native container CLI (apple/container 1.0+).
package apple

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

//go:generate mockgen -destination=mocks/mock_command_runner.go -package=mocks -source=command.go CommandRunner,AttachedProcess

// CommandRunner abstracts CLI execution for Apple's container tooling so the
// runtime can be unit-tested on any OS without a live macOS VM daemon.
type CommandRunner interface {
	// LookPath searches PATH for an executable binary.
	LookPath(file string) (string, error)
	// Run executes a command to completion and returns its stdout bytes.
	// On non-zero exit, the returned error wraps *CommandError with captured stderr.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// RunStreaming executes a command while streaming its stdout and stderr to the provided writers.
	RunStreaming(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error
	// StartAttached starts an interactive command with attached stdin, stdout, and stderr pipes.
	StartAttached(ctx context.Context, name string, args ...string) (AttachedProcess, error)
}

// AttachedProcess represents a running child process with attached stdio streams.
type AttachedProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Kill() error
}

// CommandError captures stderr output from a failed CLI invocation.
// CLI flags and values are omitted from Error() so environment variables or
// container arguments containing secrets cannot leak into logs or errors.
type CommandError struct {
	Command string
	Args    []string
	Stderr  string
	Err     error
}

// Error implements error without printing CLI flags or argument values.
func (ce *CommandError) Error() string {
	label := commandSummary(ce.Command, ce.Args)
	trimmed := strings.TrimSpace(ce.Stderr)
	if trimmed != "" {
		return fmt.Sprintf("%s failed: %s: %v", label, trimmed, ce.Err)
	}
	return fmt.Sprintf("%s failed: %v", label, ce.Err)
}

// Unwrap returns the underlying execution error.
func (ce *CommandError) Unwrap() error {
	return ce.Err
}

type execCommandRunner struct{}

func (execCommandRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := buildAllowedCommand(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if runErr := cmd.Run(); runErr != nil {
		return stdoutBuf.Bytes(), &CommandError{
			Command: name,
			Args:    redactSensitiveArgs(args),
			Stderr:  stderrBuf.String(),
			Err:     runErr,
		}
	}
	return stdoutBuf.Bytes(), nil
}

func (execCommandRunner) RunStreaming(
	ctx context.Context,
	stdout, stderr io.Writer,
	name string,
	args ...string,
) error {
	cmd, err := buildAllowedCommand(ctx, name, args...)
	if err != nil {
		return err
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if runErr := cmd.Run(); runErr != nil {
		return &CommandError{
			Command: name,
			Args:    redactSensitiveArgs(args),
			Err:     runErr,
		}
	}
	return nil
}

func (execCommandRunner) StartAttached(
	ctx context.Context,
	name string,
	args ...string,
) (AttachedProcess, error) {
	cmd, err := buildAllowedCommand(ctx, name, args...)
	if err != nil {
		return nil, err
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if startErr := cmd.Start(); startErr != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, &CommandError{
			Command: name,
			Args:    redactSensitiveArgs(args),
			Err:     startErr,
		}
	}

	return &execAttachedProcess{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
	}, nil
}

func buildAllowedCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	switch name {
	case containerBinary:
		// #nosec G204 -- command name is the fixed constant containerBinary
		return exec.CommandContext(ctx, containerBinary, args...), nil
	case swVersBinary:
		// #nosec G204 -- command name is the fixed constant swVersBinary
		return exec.CommandContext(ctx, swVersBinary, args...), nil
	default:
		return nil, fmt.Errorf("unsupported command %q", name)
	}
}

func commandSummary(command string, args []string) string {
	if command == "" {
		return "command"
	}
	if command != containerBinary || len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return command
	}
	switch args[0] {
	case "image", "network", "system":
		if len(args) >= 2 && !strings.HasPrefix(args[1], "-") {
			return command + " " + args[0] + " " + args[1]
		}
	}
	return command + " " + args[0]
}

const redactedPlaceholder = "[REDACTED]"

func redactSensitiveArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	redacted := make([]string, len(args))
	afterDoubleDash := false
	seenImageAfterDoubleDash := false

	for idx := 0; idx < len(args); idx++ {
		arg := args[idx]
		if afterDoubleDash {
			if !seenImageAfterDoubleDash {
				redacted[idx] = arg
				seenImageAfterDoubleDash = true
			} else {
				redacted[idx] = redactedPlaceholder
			}
			continue
		}

		if arg == "--" {
			redacted[idx] = arg
			afterDoubleDash = true
			continue
		}

		if arg == "-e" || arg == "--env" {
			redacted[idx] = arg
			if idx+1 < len(args) {
				idx++
				redacted[idx] = redactEnvAssignment(args[idx])
			}
			continue
		}

		if strings.HasPrefix(arg, "-e=") {
			redacted[idx] = "-e=" + redactEnvAssignment(strings.TrimPrefix(arg, "-e="))
			continue
		}

		if strings.HasPrefix(arg, "--env=") {
			redacted[idx] = "--env=" + redactEnvAssignment(strings.TrimPrefix(arg, "--env="))
			continue
		}

		redacted[idx] = arg
	}

	return redacted
}

func redactEnvAssignment(spec string) string {
	key, _, hasValue := strings.Cut(spec, "=")
	if !hasValue {
		return spec
	}
	return key + "=" + redactedPlaceholder
}

type execAttachedProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func (proc *execAttachedProcess) Stdin() io.WriteCloser { return proc.stdin }
func (proc *execAttachedProcess) Stdout() io.ReadCloser { return proc.stdout }
func (proc *execAttachedProcess) Stderr() io.ReadCloser { return proc.stderr }
func (proc *execAttachedProcess) Wait() error           { return proc.cmd.Wait() }
func (proc *execAttachedProcess) Kill() error {
	if proc.cmd.Process == nil {
		return nil
	}
	return proc.cmd.Process.Kill()
}
