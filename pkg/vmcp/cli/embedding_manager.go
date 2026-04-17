// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cli provides the business logic for the vMCP serve, validate, and init
// commands. It is designed to be imported by both the standalone vmcp binary
// (cmd/vmcp/app) and the thv vmcp subcommand (cmd/thv/app).
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// defaultTEIImage is the default HuggingFace Text Embeddings Inference image.
	defaultTEIImage = "ghcr.io/huggingface/text-embeddings-inference:cpu-latest"

	// teiModelCachePath is the path inside the TEI container where models are cached.
	teiModelCachePath = "/data"

	// teiContainerNamePrefix is the prefix for TEI container names.
	teiContainerNamePrefix = "thv-embedding-"

	// teiContainerPort is the port that the TEI HTTP server listens on inside the container.
	teiContainerPort = "80"

	// healthPath is the TEI HTTP health endpoint path.
	// Returns 200 once the model is fully loaded and ready to serve.
	healthPath = "/health"

	// pollInitialInterval is the starting backoff interval for health polling.
	pollInitialInterval = 2 * time.Second

	// pollMultiplier is the exponential growth factor applied after each failed poll.
	pollMultiplier = 2

	// pollMaxInterval is the upper bound for the exponential backoff sleep.
	pollMaxInterval = 30 * time.Second
)

// modelShortHash returns the first 8 hexadecimal characters of the SHA-256 hash
// of the given model name string. Using a hash avoids invalid container-name
// characters (e.g., slashes in "BAAI/bge-small-en-v1.5").
func modelShortHash(model string) string {
	sum := sha256.Sum256([]byte(model))
	return hex.EncodeToString(sum[:])[:8]
}

// containerNameForModel returns the canonical TEI container name for a model.
// Format: thv-embedding-<8-char-hex>
func containerNameForModel(model string) string {
	return teiContainerNamePrefix + modelShortHash(model)
}

// ContainerFactory is the minimal interface over *container.Factory that
// EmbeddingServiceManager requires. Defined here to allow mock injection in unit tests;
// in production callers pass container.NewFactory().
//
//go:generate mockgen -destination=mocks/mock_container_factory.go -package=mocks -source=embedding_manager.go ContainerFactory
type ContainerFactory interface {
	// Create initialises a container runtime backed by the host daemon.
	Create(ctx context.Context) (runtime.Runtime, error)
}

// EmbeddingServiceManagerConfig holds the parameters for constructing an
// EmbeddingServiceManager.
type EmbeddingServiceManagerConfig struct {
	// Model is the HuggingFace model name (e.g. "BAAI/bge-small-en-v1.5").
	// Required; must be non-empty.
	Model string

	// Image is the TEI container image to run.
	// Defaults to defaultTEIImage when empty.
	Image string
}

// EmbeddingServiceManager manages the lifecycle of a TEI container used by the
// Tier 2 semantic optimizer. It creates or reuses a container deterministically
// named after the model, health-polls with exponential backoff, and only stops
// the container if this instance started it.
//
// On Docker the container is bound to a localhost port and the model cache is
// bind-mounted from the host. On Kubernetes the TEI pod is exposed via a
// ClusterIP Service and reached at http://<containerName>:<teiContainerPort>;
// the host bind-mount is skipped because Kubernetes ignores Docker permission
// profiles.
type EmbeddingServiceManager struct {
	factory       ContainerFactory
	cfg           EmbeddingServiceManagerConfig
	containerName string
	port          int
	started       bool // true only when DeployWorkload was called by this instance
	isKubernetes  bool // true when running against a Kubernetes runtime

	// portFinder returns an available host port. On Docker defaults to
	// networking.FindAvailable; on Kubernetes returns the fixed container port
	// (80) because no host-port allocation is needed. Overridden in unit tests.
	portFinder func() int

	// urlFor returns the base URL for the TEI service given a port.
	// On Docker: http://localhost:<port>. On Kubernetes: http://<containerName>:<teiContainerPort>.
	// Overridden in unit tests.
	urlFor func(port int) string

	// healthURLFor returns the full health-check URL for a given port.
	// Overridden in unit tests to redirect to an httptest server.
	healthURLFor func(port int) string

	// modelCacheDir returns the host-side path used as the bind-mount source
	// for the TEI model cache. Only used on Docker. Defaults to
	// ~/.toolhive/embedding-models; overridden in unit tests to a t.TempDir() path.
	modelCacheDir func() (string, error)
}

