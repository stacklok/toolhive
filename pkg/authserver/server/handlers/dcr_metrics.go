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
// attributes. Every capacity rejection is counted here; capacityWarner logs
// them only at a time-bounded rate, because the caller of /oauth/register is
// unauthenticated.
type dcrMetrics struct {
	capacityRejections metric.Int64Counter
	// gaugeRegistration is the live-client gauge's callback, or nil when the
	// storage backend cannot count DCR clients.
	gaugeRegistration metric.Registration
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

	m := &dcrMetrics{capacityRejections: capacityRejections}
	counter, ok := storage.Unwrap(stor).(storage.DCRClientCounter)
	if !ok {
		return m, nil
	}
	gauge, err := meter.Int64ObservableGauge(
		"toolhive_authserver_dcr_clients",
		metric.WithDescription("Number of live dynamically registered clients counted against the DCR client cap"),
	)
	if err != nil {
		return nil, fmt.Errorf("create DCR client gauge: %w", err)
	}
	m.gaugeRegistration, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		n, err := counter.CountDCRClients(ctx)
		if err != nil {
			return err
		}
		o.ObserveInt64(gauge, n)
		return nil
	}, gauge)
	if err != nil {
		return nil, fmt.Errorf("register DCR client gauge callback: %w", err)
	}
	return m, nil
}

// close unregisters the gauge callback. It is a no-op on a nil receiver or
// when no gauge was registered.
func (m *dcrMetrics) close() error {
	if m == nil || m.gaugeRegistration == nil {
		return nil
	}
	if err := m.gaugeRegistration.Unregister(); err != nil {
		return fmt.Errorf("unregister DCR client gauge callback: %w", err)
	}
	return nil
}

// recordCapacityRejection counts one registration rejected at the DCR cap. It
// is a no-op on a nil receiver, for Handlers built without NewHandler.
func (m *dcrMetrics) recordCapacityRejection(ctx context.Context) {
	if m == nil {
		return
	}
	m.capacityRejections.Add(ctx, 1)
}
