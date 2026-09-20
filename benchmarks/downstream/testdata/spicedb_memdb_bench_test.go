// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package memdb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/authzed/spicedb/pkg/datastore"
	"github.com/authzed/spicedb/pkg/datastore/options"
	"github.com/authzed/spicedb/pkg/tuple"
)

// A mid-size permission graph: 2000 documents, each with 3 viewer
// relationships drawn from a pool of 50 users (6000 relationships).
const (
	benchDocs   = 2000
	benchUsers  = 50
	benchPerDoc = 3
)

func benchDocID(i int) string  { return fmt.Sprintf("doc-%05d", i) }
func benchUserID(i int) string { return fmt.Sprintf("user-%03d", i%benchUsers) }

func benchDatastore(tb testing.TB) (datastore.Datastore, datastore.Revision) {
	ds, err := NewMemdbDatastore(0, 1*time.Hour, 1*time.Hour)
	if err != nil {
		tb.Fatal(err)
	}
	ctx := context.Background()
	updates := make([]tuple.RelationshipUpdate, 0, benchDocs*benchPerDoc)
	for d := 0; d < benchDocs; d++ {
		for k := 0; k < benchPerDoc; k++ {
			updates = append(updates, tuple.Touch(tuple.MustParse(
				fmt.Sprintf("document:%s#viewer@user:%s", benchDocID(d), benchUserID(d+k)))))
		}
	}
	rev, err := ds.ReadWriteTx(ctx, func(ctx context.Context, rwt datastore.ReadWriteTransaction) error {
		return rwt.WriteRelationships(ctx, updates)
	}, options.WithDisableRetries(true))
	if err != nil {
		tb.Fatal(err)
	}
	return ds, rev
}

func drain(tb testing.TB, it datastore.RelationshipIterator) int {
	n := 0
	for _, err := range it {
		if err != nil {
			tb.Fatal(err)
		}
		n++
	}
	return n
}

// --- read paths -------------------------------------------------------------

// The permission-check hot path: all relationships on one resource.
func BenchmarkSpiceQueryByResource(b *testing.B) {
	ds, rev := benchDatastore(b)
	ctx := context.Background()
	reader := ds.SnapshotReader(rev)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		it, err := reader.QueryRelationships(ctx, datastore.RelationshipsFilter{
			OptionalResourceType: "document",
			OptionalResourceIds:  []string{benchDocID(i % benchDocs)},
		})
		if err != nil {
			b.Fatal(err)
		}
		if n := drain(b, it); n != benchPerDoc {
			b.Fatalf("n=%d", n)
		}
	}
}

// Resource + relation: the compound (namespace, relation) index.
func BenchmarkSpiceQueryByResourceAndRelation(b *testing.B) {
	ds, rev := benchDatastore(b)
	ctx := context.Background()
	reader := ds.SnapshotReader(rev)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		it, err := reader.QueryRelationships(ctx, datastore.RelationshipsFilter{
			OptionalResourceType:     "document",
			OptionalResourceIds:      []string{benchDocID(i % benchDocs)},
			OptionalResourceRelation: "viewer",
		})
		if err != nil {
			b.Fatal(err)
		}
		if n := drain(b, it); n != benchPerDoc {
			b.Fatalf("n=%d", n)
		}
	}
}

// A full namespace scan: every relationship of a type.
func BenchmarkSpiceQueryNamespaceScan(b *testing.B) {
	ds, rev := benchDatastore(b)
	ctx := context.Background()
	reader := ds.SnapshotReader(rev)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		it, err := reader.QueryRelationships(ctx, datastore.RelationshipsFilter{
			OptionalResourceType: "document",
		})
		if err != nil {
			b.Fatal(err)
		}
		if n := drain(b, it); n != benchDocs*benchPerDoc {
			b.Fatalf("n=%d", n)
		}
	}
}

// "What can this subject reach": the reverse (subject-side) index.
func BenchmarkSpiceReverseQueryBySubject(b *testing.B) {
	ds, rev := benchDatastore(b)
	ctx := context.Background()
	reader := ds.SnapshotReader(rev)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		it, err := reader.ReverseQueryRelationships(ctx, datastore.SubjectsFilter{
			SubjectType:        "user",
			OptionalSubjectIds: []string{benchUserID(i)},
		})
		if err != nil {
			b.Fatal(err)
		}
		if n := drain(b, it); n == 0 {
			b.Fatal("no results")
		}
	}
}

// --- write path -------------------------------------------------------------

// Writing a batch of relationships, each its own memdb write transaction.
func BenchmarkSpiceWriteRelationships(b *testing.B) {
	ds, _ := benchDatastore(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		updates := make([]tuple.RelationshipUpdate, 0, 10)
		for k := 0; k < 10; k++ {
			updates = append(updates, tuple.Touch(tuple.MustParse(
				fmt.Sprintf("document:new-%d-%d#viewer@user:%s", i, k, benchUserID(k)))))
		}
		if _, err := ds.ReadWriteTx(ctx, func(ctx context.Context, rwt datastore.ReadWriteTransaction) error {
			return rwt.WriteRelationships(ctx, updates)
		}, options.WithDisableRetries(true)); err != nil {
			b.Fatal(err)
		}
	}
}

// Touching an existing relationship: delete + re-insert across every index.
func BenchmarkSpiceTouchExisting(b *testing.B) {
	ds, _ := benchDatastore(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := i % benchDocs
		upd := []tuple.RelationshipUpdate{tuple.Touch(tuple.MustParse(
			fmt.Sprintf("document:%s#viewer@user:%s", benchDocID(d), benchUserID(d))))}
		if _, err := ds.ReadWriteTx(ctx, func(ctx context.Context, rwt datastore.ReadWriteTransaction) error {
			return rwt.WriteRelationships(ctx, upd)
		}, options.WithDisableRetries(true)); err != nil {
			b.Fatal(err)
		}
	}
}