// NewEmbeddingServiceManager constructs an EmbeddingServiceManager from the given
// factory and config. Returns an error when cfg.Model is empty.
func NewEmbeddingServiceManager(factory ContainerFactory, cfg EmbeddingServiceManagerConfig) (*EmbeddingServiceManager, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("model must not be empty")
	}
	if cfg.Image == "" {
		cfg.Image = defaultTEIImage
	}

	containerName := containerNameForModel(cfg.Model)
	isK8s := runtime.IsKubernetesRuntime()

	mgr := &EmbeddingServiceManager{
		factory:       factory,
		cfg:           cfg,
		containerName: containerName,
		isKubernetes:  isK8s,
		modelCacheDir: func() (string, error) {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("failed to determine home directory for TEI model cache: %w", err)
			}
			return filepath.Join(homeDir, ".toolhive", "embedding-models"), nil
		},
	}

	if isK8s {
		// On Kubernetes, the TEI pod is reachable via the ClusterIP Service that the
		// runtime creates as "mcp-<containerName>". No host-port allocation or
		// localhost binding needed.
		svcName := fmt.Sprintf("mcp-%s", containerName)
		mgr.portFinder = func() int { return 80 }
		mgr.urlFor = func(_ int) string {
			return fmt.Sprintf("http://%s:%s", svcName, teiContainerPort)
		}
		mgr.healthURLFor = func(_ int) string {
			return fmt.Sprintf("http://%s:%s%s", svcName, teiContainerPort, healthPath)
		}
	} else {
		mgr.portFinder = networking.FindAvailable
		mgr.urlFor = func(port int) string {
			return fmt.Sprintf("http://localhost:%d", port)
		}
		mgr.healthURLFor = func(port int) string {
			return fmt.Sprintf("http://localhost:%d%s", port, healthPath)
		}
	}

	return mgr, nil
}

// Start ensures the TEI container is running and returns its HTTP base URL.
// On Docker this is http://localhost:<port>; on Kubernetes it is
// http://<containerName>:<teiContainerPort>.
//
// On first call it checks for an existing running container with the same name;
// if found, it returns that container's URL without starting a new one
// (idempotent reuse). If no container is running, Start deploys a new one,
// then polls its /health endpoint with exponential backoff until it responds
// 200 or ctx is cancelled.
//
// Returns a non-nil error if the container cannot be started or the health
// check never succeeds within the context deadline.
func (m *EmbeddingServiceManager) Start(ctx context.Context) (string, error) {
	rt, err := m.factory.Create(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to create container runtime: %w", err)
	}

	running, err := rt.IsWorkloadRunning(ctx, m.containerName)
	if err != nil {
		if !errors.Is(err, runtime.ErrContainerNotFound) && !errors.Is(err, runtime.ErrWorkloadNotFound) {
			return "", fmt.Errorf("failed to check whether TEI container %q is running: %w", m.containerName, err)
		}
		// Container does not exist yet — fall through to deploy.
		running = false
	}

	if running {
		return m.reuseContainer(ctx, rt)
	}

	return m.deployContainer(ctx, rt)
}

// reuseContainer retrieves the port of an already-running TEI container,
// polls health to confirm it is ready, and returns its URL without changing
// m.started (caller must not stop it).
func (m *EmbeddingServiceManager) reuseContainer(ctx context.Context, rt runtime.Runtime) (string, error) {
	info, err := rt.GetWorkloadInfo(ctx, m.containerName)
	if err != nil {
		return "", fmt.Errorf("failed to get info for existing TEI container %q: %w", m.containerName, err)
	}

	port, err := labels.GetPort(info.Labels)
	if err != nil {
		return "", fmt.Errorf("failed to read port label from existing TEI container %q: %w", m.containerName, err)
	}

	slog.Debug("reusing existing TEI container", "name", m.containerName, "port", port)
	m.port = port

	if err := m.pollHealth(ctx); err != nil {
		return "", err
	}
	return m.urlFor(port), nil
}

