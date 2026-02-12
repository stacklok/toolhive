// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import "github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"

// EmbeddingClient generates vector embeddings from text.
// It is defined in the internal/types package and aliased here so that
// external consumers continue to use optimizer.EmbeddingClient.
type EmbeddingClient = types.EmbeddingClient
