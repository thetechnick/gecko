package quota

// Tests for the controller-runtime Reconcile methods, ensureQuota,
// countResources, approve, and updateQuotaLimit — the "thin orchestration"
// layer that cannot be exercised by the pure-logic unit tests.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openshift-online/gecko/controllers/util/logger"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	quotapkg "github.com/openshift-online/gecko/platform-api/quota"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ─── mock client ─────────────────────────────────────────────────────────────

// fakeClient is a simple in-memory client.Client for testing. It stores a
// fixed set of objects keyed by (GVK, namespace/name) and records calls to
// Create, Update, Status().Update, and List.
type fakeClient struct {
	objects       map[string]client.Object
	createErr     map[string]error
	updateErr     map[string]error
	statusUpdates []client.Object
	updates       []client.Object
	creates       []client.Object
	listErr       error
	getErr        map[string]error
	statusWriter  *fakeStatusWriter
	// lists[gvk.Kind] returns a list of items to populate List calls
	lists map[string][]client.Object
}

func newFakeClient() *fakeClient {
	c := &fakeClient{
		objects:   make(map[string]client.Object),
		createErr: make(map[string]error),
		updateErr: make(map[string]error),
		getErr:    make(map[string]error),
		lists:     make(map[string][]client.Object),
	}
	c.statusWriter = &fakeStatusWriter{client: c}
	return c
}

func objKey(obj client.Object) string {
	return fmt.Sprintf("%T/%s/%s", obj, obj.GetNamespace(), obj.GetName())
}

func (c *fakeClient) store(obj client.Object) {
	c.objects[objKey(obj)] = obj
}

func (c *fakeClient) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	k := fmt.Sprintf("%T/%s/%s", obj, key.Namespace, key.Name)
	if err := c.getErr[k]; err != nil {
		return err
	}
	stored, ok := c.objects[k]
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "resource"}, key.Name)
	}
	// Copy the stored object into obj using JSON round-trip via runtime.
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(stored)
	if err != nil {
		return err
	}
	return runtime.DefaultUnstructuredConverter.FromUnstructured(data, obj)
}

func (c *fakeClient) Create(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
	if err := c.createErr[objKey(obj)]; err != nil {
		return err
	}
	if _, exists := c.objects[objKey(obj)]; exists {
		return apierrors.NewAlreadyExists(schema.GroupResource{}, obj.GetName())
	}
	c.store(obj)
	c.creates = append(c.creates, obj)
	return nil
}

func (c *fakeClient) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	if err := c.updateErr[objKey(obj)]; err != nil {
		return err
	}
	c.store(obj)
	c.updates = append(c.updates, obj)
	return nil
}

func (c *fakeClient) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if c.listErr != nil {
		return c.listErr
	}
	lo := &client.ListOptions{}
	for _, o := range opts {
		o.ApplyToList(lo)
	}
	ns := ""
	if lo.Namespace != "" {
		ns = lo.Namespace
	}

	switch l := list.(type) {
	case *privatev1.ClusterList:
		for _, obj := range c.lists["Cluster"] {
			if ns == "" || obj.GetNamespace() == ns {
				l.Items = append(l.Items, *obj.(*privatev1.Cluster))
			}
		}
	case *privatev1.NodePoolList:
		for _, obj := range c.lists["NodePool"] {
			if ns == "" || obj.GetNamespace() == ns {
				l.Items = append(l.Items, *obj.(*privatev1.NodePool))
			}
		}
	case *privatev1.QuotaRequestList:
		for _, obj := range c.lists["QuotaRequest"] {
			if ns == "" || obj.GetNamespace() == ns {
				l.Items = append(l.Items, *obj.(*privatev1.QuotaRequest))
			}
		}
	}
	return nil
}

