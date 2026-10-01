package quota

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// pendingRequest builds a QuotaRequest in Pending state with the given name,
// resource, and creation time.
func pendingRequest(name, resource string, createdAt time.Time) privatev1.QuotaRequest {
	qr := privatev1.QuotaRequest{}
	qr.Name = name
	qr.Namespace = "test-ns"
	qr.CreationTimestamp = metav1.NewTime(createdAt)
	qr.Spec.Resource = resource
	qr.Spec.RequestedLimit = 100
	qr.Spec.Reason = "test"
	qr.Status.Phase = privatev1.QuotaRequestPhasePending
	return qr
}

// approvedRequest builds a QuotaRequest that is already Approved.
func approvedRequest(name, resource string, createdAt time.Time) privatev1.QuotaRequest {
	qr := pendingRequest(name, resource, createdAt)
	qr.Status.Phase = privatev1.QuotaRequestPhaseApproved
	return qr
}

func quotaWithThreshold(resource string, threshold int32) *privatev1.Quota {
	return &privatev1.Quota{
		Spec: privatev1.QuotaSpec{
			Resources: []privatev1.QuotaResourceSpec{{
				Resource:             resource,
				Limit:                50,
				AutoApproveThreshold: &threshold,
			}},
		},
	}
}

var (
	t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 = t0.Add(1 * time.Minute)
	t2 = t0.Add(2 * time.Minute)
	t3 = t0.Add(3 * time.Minute)
)

// ─── PendingForResource ───────────────────────────────────────────────────────

func TestPendingForResource_Empty(t *testing.T) {
	result := PendingForResource(nil, "hostedclusters")
	assert.Empty(t, result)
}

func TestPendingForResource_FiltersResource(t *testing.T) {
	all := []privatev1.QuotaRequest{
		pendingRequest("a", "hostedclusters", t0),
		pendingRequest("b", "nodepools", t0),
		pendingRequest("c", "hostedclusters", t1),
	}
	result := PendingForResource(all, "hostedclusters")
	require.Len(t, result, 2)
	names := []string{result[0].Name, result[1].Name}
	assert.ElementsMatch(t, []string{"a", "c"}, names)
}

func TestPendingForResource_ExcludesNonPending(t *testing.T) {
	all := []privatev1.QuotaRequest{
		pendingRequest("a", "hostedclusters", t0),
		approvedRequest("b", "hostedclusters", t1),
	}
	result := PendingForResource(all, "hostedclusters")
	require.Len(t, result, 1)
	assert.Equal(t, "a", result[0].Name)
}

func TestPendingForResource_TreatsEmptyPhaseAsPending(t *testing.T) {
	qr := pendingRequest("a", "hostedclusters", t0)
	qr.Status.Phase = "" // zero value = Pending
	result := PendingForResource([]privatev1.QuotaRequest{qr}, "hostedclusters")
	require.Len(t, result, 1)
}

// ─── NewestPendingRequest ─────────────────────────────────────────────────────

func TestNewestPendingRequest_Nil_OnEmpty(t *testing.T) {
	assert.Nil(t, NewestPendingRequest(nil))
}

func TestNewestPendingRequest_SingleEntry(t *testing.T) {
	qr := pendingRequest("only", "hostedclusters", t0)
	result := NewestPendingRequest([]privatev1.QuotaRequest{qr})
	require.NotNil(t, result)
	assert.Equal(t, "only", result.Name)
}

func TestNewestPendingRequest_ReturnsMostRecent(t *testing.T) {
	all := []privatev1.QuotaRequest{
		pendingRequest("old", "hostedclusters", t0),
		pendingRequest("newer", "hostedclusters", t2),
		pendingRequest("middle", "hostedclusters", t1),
	}
	result := NewestPendingRequest(all)
	require.NotNil(t, result)
	assert.Equal(t, "newer", result.Name)
}

func TestNewestPendingRequest_TieBreakByName(t *testing.T) {
	// Same timestamp — lexicographically later name wins.
	all := []privatev1.QuotaRequest{
		pendingRequest("beta", "hostedclusters", t0),
		pendingRequest("alpha", "hostedclusters", t0),
		pendingRequest("gamma", "hostedclusters", t0),
	}
	result := NewestPendingRequest(all)
	require.NotNil(t, result)
	assert.Equal(t, "gamma", result.Name)
}

func TestNewestPendingRequest_DoesNotMutateInput(t *testing.T) {
	all := []privatev1.QuotaRequest{
		pendingRequest("b", "hostedclusters", t1),
		pendingRequest("a", "hostedclusters", t0),
	}
	originalOrder := []string{all[0].Name, all[1].Name}
	_ = NewestPendingRequest(all)
	assert.Equal(t, originalOrder[0], all[0].Name, "input slice must not be mutated")
	assert.Equal(t, originalOrder[1], all[1].Name)
}

// ─── SupersessionTargets ──────────────────────────────────────────────────────

func TestSupersessionTargets_SinglePending_IsNewest(t *testing.T) {
	current := pendingRequest("only", "hostedclusters", t0)
	toSupersede, isNewest := SupersessionTargets(&current, []privatev1.QuotaRequest{current})
	assert.Empty(t, toSupersede)
	assert.True(t, isNewest)
}

func TestSupersessionTargets_CurrentIsOldest(t *testing.T) {
	current := pendingRequest("old", "hostedclusters", t0)
	newer := pendingRequest("new", "hostedclusters", t1)
	all := []privatev1.QuotaRequest{current, newer}

	toSupersede, isNewest := SupersessionTargets(&current, all)
	require.Len(t, toSupersede, 1)
	assert.Equal(t, "old", toSupersede[0].Name)
	assert.False(t, isNewest, "current is not the newest")
}

