package app

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// Define the `runtime` parent command
var runtimeCmd = &cobra.Command{
	Use:   "runtime",
	Short: "Commands related to the container runtime",
}

// Define the `runtime check` subcommand
var runtimeCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Ping the container runtime",
	Long:  "Ensure the container runtime is responsive.",
	Args:  cobra.NoArgs, // no args allowed
	RunE:  runtimeCheckCmdFunc,
}

var runtimeCheckTimeout int

func init() {
	rootCmd.AddCommand(runtimeCmd)
	runtimeCmd.AddCommand(runtimeCheckCmd)
	runtimeCheckCmd.Flags().IntVar(&runtimeCheckTimeout, "timeout", 30,
		"Timeout in seconds for runtime checks (default: 30 seconds)")
}

func runtimeCheckCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	// Create runtime with timeout
	createCtx, cancelCreate := context.WithTimeout(ctx, time.Duration(runtimeCheckTimeout)*time.Second)
	defer cancelCreate()
	rt, err := createWithTimeout(createCtx)
	if err != nil {
		if createCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("creating container runtime timed out after %d seconds", runtimeCheckTimeout)
		}
		return fmt.Errorf("failed to create container runtime: %w", err)
	}

	// Ping with separate timeout
	pingCtx, cancelPing := context.WithTimeout(ctx, time.Duration(runtimeCheckTimeout)*time.Second)
	defer cancelPing()
	if err := pingRuntime(pingCtx, rt); err != nil {
		if pingCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("runtime ping timed out after %d seconds", runtimeCheckTimeout)
		}
		return fmt.Errorf("runtime ping failed: %w", err)
	}

	fmt.Println("Container runtime is responsive")
	return nil
}

func createWithTimeout(ctx context.Context) (runtime.Runtime, error) {
	done := make(chan struct {
		rt  runtime.Runtime
		err error
	}, 1)
	go func() {
		rt, err := container.NewFactory(logger).Create(ctx)
		done <- struct {
			rt  runtime.Runtime
			err error
		}{rt, err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-done:
		return res.rt, res.err
	}
}

func pingRuntime(ctx context.Context, rt runtime.Runtime) error {
	done := make(chan error, 1)
	go func() {
		done <- rt.IsRunning(ctx)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}
