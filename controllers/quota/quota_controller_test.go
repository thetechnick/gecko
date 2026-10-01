package quota

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	quotapkg "github.com/openshift-online/gecko/platform-api/quota"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func quotaWithLimits(clusterLimit, nodepoolLimit int32) *privatev1.Quota {
	return &privatev1.Quota{
		Spec: privatev1.QuotaSpec{
			Resources: []privatev1.QuotaResourceSpec{
				{Resource: "hostedclusters", Limit: clusterLimit},
				{Resource: "nodepools", Limit: nodepoolLimit},
			},
		},
	}
}

func quotaWithManualLimits(clusterManual, nodepoolManual int32) *privatev1.Quota {
	return &privatev1.Quota{
		Spec: privatev1.QuotaSpec{
			Resources: []privatev1.QuotaResourceSpec{
				{Resource: "hostedclusters", Limit: 50, ManualLimit: &clusterManual},
				{Resource: "nodepools", Limit: 500, ManualLimit: &nodepoolManual},
			},
		},
	}
}

func counts(clusters, nodepools int32) map[string]int32 {
	return map[string]int32{
		"hostedclusters": clusters,
		"nodepools":      nodepools,
	}
}

func fixedNow() metav1.Time {
	return metav1.NewTime(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
}

// ─── EffectiveLimitFromSpec ───────────────────────────────────────────────────

func TestEffectiveLimitFromSpec_UsesLimit(t *testing.T) {
	q := quotaWithLimits(30, 200)
	assert.Equal(t, int32(30), EffectiveLimitFromSpec(q, "hostedclusters"))
	assert.Equal(t, int32(200), EffectiveLimitFromSpec(q, "nodepools"))
}

func TestEffectiveLimitFromSpec_ManualLimitTakesPrecedence(t *testing.T) {
	q := quotaWithManualLimits(100, 1000)
	assert.Equal(t, int32(100), EffectiveLimitFromSpec(q, "hostedclusters"))
	assert.Equal(t, int32(1000), EffectiveLimitFromSpec(q, "nodepools"))
}

func TestEffectiveLimitFromSpec_FallsBackToDefault(t *testing.T) {
	q := &privatev1.Quota{} // empty spec
	assert.Equal(t, quotapkg.DefaultClusterLimit, EffectiveLimitFromSpec(q, "hostedclusters"))
	assert.Equal(t, quotapkg.DefaultNodePoolLimit, EffectiveLimitFromSpec(q, "nodepools"))
}

// ─── BuildHighUsageCondition ──────────────────────────────────────────────────

func TestBuildHighUsageCondition_NoneHigh(t *testing.T) {
	conds := BuildHighUsageCondition(nil, nil, 1, fixedNow())
	require.Len(t, conds, 1)
	c := conds[0]
	assert.Equal(t, privatev1.QuotaConditionHighUsage, c.Type)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, "UsageNormal", c.Reason)
	assert.Equal(t, int64(1), c.ObservedGeneration)
}

func TestBuildHighUsageCondition_OneResourceHigh(t *testing.T) {
	conds := BuildHighUsageCondition(nil, []string{"hostedclusters"}, 2, fixedNow())
	require.Len(t, conds, 1)
	c := conds[0]
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, "HighUsage", c.Reason)
	assert.Contains(t, c.Message, "hostedclusters")
	assert.Contains(t, c.Message, "QuotaRequest")
}

func TestBuildHighUsageCondition_MultipleResourcesHigh(t *testing.T) {
	conds := BuildHighUsageCondition(nil, []string{"hostedclusters", "nodepools"}, 1, fixedNow())
	require.Len(t, conds, 1)
	assert.Equal(t, metav1.ConditionTrue, conds[0].Status)
	assert.Contains(t, conds[0].Message, "hostedclusters")
	assert.Contains(t, conds[0].Message, "nodepools")
}

func TestBuildHighUsageCondition_PreservesTransitionTimeWhenStatusUnchanged(t *testing.T) {
	original := fixedNow()
	existing := []metav1.Condition{{
		Type:               privatev1.QuotaConditionHighUsage,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: original,
		Reason:             "HighUsage",
		Message:            "old message",
	}}

	later := metav1.NewTime(original.Add(5 * time.Minute))
	conds := BuildHighUsageCondition(existing, []string{"hostedclusters"}, 2, later)

	require.Len(t, conds, 1)
	// Status is still True → LastTransitionTime must be preserved.
	assert.Equal(t, original, conds[0].LastTransitionTime, "transition time must not change when status stays True")
	assert.Equal(t, int64(2), conds[0].ObservedGeneration)
}

func TestBuildHighUsageCondition_UpdatesTransitionTimeWhenStatusChanges(t *testing.T) {
	original := fixedNow()
	existing := []metav1.Condition{{
		Type:               privatev1.QuotaConditionHighUsage,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: original,
		Reason:             "HighUsage",
		Message:            "old message",
	}}

	later := metav1.NewTime(original.Add(5 * time.Minute))
	// Transition from True → False (no high-usage resources).
	conds := BuildHighUsageCondition(existing, nil, 3, later)

	require.Len(t, conds, 1)
	assert.Equal(t, metav1.ConditionFalse, conds[0].Status)
	assert.Equal(t, later, conds[0].LastTransitionTime, "transition time must update when status changes")
}

// ─── BuildQuotaStatus ─────────────────────────────────────────────────────────

