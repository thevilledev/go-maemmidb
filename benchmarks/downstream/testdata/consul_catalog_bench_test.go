package state

import (
	"fmt"
	"testing"

	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/types"
	memdb "github.com/hashicorp/go-memdb"
)

// A mid-size Consul catalog: 1000 nodes, 50 distinct services, 3 service
// instances per node (so ~60 instances behind each service), one serf health
// check per node and one check per service instance.
const (
	benchNodes      = 1000
	benchServices   = 50
	benchSvcPerNode = 3
)

func benchNodeName(i int) string { return fmt.Sprintf("node-%04d", i) }
func benchSvcName(i int) string  { return fmt.Sprintf("svc-%02d", i%benchServices) }

func benchRegisterNode(tb testing.TB, s *Store, idx *uint64, i int) {
	node := benchNodeName(i)
	if err := s.EnsureNode(*idx, &structs.Node{
		Node:    node,
		Address: fmt.Sprintf("10.%d.%d.%d", i/65536%256, i/256%256, i%256),
		Meta:    map[string]string{"rack": fmt.Sprintf("rack-%d", i%16), "os": "linux"},
	}); err != nil {
		tb.Fatal(err)
	}
	*idx++
	if err := s.EnsureCheck(*idx, &structs.HealthCheck{
		Node: node, CheckID: "serfHealth", Name: "Serf Health Status", Status: api.HealthPassing,
	}); err != nil {
		tb.Fatal(err)
	}
	*idx++
	for j := 0; j < benchSvcPerNode; j++ {
		svc := benchSvcName(i + j)
		sid := fmt.Sprintf("%s-%d", svc, j)
		if err := s.EnsureService(*idx, node, &structs.NodeService{
			ID: sid, Service: svc,
			Tags: []string{"v1", fmt.Sprintf("az-%d", i%3)},
			Port: 8000 + j,
		}); err != nil {
			tb.Fatal(err)
		}
		*idx++
		if err := s.EnsureCheck(*idx, &structs.HealthCheck{
			Node: node, CheckID: types.CheckID(sid + ":alive"), Name: "alive",
			Status: api.HealthPassing, ServiceID: sid, ServiceName: svc,
		}); err != nil {
			tb.Fatal(err)
		}
		*idx++
	}
}

func benchCatalog(tb testing.TB) (*Store, uint64) {
	s := NewStateStore(nil)
	idx := uint64(1)
	for i := 0; i < benchNodes; i++ {
		benchRegisterNode(tb, s, &idx, i)
	}
	return s, idx
}

// --- read paths -------------------------------------------------------------

// The hot path behind every Consul DNS lookup and /health/service query.
func BenchmarkCatalogCheckServiceNodes(b *testing.B) {
	s, _ := benchCatalog(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, out, err := s.CheckServiceNodes(nil, benchSvcName(i), nil, "")
		if err != nil || len(out) == 0 {
			b.Fatalf("err=%v n=%d", err, len(out))
		}
	}
}

// The same query as a blocking query: a fresh WatchSet per call, which is how
// Consul actually serves long-poll service discovery.
func BenchmarkCatalogCheckServiceNodesBlocking(b *testing.B) {
	s, _ := benchCatalog(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ws := memdb.NewWatchSet()
		_, out, err := s.CheckServiceNodes(ws, benchSvcName(i), nil, "")
		if err != nil || len(out) == 0 {
			b.Fatalf("err=%v n=%d", err, len(out))
		}
	}
}

func BenchmarkCatalogServiceNodes(b *testing.B) {
	s, _ := benchCatalog(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, out, err := s.ServiceNodes(nil, benchSvcName(i), nil, "")
		if err != nil || len(out) == 0 {
			b.Fatalf("err=%v n=%d", err, len(out))
		}
	}
}

func BenchmarkCatalogNodeServices(b *testing.B) {
	s, _ := benchCatalog(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, out, err := s.NodeServices(nil, benchNodeName(i%benchNodes), nil, "")
		if err != nil || out == nil {
			b.Fatalf("err=%v out=%v", err, out)
		}
	}
}

func BenchmarkCatalogListNodes(b *testing.B) {
	s, _ := benchCatalog(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, out, err := s.Nodes(nil, nil, "")
		if err != nil || len(out) != benchNodes {
			b.Fatalf("err=%v n=%d", err, len(out))
		}
	}
}

func BenchmarkCatalogServiceList(b *testing.B) {
	s, _ := benchCatalog(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, out, err := s.ServiceList(nil, nil, "")
		if err != nil || len(out) != benchServices {
			b.Fatalf("err=%v n=%d", err, len(out))
		}
	}
}

func BenchmarkCatalogNodesByMeta(b *testing.B) {
	s, _ := benchCatalog(b)
	filter := map[string]string{"rack": "rack-3"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, out, err := s.NodesByMeta(nil, filter, nil, "")
		if err != nil || len(out) == 0 {
			b.Fatalf("err=%v n=%d", err, len(out))
		}
	}
}

// --- write paths ------------------------------------------------------------

// Node churn: register a fresh node with its services and checks, then
// deregister it. Each Ensure*/Delete* is its own memdb write transaction.
func BenchmarkCatalogNodeChurn(b *testing.B) {
	s, idx := benchCatalog(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchRegisterNode(b, s, &idx, benchNodes+(i%64))
		if err := s.DeleteNode(idx, benchNodeName(benchNodes+(i%64)), nil, ""); err != nil {
			b.Fatal(err)
		}
		idx++
	}
}

// A health check flapping between passing and critical: the single most
// frequent write in a busy Consul cluster.
func BenchmarkCatalogCheckUpdate(b *testing.B) {
	s, idx := benchCatalog(b)
	statuses := []string{api.HealthPassing, api.HealthCritical}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		node := benchNodeName(i % benchNodes)
		if err := s.EnsureCheck(idx, &structs.HealthCheck{
			Node: node, CheckID: "serfHealth", Name: "Serf Health Status",
			Status: statuses[i%2],
		}); err != nil {
			b.Fatal(err)
		}
		idx++
	}
}

// --- watch path -------------------------------------------------------------

// Establish a blocking query's watch set over a service, then perform the
// write that wakes it: the full long-poll cycle.
func BenchmarkCatalogWatchFire(b *testing.B) {
	s, idx := benchCatalog(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ws := memdb.NewWatchSet()
		if _, _, err := s.CheckServiceNodes(ws, benchSvcName(i), nil, ""); err != nil {
			b.Fatal(err)
		}
		node := benchNodeName(i % benchNodes)
		if err := s.EnsureCheck(idx, &structs.HealthCheck{
			Node: node, CheckID: "serfHealth", Name: "Serf Health Status",
			Status: api.HealthPassing, Output: fmt.Sprintf("tick %d", i),
		}); err != nil {
			b.Fatal(err)
		}
		idx++
	}
}
