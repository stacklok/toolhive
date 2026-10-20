// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"fmt"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// computeContentDigest hashes the canonical plugin tree (the file set
// ExtractPlugin writes), for recording in the lock file at install time.
// The hash is taken from the decompressed layer in memory rather than a
// client adapter's on-disk directory, so marketplace.json / settings.json
// mutations are not part of the pin. Sync --check hashes the ExtractPlugin
// destination, which is the same file set.
func computeContentDigest(layerData []byte) (string, error) {
	if len(layerData) == 0 {
		return "", fmt.Errorf("plugin layer data is empty")
	}

	tarData, err := ociskills.DecompressWithLimit(layerData, skills.MaxTotalExtractSize)
	if err != nil {
		return "", fmt.Errorf("decompressing plugin layer: %w", err)
	}
	files, err := ociskills.ExtractTarWithLimit(tarData, skills.MaxFileExtractSize)
	if err != nil {
		return "", fmt.Errorf("extracting plugin tree: %w", err)
	}
	if len(files) > skills.MaxExtractFileCount {
		return "", fmt.Errorf("archive contains %d files, exceeding limit of %d",
			len(files), skills.MaxExtractFileCount)
	}

	contentFiles := make([]lockfile.ContentFile, 0, len(files))
	for _, f := range files {
		contentFiles = append(contentFiles, lockfile.ContentFile{Path: f.Path, Content: f.Content})
	}
	digest, err := lockfile.ContentDigest(contentFiles)
	if err != nil {
		return "", fmt.Errorf("computing content digest: %w", err)
	}
	return digest, nil
}