func TestBuildQuotaStatus_BelowLimit(t *testing.T) {
	q := quotaWithLimits(50, 500)
	s := BuildQuotaStatus(q, counts(10, 100), fixedNow())

	require.Len(t, s.Resources, 2)
	assert.Equal(t, int32(10), resourceCurrent(s, "hostedclusters"))
	assert.Nil(t, resourceReachedTime(s, "hostedclusters"), "quotaReachedTime must be nil below limit")
	assert.Nil(t, resourceReachedTime(s, "nodepools"))

	cond := findHighUsageCondition(s.Conditions)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
}

func TestBuildQuotaStatus_AtLimit_SetsReachedTime(t *testing.T) {
	q := quotaWithLimits(10, 500)
	now := fixedNow()
	s := BuildQuotaStatus(q, counts(10, 0), now)

	rt := resourceReachedTime(s, "hostedclusters")
	require.NotNil(t, rt)
	assert.Equal(t, now, *rt)
}

func TestBuildQuotaStatus_ReachedTime_Preserved(t *testing.T) {
	original := fixedNow()
	q := quotaWithLimits(10, 500)
	q.Status.Resources = []privatev1.QuotaResourceStatus{{
		Resource:         "hostedclusters",
		Current:          10,
		QuotaReachedTime: &original,
	}}

	later := metav1.NewTime(original.Add(1 * time.Hour))
	s := BuildQuotaStatus(q, counts(10, 0), later)

	rt := resourceReachedTime(s, "hostedclusters")
	require.NotNil(t, rt)
	assert.Equal(t, original, *rt, "first reached time must be preserved across reconciles")
}

func TestBuildQuotaStatus_ReachedTime_ClearedWhenBelowLimit(t *testing.T) {
	original := fixedNow()
	q := quotaWithLimits(10, 500)
	q.Status.Resources = []privatev1.QuotaResourceStatus{{
		Resource:         "hostedclusters",
		Current:          10,
		QuotaReachedTime: &original,
	}}

	// Cluster count dropped below limit.
	s := BuildQuotaStatus(q, counts(5, 0), metav1.NewTime(original.Add(1*time.Hour)))

	assert.Nil(t, resourceReachedTime(s, "hostedclusters"), "reachedTime must be cleared when below limit")
}

func TestBuildQuotaStatus_HighUsage_At80Percent(t *testing.T) {
	// 40/50 = 80% — exactly at threshold.
	q := quotaWithLimits(50, 500)
	s := BuildQuotaStatus(q, counts(40, 0), fixedNow())

	cond := findHighUsageCondition(s.Conditions)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestBuildQuotaStatus_HighUsage_Below80Percent(t *testing.T) {
	// 39/50 = 78% — just below threshold.
	q := quotaWithLimits(50, 500)
	s := BuildQuotaStatus(q, counts(39, 0), fixedNow())

	cond := findHighUsageCondition(s.Conditions)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
}

func TestBuildQuotaStatus_ManualLimitApplied(t *testing.T) {
	// manualLimit = 20 takes precedence over limit = 50.
	// 16/20 = 80% → HighUsage.
	q := quotaWithManualLimits(20, 500)
	s := BuildQuotaStatus(q, counts(16, 0), fixedNow())

	cond := findHighUsageCondition(s.Conditions)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

// ─── StatusEqual ─────────────────────────────────────────────────────────────

func TestStatusEqual_Equal(t *testing.T) {
	now := fixedNow()
	q := quotaWithLimits(50, 500)
	s := BuildQuotaStatus(q, counts(10, 100), now)
	assert.True(t, StatusEqual(s, s))
}

func TestStatusEqual_DifferentCounts(t *testing.T) {
	now := fixedNow()
	q := quotaWithLimits(50, 500)
	a := BuildQuotaStatus(q, counts(10, 100), now)
	b := BuildQuotaStatus(q, counts(11, 100), now)
	assert.False(t, StatusEqual(a, b))
}

func TestStatusEqual_DifferentConditionStatus(t *testing.T) {
	now := fixedNow()
	q := quotaWithLimits(50, 500)
	// Below threshold.
	a := BuildQuotaStatus(q, counts(10, 0), now)
	// At threshold (80%).
	b := BuildQuotaStatus(q, counts(40, 0), now)
	assert.False(t, StatusEqual(a, b))
}

func TestStatusEqual_ReachedTimePresenceMatters(t *testing.T) {
	now := fixedNow()
	q := quotaWithLimits(10, 500)
	// At limit → reachedTime set.
	a := BuildQuotaStatus(q, counts(10, 0), now)
	// Below limit → reachedTime nil.
	b := BuildQuotaStatus(q, counts(9, 0), now)
	assert.False(t, StatusEqual(a, b))
}

// ─── test helpers ────────────────────────────────────────────────────────────

func resourceCurrent(s privatev1.QuotaStatus, resource string) int32 {
	for _, r := range s.Resources {
		if r.Resource == resource {
			return r.Current
		}
	}
	return -1
}

func resourceReachedTime(s privatev1.QuotaStatus, resource string) *metav1.Time {
	for _, r := range s.Resources {
		if r.Resource == resource {
			return r.QuotaReachedTime
		}
	}
	return nil
}

func findHighUsageCondition(conds []metav1.Condition) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == privatev1.QuotaConditionHighUsage {
			return &conds[i]
		}
	}
	return nil
}
