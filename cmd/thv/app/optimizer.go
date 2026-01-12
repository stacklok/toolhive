package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/optimizer/db"
	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	"github.com/stacklok/toolhive/pkg/optimizer/ingestion"
)

var (
	optimizerDBPath       string
	optimizerModelPath    string
	optimizerRuntimeMode  string
	optimizerPollInterval int
)

func newOptimizerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "optimizer",
		Short: "Semantic tool discovery and ingestion for MCP servers",
		Long: `The optimizer command provides semantic tool discovery by ingesting MCP workloads,
generating embeddings for tools, and enabling semantic search capabilities.

This is a standalone command for testing the optimizer functionality. In production,
the optimizer can be integrated into the vMCP process as a goroutine.`,
	}

	// Add subcommands
	cmd.AddCommand(newOptimizerIngestCommand())
	cmd.AddCommand(newOptimizerQueryCommand())
	cmd.AddCommand(newOptimizerStatusCommand())

	// Add persistent flags
	cmd.PersistentFlags().StringVar(&optimizerDBPath, "db-path", "", "Path to SQLite database (default: ~/.toolhive/optimizer.db)")
	cmd.PersistentFlags().StringVar(&optimizerModelPath, "model-path", "", "Path to ONNX embedding model (required)")
	cmd.PersistentFlags().StringVar(&optimizerRuntimeMode, "runtime-mode", "docker", "Runtime mode: docker or k8s")

	return cmd
}

func newOptimizerIngestCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Ingest MCP workloads and generate embeddings",
		Long: `Discovers MCP workloads from ToolHive (via Docker or Kubernetes),
connects to each workload to list tools, generates semantic embeddings,
and stores them in a SQLite database for semantic search.`,
		RunE: optimizerIngestCmdFunc,
	}

	cmd.Flags().IntVar(&optimizerPollInterval, "poll-interval", 0, "Poll interval in seconds (0 = run once)")

	return cmd
}

func newOptimizerQueryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "query [search text]",
		Short: "Query tools using semantic search",
		Long:  `Searches for tools using semantic similarity to the provided query text.`,
		Args:  cobra.MinimumNArgs(1),
		RunE:  optimizerQueryCmdFunc,
	}

	cmd.Flags().Int("limit", 10, "Maximum number of results to return")

	return cmd
}

func newOptimizerStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show optimizer status and statistics",
		Long:  `Displays the current status of the optimizer database, including server and tool counts.`,
		RunE:  optimizerStatusCmdFunc,
	}
}

func optimizerIngestCmdFunc(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	// Get default DB path if not provided
	dbPath := optimizerDBPath
	if dbPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		dbPath = filepath.Join(homeDir, ".toolhive", "optimizer.db")
	}

	// Validate model path
	if optimizerModelPath == "" {
		return fmt.Errorf("--model-path is required. Please provide the path to the ONNX embedding model")
	}

	// Check if model file exists
	if _, err := os.Stat(optimizerModelPath); os.IsNotExist(err) {
		return fmt.Errorf("model file not found: %s", optimizerModelPath)
	}

	logger.Infof("Initializing optimizer service")
	logger.Infof("  Database: %s", dbPath)
	logger.Infof("  Model: %s", optimizerModelPath)
	logger.Infof("  Runtime: %s", optimizerRuntimeMode)

	// Create ingestion service
	config := &ingestion.Config{
		DBConfig: &db.Config{
			DBPath: dbPath,
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType:  embeddings.BackendTypePlaceholder, // Default to placeholder for testing
			Model:        "all-minilm",
			Dimension:    384, // BAAI/bge-small-en-v1.5
			EnableCache:  true,
			MaxCacheSize: 1000,
		},
		MCPTimeout:        30,
		RegistryBatchSize: 5,
		WorkloadBatchSize: 5,
		RuntimeMode:       optimizerRuntimeMode,
	}

	svc, err := ingestion.NewService(config)
	if err != nil {
		return fmt.Errorf("failed to create ingestion service: %w", err)
	}
	defer svc.Close()

	// Run ingestion
	if optimizerPollInterval > 0 {
		// Continuous polling mode
		logger.Infof("Starting continuous ingestion (poll interval: %ds)", optimizerPollInterval)
		logger.Info("Press Ctrl+C to stop")

		interval := time.Duration(optimizerPollInterval) * time.Second
		svc.StartPolling(ctx, interval)
	} else {
		// One-time ingestion
		logger.Info("Running one-time ingestion")
		if err := svc.IngestWorkloads(ctx); err != nil {
			return fmt.Errorf("ingestion failed: %w", err)
		}
		logger.Info("Ingestion completed successfully")
	}

	return nil
}

func optimizerQueryCmdFunc(cmd *cobra.Command, args []string) error {
	// Get query text
	query := args[0]
	if len(args) > 1 {
		// Join all arguments into a single query
		query = ""
		for i, arg := range args {
			if i > 0 {
				query += " "
			}
			query += arg
		}
	}

	limit, _ := cmd.Flags().GetInt("limit")

	// Get default DB path if not provided
	dbPath := optimizerDBPath
	if dbPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		dbPath = filepath.Join(homeDir, ".toolhive", "optimizer.db")
	}

	// Validate model path
	if optimizerModelPath == "" {
		return fmt.Errorf("--model-path is required. Please provide the path to the ONNX embedding model")
	}

	logger.Infof("Searching for: %s (limit: %d)", query, limit)

	// TODO: Implement actual semantic search
	// This would:
	// 1. Initialize embedding manager
	// 2. Generate embedding for query text
	// 3. Search database for similar tool embeddings
	// 4. Return ranked results

	logger.Info("Query functionality not yet implemented (placeholder)")
	fmt.Println("Search results:")
	fmt.Println("  (No results - implementation pending)")

	return nil
}

func optimizerStatusCmdFunc(_ *cobra.Command, _ []string) error {
	// Get default DB path if not provided
	dbPath := optimizerDBPath
	if dbPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		dbPath = filepath.Join(homeDir, ".toolhive", "optimizer.db")
	}

	// Check if database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		logger.Warnf("Database does not exist: %s", dbPath)
		fmt.Println("Optimizer Status:")
		fmt.Println("  Database: Not initialized")
		fmt.Println("  Run 'thv optimizer ingest' to initialize")
		return nil
	}

	// Open database
	database, err := db.NewDB(&db.Config{DBPath: dbPath})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	// Query statistics
	ctx := context.Background()

	var workloadCount, toolCount int
	err = database.QueryRowContext(ctx, "SELECT COUNT(*) FROM mcpservers_workload").Scan(&workloadCount)
	if err != nil {
		return fmt.Errorf("failed to count workload servers: %w", err)
	}

	err = database.QueryRowContext(ctx, "SELECT COUNT(*) FROM tools_workload").Scan(&toolCount)
	if err != nil {
		return fmt.Errorf("failed to count tools: %w", err)
	}

	// Display status
	fmt.Println("Optimizer Status:")
	fmt.Printf("  Database: %s\n", dbPath)
	fmt.Printf("  Workload Servers: %d\n", workloadCount)
	fmt.Printf("  Tools: %d\n", toolCount)

	return nil
}
