package quota

import (
	"context"
	"testing"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := privatev1.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}

func quotaStore(t *testing.T) *memory.MemoryStore {
	t.Helper()
	return memory.NewMemoryStore("quotas", newScheme(),
		privatev1.GroupVersion.WithKind("Quota"))
}

func clusterStore(t *testing.T) *memory.MemoryStore {
	t.Helper()
	return memory.NewMemoryStore("clusters", newScheme(),
		privatev1.GroupVersion.WithKind("Cluster"))
}

func nodepoolStore(t *testing.T) *memory.MemoryStore {
	t.Helper()
	return memory.NewMemoryStore("nodepools", newScheme(),
		privatev1.GroupVersion.WithKind("NodePool"))
}

func mustCreate(t *testing.T, store *memory.MemoryStore, obj client.Object) {
	t.Helper()
	if err := store.Create(context.Background(), obj); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
}

func newQuota(ns string, clusterLimit, nodepoolLimit int32) *privatev1.Quota {
	return &privatev1.Quota{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gcp.managed.openshift.io/v1", Kind: "Quota"},
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
		Spec: privatev1.QuotaSpec{
			Resources: []privatev1.QuotaResourceSpec{
				{Resource: "hostedclusters", Limit: clusterLimit},
				{Resource: "nodepools", Limit: nodepoolLimit},
			},
		},
	}
}

func newQuotaWithManual(ns string, limit, manual int32, resource string) *privatev1.Quota {
	return &privatev1.Quota{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gcp.managed.openshift.io/v1", Kind: "Quota"},
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
		Spec: privatev1.QuotaSpec{
			Resources: []privatev1.QuotaResourceSpec{
				{Resource: resource, Limit: limit, ManualLimit: &manual},
			},
		},
	}
}

func newCluster(ns, name string) *privatev1.Cluster {
	return &privatev1.Cluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gcp.managed.openshift.io/v1", Kind: "Cluster"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
}

