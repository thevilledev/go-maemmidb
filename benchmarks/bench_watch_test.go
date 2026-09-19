// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package bench

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// BenchmarkFirstWatch is a watched point lookup on rows nobody has written to
// since they were loaded. The channel is always consumed.
func BenchmarkFirstWatch(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			f := getFixture(b, schemaS3, shapeUUID, size, "")
			txn := f.db.Txn(false)
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				ch, obj, err := txn.FirstWatch(tableMain, "id", f.rows[f.perm[j%size]].ID)
				j++
				must(b, err)
				if obj == nil || ch == nil {
					b.Fatal("no result")
				}
				sinkCh = ch
			}
		})
	}
}

// BenchmarkGetWatchCh is a watched prefix scan: Get, WatchCh and one Next.
func BenchmarkGetWatchCh(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			f := getFixture(b, schemaS3, shapeUUID, size, "")
			txn := f.db.Txn(false)
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				it, err := txn.Get(tableMain, "group", groupName(j%f.groups))
				j++
				must(b, err)
				ch := it.WatchCh()
				if ch == nil || it.Next() == nil {
					b.Fatal("no result")
				}
				sinkCh = ch
			}
		})
	}
}

// BenchmarkWatchCycle is the complete blocking-query loop on one row: take a
// watch on a freshly written row (so the watch is always cold), update the row
// in a write transaction, and observe the notification.
func BenchmarkWatchCycle(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			f := getFixture(b, schemaS3, shapeUUID, size, "watch-cycle")
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				i := f.perm[j%size]
				row := f.alt[i]
				if (j/size)%2 == 1 {
					row = f.rows[i]
				}
				j++

				rtxn := f.db.Txn(false)
				ch, obj, err := rtxn.FirstWatch(tableMain, "id", row.ID)
				must(b, err)
				if obj == nil {
					b.Fatal("row missing")
				}

				wtxn := f.db.Txn(true)
				must(b, wtxn.Insert(tableMain, row))
				wtxn.Commit()

				if !isClosed(ch) {
					b.Fatal("watch did not fire")
				}
			}
		})
	}
}

// BenchmarkCommitNotify watches 100 rows plus their group scan, then rewrites
// all 100 rows in one transaction: commit has to notify every watcher.
func BenchmarkCommitNotify(b *testing.B) {
	const watched = 100
	for _, size := range benchSizes() {
		b.Run(fmt.Sprintf("size=%d/rows=%d", size, watched), func(b *testing.B) {
			f := getFixture(b, schemaS3, shapeUUID, size, "commit-notify")
			chans := make([]<-chan struct{}, 0, watched+1)
			b.ReportAllocs()
			j := 0
			for b.Loop() {
				other := f.alt
				if j%2 == 1 {
					other = f.rows
				}
				j++

				chans = chans[:0]
				rtxn := f.db.Txn(false)
				for k := 0; k < watched; k++ {
					ch, _, err := rtxn.FirstWatch(tableMain, "id", f.rows[k].ID)
					must(b, err)
					chans = append(chans, ch)
				}
				it, err := rtxn.Get(tableMain, "group", f.rows[0].Group)
				must(b, err)
				chans = append(chans, it.WatchCh())

				wtxn := f.db.Txn(true)
				for k := 0; k < watched; k++ {
					must(b, wtxn.Insert(tableMain, other[k]))
				}
				wtxn.Commit()

				for _, ch := range chans {
					if !isClosed(ch) {
						b.Fatal("watch did not fire")
					}
				}
			}
		})
	}
}

// BenchmarkWatchSetAdd builds a 32-channel watch set.
func BenchmarkWatchSetAdd(b *testing.B) {
	chans := make([]chan struct{}, 32)
	for i := range chans {
		chans[i] = make(chan struct{})
	}
	b.ReportAllocs()
	for b.Loop() {
		ws := NewWatchSet()
		for _, ch := range chans {
			ws.Add(ch)
		}
		sinkInt = len(ws)
	}
}

func watchSetOf(n int) (WatchSet, []chan struct{}) {
	ws := NewWatchSet()
	chans := make([]chan struct{}, n)
	for i := range chans {
		chans[i] = make(chan struct{})
		ws.Add(chans[i])
	}
	return ws, chans
}

var watchSetSizes = []int{1, 8, 32, 33, 256, 1024}

// BenchmarkWatchSetWatchExpired is upstream's BenchmarkWatch generalised over
// set sizes: no channel fires and the timeout has already expired.
func BenchmarkWatchSetWatchExpired(b *testing.B) {
	for _, n := range watchSetSizes {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			ws, _ := watchSetOf(n)
			timeoutCh := make(chan time.Time)
			close(timeoutCh)
			b.ReportAllocs()
			for b.Loop() {
				sinkBool = ws.Watch(timeoutCh)
			}
		})
	}
}

// BenchmarkWatchSetWatchCtxCancelled: no channel fires, context already done.
func BenchmarkWatchSetWatchCtxCancelled(b *testing.B) {
	for _, n := range watchSetSizes {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			ws, _ := watchSetOf(n)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			b.ReportAllocs()
			for b.Loop() {
				sinkBool = ws.WatchCtx(ctx) != nil
			}
		})
	}
}

// BenchmarkWatchSetWatchCtxFired: one channel of the set has already fired.
func BenchmarkWatchSetWatchCtxFired(b *testing.B) {
	for _, n := range watchSetSizes {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			ws, chans := watchSetOf(n)
			close(chans[n/2])
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				if err := ws.WatchCtx(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkWatchSetBlockThenFire exercises the path that really blocks. A
// helper goroutine fires one channel as soon as it is told the watch is about
// to start; with -cpu 1 the helper cannot run until the watching goroutine has
// parked inside its select, so every iteration goes through a genuine block
// and wake-up, with no timer involved.
func BenchmarkWatchSetBlockThenFire(b *testing.B) {
	for _, n := range []int{8, 1024} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			ctx := context.Background()
			type job struct{ ch chan struct{} }
			jobs := make(chan job)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for j := range jobs {
					close(j.ch)
				}
			}()

			// The channel that fires is replaced after every iteration; the
			// other n-1 stay for the whole run.
			ws, chans := watchSetOf(n)
			fire := chans[n/2]
			b.ReportAllocs()
			for b.Loop() {
				jobs <- job{fire}
				if err := ws.WatchCtx(ctx); err != nil {
					b.Fatal(err)
				}
				delete(ws, fire)
				fire = make(chan struct{})
				ws.Add(fire)
			}
			close(jobs)
			<-done
		})
	}
}
