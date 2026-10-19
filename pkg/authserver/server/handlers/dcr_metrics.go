// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/metric"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

const dcrInstrumentationName = "github.com/stacklok/toolhive/pkg/authserver"

// dcrMetrics lets operators see the DCR client cap. It records no per-client
// attributes, and a capacity rejection is counted rather than logged: the
// caller of /oauth/register is unauthenticated, so a log line there would
// have unbounded volume.
type dcrMetrics struct {
	capacityRejections metric.Int64Counter
}

// newDCRMetrics creates the capacity rejection counter and, when the storage
// backend implements storage.DCRClientCounter, the live-client gauge. The
// gauge is read from storage at collection time, never on a request path.
func newDCRMetrics(meterProvider metric.MeterProvider, stor storage.Storage) (*dcrMetrics, error) {
	meter := meterProvider.Meter(dcrInstrumentationName)

	capacityRejections, err := meter.Int64Counter(
		"toolhive_authserver_dcr_capacity_rejections",
		metric.WithDescription("Total number of dynamic client registrations rejected because the DCR client cap was reached"),
	)
	if err != nil {
		return nil, fmt.Errorf("create DCR capacity rejection counter: %w", err)
	}

	if counter, ok := storage.Unwrap(stor).(storage.DCRClientCounter); ok {
		_, err := meter.Int64ObservableGauge(
			"toolhive_authserver_dcr_clients",
			metric.WithDescription("Number of live dynamically registered clients counted against the DCR client cap"),
			metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
				n, err := counter.CountDCRClients(ctx)
				if err != nil {
					return err
				}
				o.Observe(n)
				return nil
			}),
		)
		if err != nil {
			return nil, fmt.Errorf("create DCR client gauge: %w", err)
		}
	}

	return &dcrMetrics{capacityRejections: capacityRejections}, nil
}

// recordCapacityRejection counts one registration rejected at the DCR cap. It
// is a no-op on a nil receiver, for Handlers built without NewHandler.
func (m *dcrMetrics) recordCapacityRejection(ctx context.Context) {
	if m == nil {
		return
	}
	m.capacityRejections.Add(ctx, 1)
}