func newNodePool(ns, name string) *privatev1.NodePool {
	return &privatev1.NodePool{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gcp.managed.openshift.io/v1", Kind: "NodePool"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
}

func enforcer(t *testing.T, qs, cs, ns *memory.MemoryStore) *StoreEnforcer {
	t.Helper()
	return NewStoreEnforcer(qs, cs, ns)
}

// ─── EffectiveLimit ───────────────────────────────────────────────────────────

func TestEffectiveLimit_ReturnsSpecLimit(t *testing.T) {
	q := newQuota("ns", 30, 200)
	if got := EffectiveLimit(q, "hostedclusters"); got != 30 {
		t.Errorf("hostedclusters limit: got %d, want 30", got)
	}
	if got := EffectiveLimit(q, "nodepools"); got != 200 {
		t.Errorf("nodepools limit: got %d, want 200", got)
	}
}

func TestEffectiveLimit_ManualOverridesSpec(t *testing.T) {
	q := newQuotaWithManual("ns", 50, 100, "hostedclusters")
	if got := EffectiveLimit(q, "hostedclusters"); got != 100 {
		t.Errorf("manual limit: got %d, want 100", got)
	}
}

func TestEffectiveLimit_FallsBackToDefault(t *testing.T) {
	q := &privatev1.Quota{}
	if got := EffectiveLimit(q, "hostedclusters"); got != DefaultClusterLimit {
		t.Errorf("default cluster limit: got %d, want %d", got, DefaultClusterLimit)
	}
	if got := EffectiveLimit(q, "nodepools"); got != DefaultNodePoolLimit {
		t.Errorf("default nodepool limit: got %d, want %d", got, DefaultNodePoolLimit)
	}
}

// ─── DefaultLimit ─────────────────────────────────────────────────────────────

func TestDefaultLimit_KnownResources(t *testing.T) {
	if got := DefaultLimit("hostedclusters"); got != DefaultClusterLimit {
		t.Errorf("got %d, want %d", got, DefaultClusterLimit)
	}
	if got := DefaultLimit("nodepools"); got != DefaultNodePoolLimit {
		t.Errorf("got %d, want %d", got, DefaultNodePoolLimit)
	}
}

func TestDefaultLimit_UnknownResourceUsesClusterDefault(t *testing.T) {
	if got := DefaultLimit("unknown"); got != DefaultClusterLimit {
		t.Errorf("got %d, want %d", got, DefaultClusterLimit)
	}
}

// ─── QuotaExceededError ───────────────────────────────────────────────────────

func TestQuotaExceededError_ContainsExpectedFields(t *testing.T) {
	err := QuotaExceededError("acme-prod", "hostedclusters", 50, 50)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	msg := err.Error()
	for _, want := range []string{"acme-prod", "50", "hostedclusters", "QuotaRequest"} {
		if !contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}

// ─── StoreEnforcer.CheckQuota ─────────────────────────────────────────────────

func TestCheckQuota_AllowedWhenBelowLimit(t *testing.T) {
	ctx := context.Background()
	qs, cs, ns := quotaStore(t), clusterStore(t), nodepoolStore(t)

	mustCreate(t, qs, newQuota("ns", 5, 10))
	mustCreate(t, cs, newCluster("ns", "c1"))
	mustCreate(t, cs, newCluster("ns", "c2"))

	e := enforcer(t, qs, cs, ns)
	if err := e.CheckQuota(ctx, "ns", "hostedclusters"); err != nil {
		t.Errorf("expected no error below limit, got: %v", err)
	}
}

func TestCheckQuota_RejectedWhenAtLimit(t *testing.T) {
	ctx := context.Background()
	qs, cs, ns := quotaStore(t), clusterStore(t), nodepoolStore(t)

	mustCreate(t, qs, newQuota("ns", 2, 10))
	mustCreate(t, cs, newCluster("ns", "c1"))
	mustCreate(t, cs, newCluster("ns", "c2"))

	e := enforcer(t, qs, cs, ns)
	if err := e.CheckQuota(ctx, "ns", "hostedclusters"); err == nil {
		t.Error("expected quota exceeded error at limit, got nil")
	}
}

func TestCheckQuota_UsesDefaultWhenNoQuotaObject(t *testing.T) {
	ctx := context.Background()
	// No quota object in the store — effectiveLimit falls back to DefaultClusterLimit.
	qs, cs, ns := quotaStore(t), clusterStore(t), nodepoolStore(t)

	// Populate fewer clusters than the default limit — should be allowed.
	for i := 0; i < 3; i++ {
		mustCreate(t, cs, newCluster("ns", objName("c", i)))
	}

	e := enforcer(t, qs, cs, ns)
	if err := e.CheckQuota(ctx, "ns", "hostedclusters"); err != nil {
		t.Errorf("expected no error with counts below default limit, got: %v", err)
	}
}

func TestCheckQuota_ManualLimitApplied(t *testing.T) {
	ctx := context.Background()
	qs, cs, ns := quotaStore(t), clusterStore(t), nodepoolStore(t)

	// manualLimit=1 overrides Limit=50.
	mustCreate(t, qs, newQuotaWithManual("ns", 50, 1, "hostedclusters"))
	mustCreate(t, cs, newCluster("ns", "c1"))

	e := enforcer(t, qs, cs, ns)
	if err := e.CheckQuota(ctx, "ns", "hostedclusters"); err == nil {
		t.Error("expected quota exceeded with manualLimit=1 and 1 cluster, got nil")
	}
}

func TestCheckQuota_NodePoolsUseSeparateStore(t *testing.T) {
	ctx := context.Background()
	qs, cs, ns := quotaStore(t), clusterStore(t), nodepoolStore(t)

	mustCreate(t, qs, newQuota("ns", 50, 2))
	mustCreate(t, ns, newNodePool("ns", "np1"))
	mustCreate(t, ns, newNodePool("ns", "np2"))

	e := enforcer(t, qs, cs, ns)
	// nodepools at limit → rejected.
	if err := e.CheckQuota(ctx, "ns", "nodepools"); err == nil {
		t.Error("expected nodepools quota exceeded, got nil")
	}
	// hostedclusters unaffected — 0 clusters, limit=50.
	if err := e.CheckQuota(ctx, "ns", "hostedclusters"); err != nil {
		t.Errorf("expected hostedclusters allowed, got: %v", err)
	}
}

func TestCheckQuota_NamespaceIsolated(t *testing.T) {
	ctx := context.Background()
	qs, cs, ns := quotaStore(t), clusterStore(t), nodepoolStore(t)

	// ns-a has a tight limit and 2 clusters.
	mustCreate(t, qs, newQuota("ns-a", 2, 10))
	mustCreate(t, cs, newCluster("ns-a", "c1"))
	mustCreate(t, cs, newCluster("ns-a", "c2"))

	// ns-b is empty with its own quota.
	mustCreate(t, qs, newQuota("ns-b", 10, 10))

	e := enforcer(t, qs, cs, ns)
	if err := e.CheckQuota(ctx, "ns-a", "hostedclusters"); err == nil {
		t.Error("expected ns-a to be at limit")
	}
	if err := e.CheckQuota(ctx, "ns-b", "hostedclusters"); err != nil {
		t.Errorf("expected ns-b to have room, got: %v", err)
	}
}

func TestCheckQuota_UnknownResource(t *testing.T) {
	ctx := context.Background()
	qs, cs, ns := quotaStore(t), clusterStore(t), nodepoolStore(t)
	e := enforcer(t, qs, cs, ns)
	if err := e.CheckQuota(ctx, "ns", "widgets"); err == nil {
		t.Error("expected error for unknown resource, got nil")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func objName(prefix string, i int) string {
	return prefix + string(rune('0'+i))
}

// Ensure the MemoryStore GVK registration works for NodePool (it normally
// needs the scheme to know the list type). This is a compile-time check.
var _ = schema.GroupVersionKind{}