func (c *fakeClient) Status() client.SubResourceWriter { return c.statusWriter }
func (c *fakeClient) Delete(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
	return nil
}
func (c *fakeClient) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
	return nil
}
func (c *fakeClient) DeleteAllOf(_ context.Context, _ client.Object, _ ...client.DeleteAllOfOption) error {
	return nil
}
func (c *fakeClient) Apply(_ context.Context, _ runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
	return nil
}
func (c *fakeClient) SubResource(_ string) client.SubResourceClient { return nil }
func (c *fakeClient) Scheme() *runtime.Scheme                       { return nil }
func (c *fakeClient) RESTMapper() meta.RESTMapper                   { return nil }
func (c *fakeClient) GroupVersionKindFor(_ runtime.Object) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, nil
}
func (c *fakeClient) IsObjectNamespaced(_ runtime.Object) (bool, error) { return false, nil }

type fakeStatusWriter struct {
	client *fakeClient
	err    error
}

func (w *fakeStatusWriter) Update(_ context.Context, obj client.Object, _ ...client.SubResourceUpdateOption) error {
	if w.err != nil {
		return w.err
	}
	w.client.store(obj)
	w.client.statusUpdates = append(w.client.statusUpdates, obj)
	return nil
}
func (w *fakeStatusWriter) Create(_ context.Context, _ client.Object, _ client.Object, _ ...client.SubResourceCreateOption) error {
	return nil
}
func (w *fakeStatusWriter) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	return nil
}
func (w *fakeStatusWriter) Apply(_ context.Context, _ runtime.ApplyConfiguration, _ ...client.SubResourceApplyOption) error {
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func testLog(t *testing.T) logger.Logger {
	t.Helper()
	log, err := logger.NewLogger(logger.Config{
		Level: "error", Format: logger.FormatText, Component: "test",
	})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return log
}

func makeQuota(ns string, clusterLimit, nodepoolLimit int32) *privatev1.Quota {
	q := &privatev1.Quota{}
	q.Name = "default"
	q.Namespace = ns
	q.Spec.Resources = []privatev1.QuotaResourceSpec{
		{Resource: "hostedclusters", Limit: clusterLimit},
		{Resource: "nodepools", Limit: nodepoolLimit},
	}
	return q
}

func makeQuotaWithThreshold(ns, resource string, limit, threshold int32) *privatev1.Quota {
	q := &privatev1.Quota{}
	q.Name = "default"
	q.Namespace = ns
	q.Spec.Resources = []privatev1.QuotaResourceSpec{{
		Resource:             resource,
		Limit:                limit,
		AutoApproveThreshold: &threshold,
	}}
	return q
}

func makeQR(name, ns, resource string, requested int32) *privatev1.QuotaRequest {
	qr := &privatev1.QuotaRequest{}
	qr.Name = name
	qr.Namespace = ns
	qr.Spec.Resource = resource
	qr.Spec.RequestedLimit = requested
	qr.Spec.Reason = "test"
	return qr
}

func quotaReq(ns, name string) reconcile.Request {
	return reconcile.Request{NamespacedName: client.ObjectKey{Namespace: ns, Name: name}}
}

// ─── QuotaStatusReconciler.Reconcile ─────────────────────────────────────────

func TestQuotaStatusReconcile_CreatesQuotaWhenAbsent(t *testing.T) {
	c := newFakeClient()
	// No Quota in store; 2 clusters.
	c.lists["Cluster"] = []client.Object{
		clusterObj("ns", "c1"), clusterObj("ns", "c2"),
	}

	r := NewQuotaStatusReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "default"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Quota was created with base defaults.
	if len(c.creates) == 0 {
		t.Fatal("expected Quota to be created")
	}
	q, ok := c.creates[0].(*privatev1.Quota)
	if !ok {
		t.Fatalf("created object is %T, want *Quota", c.creates[0])
	}
	if q.Name != "default" || q.Namespace != "ns" {
		t.Errorf("created Quota has wrong key: %s/%s", q.Namespace, q.Name)
	}
	if q.Spec.Resources[0].Limit != quotapkg.DefaultClusterLimit {
		t.Errorf("default cluster limit: got %d, want %d", q.Spec.Resources[0].Limit, quotapkg.DefaultClusterLimit)
	}

	// Status was updated with the cluster count.
	if len(c.statusUpdates) == 0 {
		t.Fatal("expected status update")
	}
	updated := c.statusUpdates[len(c.statusUpdates)-1].(*privatev1.Quota)
	if got := resourceCount(updated.Status, "hostedclusters"); got != 2 {
		t.Errorf("hostedclusters current: got %d, want 2", got)
	}
}

func TestQuotaStatusReconcile_UpdatesExistingStatus(t *testing.T) {
	q := makeQuota("ns", 50, 500)
	c := newFakeClient()
	c.store(q)
	c.lists["Cluster"] = []client.Object{
		clusterObj("ns", "c1"), clusterObj("ns", "c2"), clusterObj("ns", "c3"),
	}
	c.lists["NodePool"] = []client.Object{nodepoolObj("ns", "np1")}

	r := NewQuotaStatusReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "default"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(c.statusUpdates) == 0 {
		t.Fatal("expected status update")
	}
	updated := c.statusUpdates[0].(*privatev1.Quota)
	if got := resourceCount(updated.Status, "hostedclusters"); got != 3 {
		t.Errorf("hostedclusters: got %d, want 3", got)
	}
	if got := resourceCount(updated.Status, "nodepools"); got != 1 {
		t.Errorf("nodepools: got %d, want 1", got)
	}
}

func TestQuotaStatusReconcile_SkipsWriteWhenUnchanged(t *testing.T) {
	q := makeQuota("ns", 50, 500)
	// Pre-populate status that already matches the expected outcome.
	q.Status.Resources = []privatev1.QuotaResourceStatus{
		{Resource: "hostedclusters", Current: 1},
		{Resource: "nodepools", Current: 0},
	}
	q.Status.Conditions = BuildHighUsageCondition(nil, nil, 0, metav1.Now())
	c := newFakeClient()
	c.store(q)
	c.lists["Cluster"] = []client.Object{clusterObj("ns", "c1")}

	r := NewQuotaStatusReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "default"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(c.statusUpdates) != 0 {
		t.Errorf("expected no status write when nothing changed, got %d updates", len(c.statusUpdates))
	}
}

func TestQuotaStatusReconcile_SetsHighUsageCondition(t *testing.T) {
	// 40 clusters against a limit of 50 → 80% → HighUsage=True.
	q := makeQuota("ns", 50, 500)
	c := newFakeClient()
	c.store(q)
	clusters := make([]client.Object, 40)
	for i := range clusters {
		clusters[i] = clusterObj("ns", fmt.Sprintf("c%d", i))
	}
	c.lists["Cluster"] = clusters

	r := NewQuotaStatusReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "default"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(c.statusUpdates) == 0 {
		t.Fatal("expected status update")
	}
	updated := c.statusUpdates[0].(*privatev1.Quota)
	cond := findCond(updated.Status.Conditions, privatev1.QuotaConditionHighUsage)
	if cond == nil {
		t.Fatal("HighUsage condition missing from status")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("HighUsage status: got %s, want True", cond.Status)
	}
}

func TestQuotaStatusReconcile_QuotaNotFound_NoError(t *testing.T) {
	c := newFakeClient()
	// Totally empty — no Quota, no clusters.
	r := NewQuotaStatusReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "default"))
	if err != nil {
		t.Errorf("expected no error on empty namespace, got: %v", err)
	}
}