func TestSupersessionTargets_CurrentIsNewest(t *testing.T) {
	old := pendingRequest("old", "hostedclusters", t0)
	middle := pendingRequest("middle", "hostedclusters", t1)
	current := pendingRequest("new", "hostedclusters", t2)
	all := []privatev1.QuotaRequest{old, middle, current}

	toSupersede, isNewest := SupersessionTargets(&current, all)
	require.Len(t, toSupersede, 2)
	names := []string{toSupersede[0].Name, toSupersede[1].Name}
	assert.ElementsMatch(t, []string{"old", "middle"}, names)
	assert.True(t, isNewest)
}

func TestSupersessionTargets_IgnoresOtherResources(t *testing.T) {
	// A Pending NodePool request must not interfere with HostedCluster supersession.
	current := pendingRequest("hc-new", "hostedclusters", t1)
	np := pendingRequest("np-old", "nodepools", t0)
	all := []privatev1.QuotaRequest{current, np}

	toSupersede, isNewest := SupersessionTargets(&current, all)
	assert.Empty(t, toSupersede)
	assert.True(t, isNewest)
}

func TestSupersessionTargets_IgnoresNonPending(t *testing.T) {
	current := pendingRequest("new", "hostedclusters", t1)
	approved := approvedRequest("approved", "hostedclusters", t2) // newer but Approved
	all := []privatev1.QuotaRequest{current, approved}

	// Approved request must not be treated as superseding the Pending one.
	toSupersede, isNewest := SupersessionTargets(&current, all)
	assert.Empty(t, toSupersede)
	assert.True(t, isNewest)
}

func TestSupersessionTargets_ThreeWayTie_DeterministicWinner(t *testing.T) {
	// All same timestamp — lexicographically "gamma" should win.
	alpha := pendingRequest("alpha", "hostedclusters", t0)
	beta := pendingRequest("beta", "hostedclusters", t0)
	gamma := pendingRequest("gamma", "hostedclusters", t0)
	all := []privatev1.QuotaRequest{alpha, beta, gamma}

	toSupersede, isNewest := SupersessionTargets(&gamma, all)
	require.Len(t, toSupersede, 2)
	names := []string{toSupersede[0].Name, toSupersede[1].Name}
	assert.ElementsMatch(t, []string{"alpha", "beta"}, names)
	assert.True(t, isNewest)

	// Alpha's reconcile: it is NOT the newest.
	toSupersede2, isNewest2 := SupersessionTargets(&alpha, all)
	require.Len(t, toSupersede2, 2)
	assert.False(t, isNewest2)
}

// ─── AutoApproveThreshold ─────────────────────────────────────────────────────

func TestAutoApproveThreshold_Found(t *testing.T) {
	q := quotaWithThreshold("hostedclusters", 150)
	got, ok := AutoApproveThreshold(q, "hostedclusters")
	require.True(t, ok)
	assert.Equal(t, int32(150), got)
}

func TestAutoApproveThreshold_NotFound_WrongResource(t *testing.T) {
	q := quotaWithThreshold("hostedclusters", 150)
	_, ok := AutoApproveThreshold(q, "nodepools")
	assert.False(t, ok)
}

func TestAutoApproveThreshold_NotFound_NilThreshold(t *testing.T) {
	q := &privatev1.Quota{
		Spec: privatev1.QuotaSpec{
			Resources: []privatev1.QuotaResourceSpec{{
				Resource: "hostedclusters",
				Limit:    50,
				// AutoApproveThreshold is nil
			}},
		},
	}
	_, ok := AutoApproveThreshold(q, "hostedclusters")
	assert.False(t, ok)
}

func TestAutoApproveThreshold_EmptySpec(t *testing.T) {
	_, ok := AutoApproveThreshold(&privatev1.Quota{}, "hostedclusters")
	assert.False(t, ok)
}

// ─── ShouldAutoApprove ────────────────────────────────────────────────────────

func TestShouldAutoApprove_BelowThreshold(t *testing.T) {
	assert.True(t, ShouldAutoApprove(100, 150))
}

func TestShouldAutoApprove_AtThreshold(t *testing.T) {
	assert.True(t, ShouldAutoApprove(150, 150))
}

func TestShouldAutoApprove_AboveThreshold(t *testing.T) {
	assert.False(t, ShouldAutoApprove(151, 150))
}

// ─── Note helpers ─────────────────────────────────────────────────────────────

func TestApproveNote(t *testing.T) {
	note := ApproveNote(200)
	assert.Contains(t, note, "200")
	assert.Contains(t, note, "autoApproveThreshold")
}

func TestSupersedeNote(t *testing.T) {
	note := SupersedeNote("my-newer-request")
	assert.Contains(t, note, "my-newer-request")
	assert.Contains(t, note, "superseded")
}

// ─── Multi-resource supersession isolation ────────────────────────────────────

func TestSupersessionTargets_TwoResourcesIndependent(t *testing.T) {
	// Two Pending requests: one for hostedclusters (new) and one for nodepools (old).
	// Reconciling the hostedclusters request — nodepool request must be untouched.
	hcNew := pendingRequest("hc-new", "hostedclusters", t3)
	hcOld := pendingRequest("hc-old", "hostedclusters", t0)
	npOld := pendingRequest("np-old", "nodepools", t0)
	all := []privatev1.QuotaRequest{hcNew, hcOld, npOld}

	// Reconciling hc-new: it is the newest for hostedclusters.
	toSupersede, isNewest := SupersessionTargets(&hcNew, all)
	require.Len(t, toSupersede, 1)
	assert.Equal(t, "hc-old", toSupersede[0].Name)
	assert.True(t, isNewest)
}
