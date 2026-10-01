package quota

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift-online/gecko/controllers/util/logger"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	quotapkg "github.com/openshift-online/gecko/platform-api/quota"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// highUsageThreshold is the fraction of the limit at which the HighUsage
// condition is set to True (80%).
const highUsageThreshold = 0.80

// resourceNames is the ordered list of resource kinds tracked in Quota status.
var resourceNames = []string{"hostedclusters", "nodepools"}

// QuotaStatusReconciler reconciles the Quota status for a namespace.
// It is triggered by changes to Cluster and NodePool objects. On each
// reconcile it:
//  1. Ensures the namespace's Quota singleton ("default") exists, creating it
//     with base defaults when absent.
//  2. Counts current Cluster and NodePool objects in the namespace.
//  3. Updates Quota.status.resources with the live counts, effective limits,
//     and quotaReachedTime for each resource kind.
type QuotaStatusReconciler struct {
	log    logger.Logger
	client client.Client
}

// NewQuotaStatusReconciler creates a new QuotaStatusReconciler.
func NewQuotaStatusReconciler(log logger.Logger, c client.Client) *QuotaStatusReconciler {
	return &QuotaStatusReconciler{log: log, client: c}
}

// Reconcile is triggered whenever a Cluster or NodePool changes in the
// namespace identified by req.Namespace. req.Name is the name of the
// triggering object and is not used directly — the reconciler always
// operates on the namespace's Quota singleton.
func (r *QuotaStatusReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	ns := req.Namespace

	// 1. Ensure the Quota singleton exists.
	quota, err := r.ensureQuota(ctx, ns)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("quota-status: ensure Quota for namespace %s: %w", ns, err)
	}

	// 2. Count current resources.
	counts, err := r.countResources(ctx, ns)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("quota-status: count resources in namespace %s: %w", ns, err)
	}

	// 3. Build the desired status.
	newStatus := r.buildStatus(quota, counts)

	// 4. Patch the status only when it differs.
	if statusEqual(quota.Status, newStatus) {
		return reconcile.Result{}, nil
	}

	updated := quota.DeepCopy()
	updated.Status = newStatus
	if err := r.client.Status().Update(ctx, updated); err != nil {
		return reconcile.Result{}, fmt.Errorf("quota-status: update Quota status %s/default: %w", ns, err)
	}
	r.log.Infof(ctx, "quota-status: updated Quota status for namespace %s: %v", ns, countsLogLine(counts))
	return reconcile.Result{}, nil
}

// ensureQuota returns the existing Quota singleton or creates a new one with
// base limits and an empty status.
func (r *QuotaStatusReconciler) ensureQuota(ctx context.Context, ns string) (*privatev1.Quota, error) {
	var quota privatev1.Quota
	err := r.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: "default"}, &quota)
	if err == nil {
		return &quota, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get Quota: %w", err)
	}

	// Bootstrap: create the singleton with base defaults.
	defaultThresholdClusters := quotapkg.DefaultAutoApproveThresholdClusters
	defaultThresholdNodePools := quotapkg.DefaultAutoApproveThresholdNodePools
	quota = privatev1.Quota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: ns,
		},
		Spec: privatev1.QuotaSpec{
			Resources: []privatev1.QuotaResourceSpec{
				{
					Resource:             "hostedclusters",
					Limit:                quotapkg.DefaultClusterLimit,
					AutoApproveThreshold: &defaultThresholdClusters,
				},
				{
					Resource:             "nodepools",
					Limit:                quotapkg.DefaultNodePoolLimit,
					AutoApproveThreshold: &defaultThresholdNodePools,
				},
			},
		},
	}
	if err := r.client.Create(ctx, &quota); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Another controller instance created it concurrently — re-fetch.
			if getErr := r.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: "default"}, &quota); getErr != nil {
				return nil, fmt.Errorf("get Quota after concurrent creation: %w", getErr)
			}
			return &quota, nil
		}
		return nil, fmt.Errorf("create Quota: %w", err)
	}
	r.log.Infof(ctx, "quota-status: created Quota default for namespace %s", ns)
	return &quota, nil
}