// ─── Reconciler (QuotaRequest).Reconcile ─────────────────────────────────────

func TestQRReconcile_NotFound_NoError(t *testing.T) {
	c := newFakeClient()
	r := NewReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "missing"))
	if err != nil {
		t.Errorf("expected no error for missing QuotaRequest, got: %v", err)
	}
}

func TestQRReconcile_AlreadyApproved_NoAction(t *testing.T) {
	qr := makeQR("req", "ns", "hostedclusters", 100)
	qr.Status.Phase = privatev1.QuotaRequestPhaseApproved
	c := newFakeClient()
	c.store(qr)

	r := NewReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "req"))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(c.statusUpdates) != 0 {
		t.Errorf("expected no status updates for Approved request, got %d", len(c.statusUpdates))
	}
}

func TestQRReconcile_AutoApproves_WithinThreshold(t *testing.T) {
	qr := makeQR("req", "ns", "hostedclusters", 100)
	quota := makeQuotaWithThreshold("ns", "hostedclusters", 50, 150)

	c := newFakeClient()
	c.store(qr)
	c.store(quota)
	c.lists["QuotaRequest"] = []client.Object{qr}

	r := NewReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "req"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// QuotaRequest status should be Approved.
	if len(c.statusUpdates) == 0 {
		t.Fatal("expected status update for auto-approval")
	}
	updated, ok := c.statusUpdates[0].(*privatev1.QuotaRequest)
	if !ok {
		t.Fatalf("status update is %T, want *QuotaRequest", c.statusUpdates[0])
	}
	if updated.Status.Phase != privatev1.QuotaRequestPhaseApproved {
		t.Errorf("phase: got %s, want Approved", updated.Status.Phase)
	}
	if updated.Status.AutoApproved == nil || !*updated.Status.AutoApproved {
		t.Error("expected AutoApproved=true")
	}
	if updated.Status.ApprovedLimit == nil || *updated.Status.ApprovedLimit != 100 {
		t.Errorf("approvedLimit: got %v, want 100", updated.Status.ApprovedLimit)
	}

	// Quota limit should be raised.
	if len(c.updates) == 0 {
		t.Fatal("expected Quota spec update")
	}
	updatedQuota, ok := c.updates[0].(*privatev1.Quota)
	if !ok {
		t.Fatalf("spec update is %T, want *Quota", c.updates[0])
	}
	if l := specLimit(updatedQuota, "hostedclusters"); l != 100 {
		t.Errorf("quota limit: got %d, want 100", l)
	}
}

