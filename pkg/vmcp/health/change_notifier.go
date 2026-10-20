// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"slices"
	"sync"
	"time"
)

// ChangeListener receives backend-catalog change notifications from the
// Monitor (see Monitor.OnChange). generation is a monotonically increasing
// counter identifying the latest change folded into this notification;
// intermediate generations are coalesced away by debouncing, so a listener
// never sees the same generation twice. Generations are assigned in
// dispatch order, but each delivery runs on its own goroutine: a listener
// still processing one notification when the next window's delivery starts
// can observe them overlapping or out of order. Listeners must therefore
// treat a notification as "re-read current state", not as an ordered event
// log (the tools-resync listener re-derives from the live health view, so
// this is inherently safe there).
//
// Listeners are invoked on a dedicated goroutine — never on a health-check or
// UpdateBackends call path — so they may safely call back into the Monitor's
// read methods. Listeners MUST NOT call Monitor.Stop: Stop waits for in-flight
// notifications to complete and would deadlock.
type ChangeListener func(generation uint64)

// changeNotifier coalesces backend-catalog change events and fans them out to
// subscribed listeners, debounced to a fixed window: the first event after a
// quiet period is delivered immediately, and further events inside the window
// collapse into a single trailing notification carrying the latest
// generation. This bounds delivery to at most two notifications per window
// (one leading, one trailing) no matter how many backends change at once, so
// a flapping backend cannot storm listeners.
//
// The zero value is not usable; construct with newChangeNotifier.
type changeNotifier struct {
	// window is the debounce window between deliveries.
	window time.Duration

	// fireWG tracks in-flight delivery goroutines so stop can wait for them
	// (keeps shutdown deterministic and goroutine-leak-free).
	fireWG sync.WaitGroup

	mu           sync.Mutex
	listeners    []ChangeListener
	generation   uint64 // bumped on every notify()
	deliveredGen uint64 // latest generation handed to listeners
	lastFire     time.Time
	timer        *time.Timer // non-nil while a trailing delivery is scheduled
	stopped      bool
}

// newChangeNotifier returns a notifier that debounces deliveries to window.
func newChangeNotifier(window time.Duration) *changeNotifier {
	return &changeNotifier{window: window}
}

// subscribe registers fn for future deliveries — every delivery fired after
// subscription, including a trailing delivery already scheduled for changes
// that predate the subscription. fn never observes deliveries that fired
// before it subscribed.
func (n *changeNotifier) subscribe(fn ChangeListener) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.listeners = append(n.listeners, fn)
}

// notify records one catalog change and schedules its delivery: immediately
// when the window has elapsed since the last delivery, otherwise via a single
// trailing timer that folds every notify within the window into one delivery.
func (n *changeNotifier) notify() {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.generation++
	if n.stopped || n.timer != nil {
		// Stopped: never deliver. Timer pending: the scheduled trailing
		// delivery picks up the generation bumped above.
		return
	}
	if delay := n.window - time.Since(n.lastFire); delay > 0 {
		n.timer = time.AfterFunc(delay, n.fireTrailing)
		return
	}
	n.fireLocked()
}

// stop cancels any pending trailing delivery, suppresses all future delivery,
// and waits for in-flight listener invocations to return. Idempotent.
func (n *changeNotifier) stop() {
	n.mu.Lock()
	n.stopped = true
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	n.mu.Unlock()

	n.fireWG.Wait()
}

// fireTrailing runs when the trailing timer expires: it delivers the current
// generation unless it was already delivered (or the notifier stopped).
func (n *changeNotifier) fireTrailing() {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.timer = nil
	if n.stopped || n.generation == n.deliveredGen {
		return
	}
	n.fireLocked()
}

// fireLocked delivers the current generation to all listeners on a fresh
// goroutine, so no delivery ever runs on a health-check or UpdateBackends
// call path (listener code may take the Monitor's locks). Caller must hold
// n.mu.
func (n *changeNotifier) fireLocked() {
	n.lastFire = time.Now()
	n.deliveredGen = n.generation
	if len(n.listeners) == 0 {
		return
	}

	gen := n.generation
	listeners := slices.Clone(n.listeners)
	n.fireWG.Add(1)
	go func() {
		defer n.fireWG.Done()
		for _, fn := range listeners {
			fn(gen)
		}
	}()
}