// deployContainer allocates a port, deploys the TEI container, marks
// m.started = true, then polls health.
//
// On Docker it also creates the model-cache host directory and adds a localhost
// port binding. On Kubernetes those steps are skipped: the runtime ignores
// Docker permission profiles, and the pod is reachable via its ClusterIP Service.
func (m *EmbeddingServiceManager) deployContainer(ctx context.Context, rt runtime.Runtime) (string, error) {
	port := m.portFinder()
	if port == 0 {
		return "", fmt.Errorf("could not find an available port for TEI container %q", m.containerName)
	}

	m.port = port
	opts := runtime.NewDeployWorkloadOptions()
	opts.ExposedPorts[teiContainerPort+"/tcp"] = struct{}{}

	var profile *permissions.Profile

	if !m.isKubernetes {
		// On Docker: bind-mount the model cache from the host and expose a
		// localhost port so the embedding client can reach TEI directly.
		modelCacheHost, err := m.modelCacheDir()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(modelCacheHost, 0o700); err != nil {
			return "", fmt.Errorf("failed to create TEI model cache directory %q: %w", modelCacheHost, err)
		}
		opts.PortBindings[teiContainerPort+"/tcp"] = []runtime.PortBinding{
			{HostIP: "127.0.0.1", HostPort: strconv.Itoa(port)},
		}
		profile = &permissions.Profile{
			Write: []permissions.MountDeclaration{
				permissions.MountDeclaration(modelCacheHost + ":" + teiModelCachePath),
			},
		}
	}

	labelsMap := make(map[string]string)
	labels.AddStandardLabels(labelsMap, m.containerName, m.containerName, "streamable-http", port)
	labelsMap[labels.LabelAuxiliary] = labels.LabelToolHiveValue

	slog.Debug("deploying TEI container",
		"name", m.containerName,
		"image", m.cfg.Image,
		"model", m.cfg.Model,
		"port", port,
		"kubernetes", m.isKubernetes)

	if _, err := rt.DeployWorkload(
		ctx,
		m.cfg.Image,
		m.containerName,
		[]string{"--model-id", m.cfg.Model},
		nil,
		labelsMap,
		profile,
		"streamable-http",
		opts,
		false,
	); err != nil {
		return "", fmt.Errorf("failed to deploy TEI container %q: %w", m.containerName, err)
	}
	m.started = true

	if err := m.pollHealth(ctx); err != nil {
		return "", err
	}
	return m.urlFor(m.port), nil
}

// pollHealth polls the TEI /health endpoint with exponential backoff until it
// returns HTTP 200 or ctx is cancelled. Response bodies are always drained and
// closed to allow connection reuse.
func (m *EmbeddingServiceManager) pollHealth(ctx context.Context) error {
	healthURL := m.healthURLFor(m.port)
	interval := pollInitialInterval

	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return fmt.Errorf("failed to build TEI health request: %w", err)
		}
		resp, err := http.DefaultClient.Do(req) //nolint:gosec // URL constructed from localhost+known port
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				slog.Debug("TEI container is healthy", "name", m.containerName, "url", healthURL)
				return nil
			}
			slog.Debug("TEI container not yet healthy", "name", m.containerName, "status", resp.StatusCode)
		} else {
			slog.Debug("TEI health check failed", "name", m.containerName, "error", err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("TEI container %q did not become healthy within deadline: %w",
				m.containerName, ctx.Err())
		case <-timer.C:
			interval = min(interval*pollMultiplier, pollMaxInterval)
			timer.Reset(interval)
		}
	}
}

// Stop stops the TEI container if this EmbeddingServiceManager instance started it.
// If the container was already running when Start was called (reuse case),
// Stop is a no-op — the container belongs to whichever process created it.
func (m *EmbeddingServiceManager) Stop(ctx context.Context) error {
	if !m.started {
		return nil
	}

	rt, err := m.factory.Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime for stop: %w", err)
	}

	if err := rt.StopWorkload(ctx, m.containerName); err != nil {
		return fmt.Errorf("failed to stop TEI container %q: %w", m.containerName, err)
	}
	return nil
}