// countResources counts Clusters and NodePools in the given namespace.
func (r *QuotaStatusReconciler) countResources(ctx context.Context, ns string) (map[string]int32, error) {
	counts := make(map[string]int32, len(resourceNames))

	var clusters privatev1.ClusterList
	if err := r.client.List(ctx, &clusters, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	counts["hostedclusters"] = int32(len(clusters.Items))

	var nodepools privatev1.NodePoolList
	if err := r.client.List(ctx, &nodepools, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list nodepools: %w", err)
	}
	counts["nodepools"] = int32(len(nodepools.Items))

	return counts, nil
}

// buildStatus constructs a QuotaStatus from the current counts, preserving
// quotaReachedTime when the limit was already reached, and maintaining the
// HighUsage condition when any resource has reached 80% of its effective limit.
func (r *QuotaStatusReconciler) buildStatus(quota *privatev1.Quota, counts map[string]int32) privatev1.QuotaStatus {
	now := metav1.NewTime(time.Now().UTC())

	// Index existing status entries for quotaReachedTime preservation.
	prevReached := make(map[string]*metav1.Time, len(quota.Status.Resources))
	for _, rs := range quota.Status.Resources {
		if rs.QuotaReachedTime != nil {
			t := *rs.QuotaReachedTime
			prevReached[rs.Resource] = &t
		}
	}

	var resources []privatev1.QuotaResourceStatus
	var highUsageResources []string

	for _, resource := range resourceNames {
		current := counts[resource]
		limit := effectiveLimitFromSpec(quota, resource)

		var reachedTime *metav1.Time
		if current >= limit {
			if prev, ok := prevReached[resource]; ok {
				reachedTime = prev
			} else {
				t := now
				reachedTime = &t
			}
		}

		// Track resources at or above the 80% high-usage threshold.
		if limit > 0 && float64(current)/float64(limit) >= highUsageThreshold {
			highUsageResources = append(highUsageResources, resource)
		}

		resources = append(resources, privatev1.QuotaResourceStatus{
			Resource:         resource,
			Current:          current,
			QuotaReachedTime: reachedTime,
		})
	}

	// Build the HighUsage condition from the current observation.
	conditions := buildHighUsageCondition(quota.Status.Conditions, highUsageResources, quota.Generation, now)

	return privatev1.QuotaStatus{
		Conditions: conditions,
		Resources:  resources,
	}
}

// buildHighUsageCondition returns an updated conditions slice with the HighUsage
// condition set according to whether any resources are at or above 80% usage.
// It preserves the LastTransitionTime from the existing condition when the
// status (True/False) has not changed.
func buildHighUsageCondition(existing []metav1.Condition, highUsageResources []string, generation int64, now metav1.Time) []metav1.Condition {
	isHigh := len(highUsageResources) > 0

	var condStatus metav1.ConditionStatus
	var reason, message string
	if isHigh {
		condStatus = metav1.ConditionTrue
		reason = "HighUsage"
		if len(highUsageResources) == 1 {
			message = fmt.Sprintf("resource %q has reached 80%% of its quota limit; consider submitting a QuotaRequest", highUsageResources[0])
		} else {
			message = fmt.Sprintf("resources %v have reached 80%% of their quota limits; consider submitting a QuotaRequest", highUsageResources)
		}
	} else {
		condStatus = metav1.ConditionFalse
		reason = "UsageNormal"
		message = "all resources are below 80% of their quota limits"
	}

	// Preserve LastTransitionTime when the condition status has not changed.
	transitionTime := now
	for _, c := range existing {
		if c.Type == privatev1.QuotaConditionHighUsage && c.Status == condStatus {
			transitionTime = c.LastTransitionTime
			break
		}
	}

	newCond := metav1.Condition{
		Type:               privatev1.QuotaConditionHighUsage,
		Status:             condStatus,
		ObservedGeneration: generation,
		LastTransitionTime: transitionTime,
		Reason:             reason,
		Message:            message,
	}

	// Use meta.SetStatusCondition to merge into a copy of the existing slice.
	out := make([]metav1.Condition, len(existing))
	copy(out, existing)
	meta.SetStatusCondition(&out, newCond)
	return out
}

// effectiveLimitFromSpec returns the enforced limit for the resource from the
// Quota spec, applying the same precedence as the enforcer: manualLimit >
// limit > base default.
func effectiveLimitFromSpec(quota *privatev1.Quota, resource string) int32 {
	for _, r := range quota.Spec.Resources {
		if r.Resource == resource {
			if r.ManualLimit != nil {
				return *r.ManualLimit
			}
			return r.Limit
		}
	}
	// Fall back to hard-coded base defaults when the spec entry is absent.
	if resource == "nodepools" {
		return quotapkg.DefaultNodePoolLimit
	}
	return quotapkg.DefaultClusterLimit
}

// statusEqual reports whether two QuotaStatus values are semantically
// identical — same conditions, same resources with the same counts and reached
// times. This avoids spurious status updates.
func statusEqual(a, b privatev1.QuotaStatus) bool {
	if !conditionsEqual(a.Conditions, b.Conditions) {
		return false
	}
	if len(a.Resources) != len(b.Resources) {
		return false
	}
	for i := range a.Resources {
		ar, br := a.Resources[i], b.Resources[i]
		if ar.Resource != br.Resource || ar.Current != br.Current {
			return false
		}
		aHas := ar.QuotaReachedTime != nil
		bHas := br.QuotaReachedTime != nil
		if aHas != bHas {
			return false
		}
		if aHas && !ar.QuotaReachedTime.Equal(br.QuotaReachedTime) {
			return false
		}
	}
	return true
}

// conditionsEqual reports whether two condition slices are semantically
// identical for the purpose of avoiding spurious status writes.
func conditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	bByType := make(map[string]metav1.Condition, len(b))
	for _, c := range b {
		bByType[c.Type] = c
	}
	for _, ac := range a {
		bc, ok := bByType[ac.Type]
		if !ok {
			return false
		}
		if ac.Status != bc.Status ||
			ac.Reason != bc.Reason ||
			ac.Message != bc.Message ||
			ac.ObservedGeneration != bc.ObservedGeneration ||
			!ac.LastTransitionTime.Equal(&bc.LastTransitionTime) {
			return false
		}
	}
	return true
}

func countsLogLine(counts map[string]int32) string {
	return fmt.Sprintf("hostedclusters=%d nodepools=%d", counts["hostedclusters"], counts["nodepools"])
}