func TestQRReconcile_LeavesAboveThresholdPending(t *testing.T) {
	qr := makeQR("req", "ns", "hostedclusters", 200) // 200 > threshold 150
	quota := makeQuotaWithThreshold("ns", "hostedclusters", 50, 150)

	c := newFakeClient()
	c.store(qr)
	c.store(quota)
	c.lists["QuotaRequest"] = []client.Object{qr}

	r := NewReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "req"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(c.statusUpdates) != 0 {
		t.Errorf("expected no status update for above-threshold request, got %d", len(c.statusUpdates))
	}
}

func TestQRReconcile_SupersedesOlderPending(t *testing.T) {
	base := fixedTimestamp()
	t0 := metav1.NewTime(base)
	t1 := metav1.NewTime(base.Add(time.Second))

	old := makeQR("old-req", "ns", "hostedclusters", 60)
	old.CreationTimestamp = t0
	newer := makeQR("new-req", "ns", "hostedclusters", 80)
	newer.CreationTimestamp = t1

	quota := makeQuotaWithThreshold("ns", "hostedclusters", 50, 150)

	c := newFakeClient()
	c.store(old)
	c.store(newer)
	c.store(quota)
	c.lists["QuotaRequest"] = []client.Object{old, newer}

	r := NewReconciler(testLog(t), c)
	// Reconcile the newer request — it should supersede the old one.
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "new-req"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// One of the status updates should be the old request going to Superseded.
	superseded := false
	approved := false
	for _, u := range c.statusUpdates {
		if qr, ok := u.(*privatev1.QuotaRequest); ok {
			switch qr.Status.Phase {
			case privatev1.QuotaRequestPhaseSuperseded:
				superseded = true
				if qr.Status.SupersededBy == nil || *qr.Status.SupersededBy != "new-req" {
					t.Errorf("supersededBy: got %v, want \"new-req\"", qr.Status.SupersededBy)
				}
			case privatev1.QuotaRequestPhaseApproved:
				approved = true
			}
		}
	}
	if !superseded {
		t.Error("old request was not superseded")
	}
	if !approved {
		t.Error("new request was not approved")
	}
}

