// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import "sync"

// AdmissionQueue controls admission of concurrent requests to a shared
// resource that can be closed. Once closed, no further requests are admitted
// and CloseAndDrain blocks until all previously-admitted requests complete.
type AdmissionQueue interface {
	// TryAdmit attempts to admit a request. If the queue is open, it returns
	// (true, done) where done must be called when the request completes.
	// If the queue is already closed, it returns (false, nil).
	TryAdmit() (bool, func())

	// CloseAndDrain closes the queue so that subsequent TryAdmit calls return
	// false, then blocks until all currently-admitted requests have called
	// their done function. Idempotent.
	CloseAndDrain()
}

// admissionQueue is the production AdmissionQueue implementation.
// It uses a read-write mutex to protect the closed flag and an
// atomic-counter wait group to track in-flight requests.
//
// Invariant: wg.Add(1) is always called while mu is read-locked,
// so CloseAndDrain() cannot observe a zero wait-group count between
// a caller's closed-check and its wg.Add.
type admissionQueue struct {
	mu     sync.RWMutex
	wg     sync.WaitGroup
	closed bool
}

// Compile-time assertion: admissionQueue must implement AdmissionQueue.
var _ AdmissionQueue = (*admissionQueue)(nil)

func newAdmissionQueue() AdmissionQueue {
	return &admissionQueue{}
}

func (q *admissionQueue) TryAdmit() (bool, func()) {
	q.mu.RLock()
	if q.closed {
		q.mu.RUnlock()
		return false, nil
	}
	q.wg.Add(1)
	q.mu.RUnlock()
	return true, q.wg.Done
}

func (q *admissionQueue) CloseAndDrain() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()
	q.wg.Wait()
}
