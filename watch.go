// Copyright IBM Corp. 2015, 2026
// SPDX-License-Identifier: MPL-2.0
//
// Modifications Copyright (c) 2026 Ville Vesilehto
// Derived from github.com/hashicorp/go-memdb watch.go @ 7d3fdd5. The exported
// API and its documentation are upstream's. The waiting strategy is new: sets
// of up to maxFew channels wait in one right-sized select together with the
// timeout or context (no helper goroutine, no context allocation, no slice
// allocation); bigger sets return at once if the timeout or context is already
// done and are otherwise spread over goroutines in chunks of fanoutChunk.

package memdb

import (
	"context"
	"time"
)

// WatchSet is a collection of watch channels. The zero value is not usable.
// Use NewWatchSet to create a WatchSet.
type WatchSet map[<-chan struct{}]struct{}

// NewWatchSet constructs a new watch set.
func NewWatchSet() WatchSet {
	return make(map[<-chan struct{}]struct{})
}

// Add appends a watchCh to the WatchSet if non-nil.
func (w WatchSet) Add(watchCh <-chan struct{}) {
	if w == nil {
		return
	}

	w[watchCh] = struct{}{}
}

// AddWithLimit appends a watchCh to the WatchSet if non-nil, and if the given
// softLimit hasn't been exceeded. Otherwise, it will watch the given alternate
// channel. It's expected that the altCh will be the same on many calls to this
// function, so you will exceed the soft limit a little bit if you hit this, but
// not by much.
//
// This is useful if you want to track individual items up to some limit, after
// which you watch a higher-level channel (usually a channel from start of
// an iterator higher up in the radix tree) that will watch a superset of items.
func (w WatchSet) AddWithLimit(softLimit int, watchCh <-chan struct{}, altCh <-chan struct{}) {
	// This is safe for a nil WatchSet so we don't need to check that here.
	if len(w) < softLimit {
		w.Add(watchCh)
	} else {
		w.Add(altCh)
	}
}

// Watch blocks until one of the channels in the watch set is closed, or
// timeoutCh sends a value.
// Returns true if timeoutCh is what caused Watch to unblock.
func (w WatchSet) Watch(timeoutCh <-chan time.Time) bool {
	if w == nil {
		return false
	}

	return w.watch(nil, timeoutCh) == watchTimeout
}

// WatchCtx blocks until one of the channels in the watch set is closed, or
// ctx is done (cancelled or exceeds the deadline). WatchCtx returns an error
// if the ctx causes it to unblock, otherwise returns nil.
//
// WatchCtx should be preferred over Watch.
func (w WatchSet) WatchCtx(ctx context.Context) error {
	if w == nil {
		return nil
	}

	if w.watch(ctx.Done(), nil) == watchDone {
		return ctx.Err()
	}
	return nil
}

// The on-stack channel arrays come in three sizes, so that a small watch set
// -- by far the most common kind -- does not pay for clearing a big one.
const (
	smallFew  = 8
	mediumFew = 32
)

// watchFew runs the single select for a small set. At most one of done and
// timeoutCh is in use (see Watch and WatchCtx).
func watchFew(n int, done <-chan struct{}, timeoutCh <-chan time.Time, ch []<-chan struct{}) int {
	if timeoutCh != nil {
		return watchFewTimeout(n, timeoutCh, ch)
	}
	return watchFewDone(n, done, ch)
}

// watch blocks until a channel of the set fires, done is closed, or timeoutCh
// delivers, and reports which of the three happened.
func (w WatchSet) watch(done <-chan struct{}, timeoutCh <-chan time.Time) int {
	n := len(w)
	switch {
	case n <= smallFew:
		var chunk [smallFew]<-chan struct{}
		i := 0
		for watchCh := range w {
			chunk[i] = watchCh
			i++
		}
		return watchFew(n, done, timeoutCh, chunk[:])

	case n <= mediumFew:
		var chunk [mediumFew]<-chan struct{}
		i := 0
		for watchCh := range w {
			chunk[i] = watchCh
			i++
		}
		return watchFew(n, done, timeoutCh, chunk[:])

	case n <= maxFew:
		var chunk [maxFew]<-chan struct{}
		i := 0
		for watchCh := range w {
			chunk[i] = watchCh
			i++
		}
		return watchFew(n, done, timeoutCh, chunk[:])
	}
	return w.watchMany(done, timeoutCh)
}

// watchMany is used if there are many watchers.
func (w WatchSet) watchMany(done <-chan struct{}, timeoutCh <-chan time.Time) int {
	// If the caller is already done there is no need to set up any machinery
	// at all. (Looking at every watch channel first as well was tried and
	// measured: it only pays when something has already fired, and costs the
	// normal, blocking case about 5%.)
	select {
	case <-done:
		return watchDone
	case <-timeoutCh:
		return watchTimeout
	default:
	}

	// Set up a goroutine for each chunk of watchers, all stopped on return.
	stopCh := make(chan struct{})
	defer close(stopCh)
	triggerCh := make(chan struct{}, 1)
	watcher := func(chunk []<-chan struct{}) {
		if watchChunk(stopCh, chunk) == watchFired {
			select {
			case triggerCh <- struct{}{}:
			default:
			}
		}
	}

	// Apportion the watch channels into chunks, each its own small slice (a
	// nil tail is fine: a nil channel is never ready), and fire each chunk off
	// as soon as it is full.
	idx := 0
	chunk := make([]<-chan struct{}, fanoutChunk)
	for watchCh := range w {
		chunk[idx%fanoutChunk] = watchCh
		idx++
		if idx%fanoutChunk == 0 {
			go watcher(chunk)
			if idx < len(w) {
				chunk = make([]<-chan struct{}, fanoutChunk)
			}
		}
	}
	if idx%fanoutChunk != 0 {
		go watcher(chunk)
	}

	// Wait for a channel to trigger or timeout.
	select {
	case <-triggerCh:
		return watchFired
	case <-done:
		return watchDone
	case <-timeoutCh:
		return watchTimeout
	}
}

// WatchCh returns a channel that is used to wait for any channel of the watch set to trigger
// or for the context to be cancelled. WatchCh creates a new goroutine each call, so
// callers may need to cache the returned channel to avoid creating extra goroutines.
func (w WatchSet) WatchCh(ctx context.Context) <-chan error {
	// Create the outgoing channel
	triggerCh := make(chan error, 1)

	// Create a goroutine to collect the error from WatchCtx
	go func() {
		triggerCh <- w.WatchCtx(ctx)
	}()

	return triggerCh
}
