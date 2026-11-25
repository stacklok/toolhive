// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"

	"github.com/stacklok/toolhive-core/httperr"
)

const (
	// DefaultAppName is the default application name used for XDG paths
	DefaultAppName = "toolhive"

	// FileExtension is the file extension for stored configurations
	FileExtension = ".json"
)

// LocalStore implements the Store interface using the local filesystem
// following the XDG Base Directory Specification
type LocalStore struct {
	// basePath is the base directory path for storing configurations
	basePath string
}

// NewLocalStore creates a new LocalStore with the given application name and store type
// If appName is empty, DefaultAppName will be used
func NewLocalStore(appName string, storeName string) (*LocalStore, error) {
	if appName == "" {
		appName = DefaultAppName
	}

	// Create the base directory path following XDG spec
	basePath := filepath.Join(xdg.StateHome, appName, storeName)

	// Ensure the directory exists
	if err := os.MkdirAll(basePath, 0750); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	return &LocalStore{
		basePath: basePath,
	}, nil
}

// getFilePath returns the full file path for a configuration, validating that
// the resolved path stays within the store's base directory.
func (s *LocalStore) getFilePath(name string) (string, error) {
	if !strings.HasSuffix(name, FileExtension) {
		name = name + FileExtension
	}
	filePath := filepath.Join(s.basePath, name)
	// filepath.Join calls filepath.Clean, which resolves ".." components.
	// Verify the result is still within basePath to prevent path traversal.
	if !strings.HasPrefix(filePath, s.basePath+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal detected: name escapes state directory")
	}
	return filePath, nil
}

// GetReader returns a reader for the state data
func (s *LocalStore) GetReader(_ context.Context, name string) (io.ReadCloser, error) {
	filePath, err := s.getFilePath(name)
	if err != nil {
		return nil, err
	}
	// #nosec G304 - path traversal is prevented by the containment check in getFilePath
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, httperr.WithCode(fmt.Errorf("state '%s' not found", name), http.StatusNotFound)
		}
		return nil, fmt.Errorf("failed to open state file: %w", err)
	}

	return file, nil
}

// localWriter writes state to a temporary file and publishes it when closed.
type localWriter struct {
	file       *os.File
	tempPath   string
	targetPath string
	exclusive  bool
}

// Write writes state data to the temporary file.
func (w *localWriter) Write(p []byte) (int, error) {
	return w.file.Write(p)
}

// Sync flushes the temporary file to durable storage.
func (w *localWriter) Sync() error {
	return w.file.Sync()
}

// Close syncs and closes the temporary file before atomically publishing it.
func (w *localWriter) Close() error {
	if err := w.file.Sync(); err != nil {
		return w.cleanupAfterError(err)
	}
	if err := w.file.Close(); err != nil {
		return w.removeTemp(err)
	}

	if w.exclusive {
		if err := os.Link(w.tempPath, w.targetPath); err != nil {
			if os.IsExist(err) {
				return w.removeTemp(httperr.WithCode(
					fmt.Errorf("state '%s' already exists", strings.TrimSuffix(filepath.Base(w.targetPath), FileExtension)),
					http.StatusConflict,
				))
			}
			return w.removeTemp(fmt.Errorf("failed to publish state file: %w", err))
		}
		if err := os.Remove(w.tempPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to remove published temporary state file", "path", w.tempPath, "error", err)
		}
		return nil
	}

	if err := os.Rename(w.tempPath, w.targetPath); err != nil {
		return w.removeTemp(fmt.Errorf("failed to publish state file: %w", err))
	}
	return nil
}

// Abort closes and removes the temporary file without publishing it.
func (w *localWriter) Abort() error {
	return w.cleanupAfterError(nil)
}

func (w *localWriter) cleanupAfterError(err error) error {
	return w.removeTemp(errors.Join(err, w.file.Close()))
}

func (w *localWriter) removeTemp(err error) error {
	if removeErr := os.Remove(w.tempPath); removeErr != nil && !os.IsNotExist(removeErr) {
		return errors.Join(err, fmt.Errorf("failed to remove temporary state file: %w", removeErr))
	}
	return err
}

// GetWriter returns a writer for the state data. Data is atomically published when the writer is closed.
func (s *LocalStore) GetWriter(_ context.Context, name string) (io.WriteCloser, error) {
	filePath, err := s.getFilePath(name)
	if err != nil {
		return nil, err
	}

	return s.newWriter(filePath, false)
}

// CreateExclusive creates a new state entry exclusively. Data is atomically published when the writer is closed.
func (s *LocalStore) CreateExclusive(_ context.Context, name string) (io.WriteCloser, error) {
	filePath, err := s.getFilePath(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filePath); err == nil {
		return nil, httperr.WithCode(
			fmt.Errorf("state '%s' already exists", name),
			http.StatusConflict,
		)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to check state file: %w", err)
	}

	return s.newWriter(filePath, true)
}

// Delete removes the data for the given name
func (s *LocalStore) Delete(_ context.Context, name string) error {
	filePath, err := s.getFilePath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("state '%s' not found", name)
		}
		return fmt.Errorf("failed to delete state file: %w", err)
	}
	return nil
}

// List returns all available state names
func (s *LocalStore) List(_ context.Context) ([]string, error) {
	// Read the directory
	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read state directory: %w", err)
	}

	// Filter and process the file names
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasSuffix(name, FileExtension) {
			// Remove the file extension
			name = strings.TrimSuffix(name, FileExtension)
			names = append(names, name)
		}
	}

	return names, nil
}

// Exists checks if data exists for the given name
func (s *LocalStore) Exists(_ context.Context, name string) (bool, error) {
	filePath, err := s.getFilePath(name)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check if state exists: %w", err)
	}
	return true, nil
}

func (*LocalStore) newWriter(filePath string, exclusive bool) (io.WriteCloser, error) {
	file, err := os.CreateTemp(filepath.Dir(filePath), "."+filepath.Base(filePath)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary state file: %w", err)
	}

	return &localWriter{file: file, tempPath: file.Name(), targetPath: filePath, exclusive: exclusive}, nil
}
