// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileutils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteContainedFile writes content to filePath (relative) inside targetDir,
// ensuring the resulting path does not escape targetDir. Parent directories
// are created with dirPerm, and the file is written atomically with filePerm.
//
// targetDir must already be filepath.Clean'd by the caller.
func WriteContainedFile(targetDir, filePath string, content []byte, dirPerm, filePerm os.FileMode) error {
	cleanTarget := targetDir + string(os.PathSeparator)
	destPath := filepath.Clean(filepath.Join(targetDir, filepath.FromSlash(filePath)))

	if !strings.HasPrefix(destPath, cleanTarget) {
		return fmt.Errorf("path traversal detected: file %q escapes target directory", filePath)
	}

	parentDir := filepath.Dir(destPath)
	if err := os.MkdirAll(parentDir, dirPerm); err != nil {
		return fmt.Errorf("creating directory %q: %w", parentDir, err)
	}

	if err := AtomicWriteFile(destPath, content, filePerm); err != nil {
		return fmt.Errorf("writing file %q: %w", filePath, err)
	}

	return nil
}
