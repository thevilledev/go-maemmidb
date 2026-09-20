// Copyright (c) 2026 Ville Vesilehto
// SPDX-License-Identifier: MPL-2.0

package vault

import (
	"context"
	"fmt"
	"testing"

	memdb "github.com/hashicorp/go-memdb"
	"github.com/hashicorp/vault/helper/identity"
	"github.com/hashicorp/vault/helper/namespace"
)

// A mid-size Vault identity store: 5,000 entities, one entity alias each
// spread over 4 auth mounts, and 200 groups of 50 members (so each entity
// belongs to ~2 groups).
const (
	benchEntities = 5000
	benchGroups   = 200
	benchMembers  = 50
	benchMounts   = 4
)

func benchEntityID(i int) string { return fmt.Sprintf("entity-%05d", i) }
func benchAliasID(i int) string  { return fmt.Sprintf("alias-%05d", i) }
func benchMount(i int) string    { return fmt.Sprintf("auth_userpass_%d", i%benchMounts) }
func benchAliasName(i int) string {
	return fmt.Sprintf("user-%05d@example.com", i)
}

// An IdentityStore backed only by memdb: these MemDB* helpers touch nothing
// else on the struct, so no Core is needed.
func benchIdentityStore(tb testing.TB) *IdentityStore {
	db, err := memdb.NewMemDB(identityStoreSchema(true))
	if err != nil {
		tb.Fatal(err)
	}
	i := &IdentityStore{db: db}

	txn := db.Txn(true)
	defer txn.Abort()

	for n := 0; n < benchEntities; n++ {
		entity := &identity.Entity{
			ID:          benchEntityID(n),
			Name:        fmt.Sprintf("entity-name-%05d", n),
			NamespaceID: namespace.RootNamespaceID,
			BucketKey:   fmt.Sprintf("bucket-%02d", n%256),
			Metadata:    map[string]string{"team": fmt.Sprintf("team-%d", n%32)},
			Policies:    []string{"default", fmt.Sprintf("policy-%d", n%16)},
		}
		if err := i.MemDBUpsertEntityInTxn(txn, entity); err != nil {
			tb.Fatal(err)
		}
		alias := &identity.Alias{
			ID:            benchAliasID(n),
			CanonicalID:   entity.ID,
			MountAccessor: benchMount(n),
			MountType:     "userpass",
			Name:          benchAliasName(n),
			NamespaceID:   namespace.RootNamespaceID,
		}
		if err := i.MemDBUpsertAliasInTxn(txn, alias, false); err != nil {
			tb.Fatal(err)
		}
	}

	for g := 0; g < benchGroups; g++ {
		members := make([]string, benchMembers)
		for m := 0; m < benchMembers; m++ {
			members[m] = benchEntityID((g*benchMembers + m) % benchEntities)
		}
		group := &identity.Group{
			ID:              fmt.Sprintf("group-%04d", g),
			Name:            fmt.Sprintf("group-name-%04d", g),
			NamespaceID:     namespace.RootNamespaceID,
			BucketKey:       fmt.Sprintf("gbucket-%02d", g%64),
			MemberEntityIDs: members,
			Policies:        []string{fmt.Sprintf("group-policy-%d", g%8)},
			Type:            groupTypeInternal,
		}
		if err := i.MemDBUpsertGroupInTxn(txn, group); err != nil {
			tb.Fatal(err)
		}
	}
	txn.Commit()
	return i
}

// --- read paths -------------------------------------------------------------

func BenchmarkVaultEntityByID(b *testing.B) {
	is := benchIdentityStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		e, err := is.MemDBEntityByID(benchEntityID(n%benchEntities), false)
		if err != nil || e == nil {
			b.Fatalf("err=%v e=%v", err, e)
		}
	}
}

// The login path: an auth mount resolves (mount accessor, alias name) to an
// entity alias on every authentication.
func BenchmarkVaultAliasByFactors(b *testing.B) {
	is := benchIdentityStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		i := n % benchEntities
		a, err := is.MemDBAliasByFactors(benchMount(i), benchAliasName(i), false, false)
		if err != nil || a == nil {
			b.Fatalf("err=%v a=%v", err, a)
		}
	}
}

