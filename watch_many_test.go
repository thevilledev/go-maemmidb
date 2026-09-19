// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"
)

// Upstream's TestWatch sweeps watch sets of up to 3*aFew-1 = 95 channels. With
// tiered selects a single select now covers up to maxFew channels, so that
// sweep never reaches the goroutine fan-out. These tests do: they straddle
// every tier boundary and the chunk boundaries of watchMany, with channels
// that have fired before the call (the poll path) and channels that fire
// while the watch is blocked (the select and fan-out paths).

func watchSizes() []int {
	sizes := []int{0, 1, 2, 3, 4, 5, 8, 9, 16, 17, 32, 33, 64, 65}
	for _, c := range []int{1, 2, 3} {
		sizes = append(sizes, c*maxFew-1, c*maxFew, c*maxFew+1)
	}
	return append(sizes, 1000)
}

type watchMode struct {
	name string
	// run blocks on ws and reports whether the watch fired (as opposed to
	// timing out); stop makes it time out.
	start func(ws WatchSet) (result <-chan bool, stop func())
}

var watchModes = []watchMode{
	{"Watch", func(ws WatchSet) (<-chan bool, func()) {
		timeoutCh := make(chan time.Time)
		out := make(chan bool, 1)
		go func() { out <- !ws.Watch(timeoutCh) }()
		return out, func() { close(timeoutCh) }
	}},
	{"WatchCtx", func(ws WatchSet) (<-chan bool, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan bool, 1)
		go func() { out <- ws.WatchCtx(ctx) == nil }()
		return out, cancel
	}},
	{"WatchCh", func(ws WatchSet) (<-chan bool, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		errCh := ws.WatchCh(ctx)
		out := make(chan bool, 1)
		go func() { out <- <-errCh == nil }()
		return out, cancel
	}},
}

func makeWatchSet(size int) (WatchSet, []chan struct{}) {
	ws := NewWatchSet()
	chans := make([]chan struct{}, size)
	for i := range chans {
		chans[i] = make(chan struct{})
		ws.Add(chans[i])
	}
	return ws, chans
}

func expect(t *testing.T, what string, result <-chan bool, wantFired bool) {
	t.Helper()
	select {
	case fired := <-result:
		if fired != wantFired {
			t.Fatalf("%s: fired=%v, want %v", what, fired, wantFired)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: watch did not return", what)
	}
}

func TestWatchSetSizes(t *testing.T) {
	for _, mode := range watchModes {
		t.Run(mode.name, func(t *testing.T) {
			for _, size := range watchSizes() {
				what := fmt.Sprintf("size %d", size)

				// Nothing fires: the watch must block, then time out.
				ws, _ := makeWatchSet(size)
				result, stop := mode.start(ws)
				select {
				case <-result:
					t.Fatalf("%s: returned although nothing fired", what)
				case <-time.After(2 * time.Millisecond):
				}
				stop()
				expect(t, what+" timeout", result, false)

				for _, fire := range []int{0, size / 2, size - 1} {
					if fire < 0 || fire >= size {
						continue
					}

					// Fired before the call.
					ws, chans := makeWatchSet(size)
					close(chans[fire])
					result, stop := mode.start(ws)
					expect(t, fmt.Sprintf("%s pre-fired %d", what, fire), result, true)
					stop()

					// Fires while the watch is blocked, by close and by send.
					for _, send := range []bool{false, true} {
						ws, chans := makeWatchSet(size)
						result, stop := mode.start(ws)
						time.Sleep(time.Millisecond)
						if send {
							select {
							case chans[fire] <- struct{}{}:
							case <-time.After(10 * time.Second):
								t.Fatalf("%s: nobody received on channel %d", what, fire)
							}
						} else {
							close(chans[fire])
						}
						expect(t, fmt.Sprintf("%s fired %d (send=%v)", what, fire, send), result, true)
						stop()
					}
				}
			}
		})
	}
}

// TestWatchSetNilChannels: nil entries and a nil timeout are legal and simply
// never fire.
func TestWatchSetNilChannels(t *testing.T) {
	ws := NewWatchSet()
	ws.Add(nil)
	real := make(chan struct{})
	ws.Add(real)
	out := make(chan bool, 1)
	go func() { out <- ws.Watch(nil) }()
	select {
	case <-out:
		t.Fatal("returned although nothing fired")
	case <-time.After(5 * time.Millisecond):
	}
	close(real)
	select {
	case timedOut := <-out:
		if timedOut {
			t.Fatal("reported a timeout")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not return")
	}

	var nilSet WatchSet
	if nilSet.Watch(nil) {
		t.Fatal("nil set reported a timeout")
	}
	if err := nilSet.WatchCtx(context.Background()); err != nil {
		t.Fatalf("nil set: %v", err)
	}
}

// TestWatchSetNoGoroutineLeak: however a big watch ends, its helper goroutines
// must end with it.
func TestWatchSetNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		ws, chans := makeWatchSet(5 * maxFew)
		timeoutCh := make(chan time.Time)
		out := make(chan bool, 1)
		go func() { out <- ws.Watch(timeoutCh) }()
		time.Sleep(time.Millisecond)
		if i%2 == 0 {
			close(chans[i])
		} else {
			close(timeoutCh)
		}
		<-out
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+2 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines leaked: %d before, %d after", before, runtime.NumGoroutine())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