func TestQRReconcile_OldRequestSupersedesItself(t *testing.T) {
	base := fixedTimestamp()
	t0 := metav1.NewTime(base)
	t1 := metav1.NewTime(base.Add(time.Second))

	old := makeQR("old-req", "ns", "hostedclusters", 60)
	old.CreationTimestamp = t0
	newer := makeQR("new-req", "ns", "hostedclusters", 80)
	newer.CreationTimestamp = t1

	quota := makeQuotaWithThreshold("ns", "hostedclusters", 50, 150)

	c := newFakeClient()
	c.store(old)
	c.store(newer)
	c.store(quota)
	c.lists["QuotaRequest"] = []client.Object{old, newer}

	r := NewReconciler(testLog(t), c)
	// Reconcile the OLD request — it should identify itself as stale and stop.
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "old-req"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Old request should be Superseded; new request must not have been approved
	// by this reconcile (it will be approved when new-req is reconciled).
	superseded := false
	for _, u := range c.statusUpdates {
		if qr, ok := u.(*privatev1.QuotaRequest); ok {
			if qr.Name == "old-req" && qr.Status.Phase == privatev1.QuotaRequestPhaseSuperseded {
				superseded = true
			}
			if qr.Name == "new-req" && qr.Status.Phase == privatev1.QuotaRequestPhaseApproved {
				t.Error("new request should not be approved when reconciling the old request")
			}
		}
	}
	if !superseded {
		t.Error("old request was not superseded by reconciling itself")
	}
}

func TestQRReconcile_NoQuota_LeavePending(t *testing.T) {
	qr := makeQR("req", "ns", "hostedclusters", 100)
	c := newFakeClient()
	c.store(qr)
	c.lists["QuotaRequest"] = []client.Object{qr}
	// No Quota object in the store.

	r := NewReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "req"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.statusUpdates) != 0 {
		t.Errorf("expected no status update when no Quota exists, got %d", len(c.statusUpdates))
	}
}

func TestQRReconcile_NoThreshold_LeavePending(t *testing.T) {
	qr := makeQR("req", "ns", "hostedclusters", 100)
	// Quota exists but has no autoApproveThreshold.
	quota := makeQuota("ns", 50, 500)
	c := newFakeClient()
	c.store(qr)
	c.store(quota)
	c.lists["QuotaRequest"] = []client.Object{qr}

	r := NewReconciler(testLog(t), c)
	_, err := r.Reconcile(context.Background(), quotaReq("ns", "req"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.statusUpdates) != 0 {
		t.Errorf("expected no auto-approval without threshold, got %d updates", len(c.statusUpdates))
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func clusterObj(ns, name string) client.Object {
	c := &privatev1.Cluster{}
	c.Name = name
	c.Namespace = ns
	return c
}

func nodepoolObj(ns, name string) client.Object {
	np := &privatev1.NodePool{}
	np.Name = name
	np.Namespace = ns
	return np
}

func resourceCount(s privatev1.QuotaStatus, resource string) int32 {
	for _, r := range s.Resources {
		if r.Resource == resource {
			return r.Current
		}
	}
	return -1
}

func specLimit(q *privatev1.Quota, resource string) int32 {
	for _, r := range q.Spec.Resources {
		if r.Resource == resource {
			return r.Limit
		}
	}
	return -1
}

func findCond(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}

func fixedTimestamp() time.Time {
	return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
}

// Compile-time check: fakeClient must implement client.Client.
var _ client.Client = (*fakeClient)(nil)
