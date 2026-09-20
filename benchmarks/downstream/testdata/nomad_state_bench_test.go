// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package state

import (
	"fmt"
	"testing"

	memdb "github.com/hashicorp/go-memdb"
	"github.com/hashicorp/nomad/nomad/mock"
	"github.com/hashicorp/nomad/nomad/structs"
)

// A mid-size Nomad cluster: 500 client nodes, 100 jobs, 10 allocations per job
// spread across the nodes (5000 allocations total).
const (
	benchNomadNodes     = 500
	benchNomadJobs      = 100
	benchNomadAllocsPer = 10
)

type benchFixture struct {
	store   *StateStore
	nodeIDs []string
	jobs    []*structs.Job
	idx     uint64
}

func benchNomadState(tb testing.TB) *benchFixture {
	s := TestStateStore(tb)
	f := &benchFixture{store: s, idx: 1}

	f.nodeIDs = make([]string, benchNomadNodes)
	for i := 0; i < benchNomadNodes; i++ {
		n := mock.Node()
		n.Name = fmt.Sprintf("client-%04d", i)
		f.nodeIDs[i] = n.ID
		if err := s.UpsertNode(structs.MsgTypeTestSetup, f.idx, n); err != nil {
			tb.Fatal(err)
		}
		f.idx++
	}

	f.jobs = make([]*structs.Job, benchNomadJobs)
	for i := 0; i < benchNomadJobs; i++ {
		j := mock.Job()
		j.ID = fmt.Sprintf("job-%03d", i)
		j.Name = j.ID
		f.jobs[i] = j
		if err := s.UpsertJob(structs.MsgTypeTestSetup, f.idx, nil, j); err != nil {
			tb.Fatal(err)
		}
		f.idx++
	}

	for i, j := range f.jobs {
		allocs := make([]*structs.Allocation, benchNomadAllocsPer)
		for k := 0; k < benchNomadAllocsPer; k++ {
			a := mock.Alloc()
			a.Job = j
			a.JobID = j.ID
			a.Namespace = j.Namespace
			a.TaskGroup = j.TaskGroups[0].Name
			a.NodeID = f.nodeIDs[(i*benchNomadAllocsPer+k)%benchNomadNodes]
			allocs[k] = a
		}
		if err := s.UpsertAllocs(structs.MsgTypeTestSetup, f.idx, allocs); err != nil {
			tb.Fatal(err)
		}
		f.idx++
	}
	return f
}

// --- read paths -------------------------------------------------------------

// What every Nomad client asks for on each heartbeat.
func BenchmarkNomadAllocsByNode(b *testing.B) {
	f := benchNomadState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := f.store.AllocsByNode(nil, f.nodeIDs[i%benchNomadNodes])
		if err != nil || len(out) == 0 {
			b.Fatalf("err=%v n=%d", err, len(out))
		}
	}
}

// The same as a blocking query, with the watch set the client long-poll uses.
func BenchmarkNomadAllocsByNodeBlocking(b *testing.B) {
	f := benchNomadState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ws := memdb.NewWatchSet()
		out, err := f.store.AllocsByNode(ws, f.nodeIDs[i%benchNomadNodes])
		if err != nil || len(out) == 0 {
			b.Fatalf("err=%v n=%d", err, len(out))
		}
	}
}

func BenchmarkNomadAllocsByJob(b *testing.B) {
	f := benchNomadState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j := f.jobs[i%benchNomadJobs]
		out, err := f.store.AllocsByJob(nil, j.Namespace, j.ID, false)
		if err != nil || len(out) == 0 {
			b.Fatalf("err=%v n=%d", err, len(out))
		}
	}
}

func BenchmarkNomadJobByID(b *testing.B) {
	f := benchNomadState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j := f.jobs[i%benchNomadJobs]
		out, err := f.store.JobByID(nil, j.Namespace, j.ID)
		if err != nil || out == nil {
			b.Fatalf("err=%v out=%v", err, out)
		}
	}
}

func BenchmarkNomadNodeByID(b *testing.B) {
	f := benchNomadState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := f.store.NodeByID(nil, f.nodeIDs[i%benchNomadNodes])
		if err != nil || out == nil {
			b.Fatalf("err=%v out=%v", err, out)
		}
	}
}

// Full node-table scan, as the `nomad node status` listing does.
func BenchmarkNomadListNodes(b *testing.B) {
	f := benchNomadState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		iter, err := f.store.Nodes(nil)
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for raw := iter.Next(); raw != nil; raw = iter.Next() {
			n++
		}
		if n != benchNomadNodes {
			b.Fatalf("n=%d", n)
		}
	}
}

// --- write paths ------------------------------------------------------------

// An allocation status update: the most frequent write in a busy cluster.
func BenchmarkNomadUpsertAllocUpdate(b *testing.B) {
	f := benchNomadState(b)
	j := f.jobs[0]
	base := mock.Alloc()
	base.Job = j
	base.JobID = j.ID
	base.Namespace = j.Namespace
	base.TaskGroup = j.TaskGroups[0].Name
	base.NodeID = f.nodeIDs[0]
	if err := f.store.UpsertAllocs(structs.MsgTypeTestSetup, f.idx, []*structs.Allocation{base}); err != nil {
		b.Fatal(err)
	}
	f.idx++
	states := []string{structs.AllocClientStatusRunning, structs.AllocClientStatusPending}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		up := base.Copy()
		up.ClientStatus = states[i%2]
		if err := f.store.UpsertAllocs(structs.MsgTypeTestSetup, f.idx, []*structs.Allocation{up}); err != nil {
			b.Fatal(err)
		}
		f.idx++
	}
}

// A node heartbeat, which rewrites the node row and its secondary indexes.
func BenchmarkNomadNodeHeartbeat(b *testing.B) {
	f := benchNomadState(b)
	nodes := make([]*structs.Node, benchNomadNodes)
	for i := range nodes {
		n, err := f.store.NodeByID(nil, f.nodeIDs[i])
		if err != nil || n == nil {
			b.Fatalf("err=%v", err)
		}
		nodes[i] = n
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := nodes[i%benchNomadNodes].Copy()
		n.StatusUpdatedAt = int64(i)
		if err := f.store.UpsertNode(structs.MsgTypeTestSetup, f.idx, n); err != nil {
			b.Fatal(err)
		}
		f.idx++
	}
}

// --- watch path -------------------------------------------------------------

// A client's blocking query for its allocations, woken by an alloc write.
func BenchmarkNomadWatchFire(b *testing.B) {
	f := benchNomadState(b)
	j := f.jobs[0]
	base := mock.Alloc()
	base.Job = j
	base.JobID = j.ID
	base.Namespace = j.Namespace
	base.TaskGroup = j.TaskGroups[0].Name
	base.NodeID = f.nodeIDs[0]
	if err := f.store.UpsertAllocs(structs.MsgTypeTestSetup, f.idx, []*structs.Allocation{base}); err != nil {
		b.Fatal(err)
	}
	f.idx++
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ws := memdb.NewWatchSet()
		if _, err := f.store.AllocsByNode(ws, f.nodeIDs[0]); err != nil {
			b.Fatal(err)
		}
		up := base.Copy()
		up.ClientStatus = structs.AllocClientStatusRunning
		up.ClientDescription = fmt.Sprintf("tick %d", i)
		if err := f.store.UpsertAllocs(structs.MsgTypeTestSetup, f.idx, []*structs.Allocation{up}); err != nil {
			b.Fatal(err)
		}
		f.idx++
	}
}