func BenchmarkVaultEntityByName(b *testing.B) {
	is := benchIdentityStore(b)
	ctx := namespace.RootContext(context.Background())
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		e, err := is.MemDBEntityByName(ctx, fmt.Sprintf("entity-name-%05d", n%benchEntities), false)
		if err != nil || e == nil {
			b.Fatalf("err=%v e=%v", err, e)
		}
	}
}

func BenchmarkVaultEntityByAliasID(b *testing.B) {
	is := benchIdentityStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		e, err := is.MemDBEntityByAliasID(benchAliasID(n%benchEntities), false)
		if err != nil || e == nil {
			b.Fatalf("err=%v e=%v", err, e)
		}
	}
}

// Policy resolution: which groups is this entity a member of. This is a
// multi-value (StringSlice) index lookup.
func BenchmarkVaultGroupsByMemberEntityID(b *testing.B) {
	is := benchIdentityStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		g, err := is.MemDBGroupsByMemberEntityID(benchEntityID(n%benchEntities), false, false)
		if err != nil || len(g) == 0 {
			b.Fatalf("err=%v n=%d", err, len(g))
		}
	}
}

// What Vault actually does per authenticated request: resolve the alias, load
// the entity, then collect its groups for policy evaluation.
func BenchmarkVaultLoginLookup(b *testing.B) {
	is := benchIdentityStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		i := n % benchEntities
		alias, err := is.MemDBAliasByFactors(benchMount(i), benchAliasName(i), false, false)
		if err != nil || alias == nil {
			b.Fatalf("alias err=%v", err)
		}
		entity, err := is.MemDBEntityByID(alias.CanonicalID, false)
		if err != nil || entity == nil {
			b.Fatalf("entity err=%v", err)
		}
		groups, err := is.MemDBGroupsByMemberEntityID(entity.ID, false, false)
		if err != nil || len(groups) == 0 {
			b.Fatalf("groups err=%v n=%d", err, len(groups))
		}
	}
}

// --- write paths ------------------------------------------------------------

// An entity update: delete + re-insert across every index, in its own
// transaction, as an identity write does.
func BenchmarkVaultUpsertEntity(b *testing.B) {
	is := benchIdentityStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		i := n % benchEntities
		entity := &identity.Entity{
			ID:          benchEntityID(i),
			Name:        fmt.Sprintf("entity-name-%05d", i),
			NamespaceID: namespace.RootNamespaceID,
			BucketKey:   fmt.Sprintf("bucket-%02d", i%256),
			Metadata:    map[string]string{"team": fmt.Sprintf("team-%d", i%32), "seq": fmt.Sprint(n)},
			Policies:    []string{"default", fmt.Sprintf("policy-%d", i%16)},
		}
		txn := is.db.Txn(true)
		if err := is.MemDBUpsertEntityInTxn(txn, entity); err != nil {
			txn.Abort()
			b.Fatal(err)
		}
		txn.Commit()
	}
}

// A group membership update, which rewrites a 50-entry multi-value index.
func BenchmarkVaultUpsertGroup(b *testing.B) {
	is := benchIdentityStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		g := n % benchGroups
		members := make([]string, benchMembers)
		for m := 0; m < benchMembers; m++ {
			members[m] = benchEntityID((g*benchMembers + m) % benchEntities)
		}
		group := &identity.Group{
			ID:              fmt.Sprintf("group-%04d", g),
			Name:            fmt.Sprintf("group-name-%04d", g),
			NamespaceID:     namespace.RootNamespaceID,
			BucketKey:       fmt.Sprintf("gbucket-%02d", g%64),
			MemberEntityIDs: members,
			Policies:        []string{fmt.Sprintf("group-policy-%d", g%8)},
			Type:            groupTypeInternal,
		}
		txn := is.db.Txn(true)
		if err := is.MemDBUpsertGroupInTxn(txn, group); err != nil {
			txn.Abort()
			b.Fatal(err)
		}
		txn.Commit()
	}
}
