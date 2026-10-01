package quota

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift-online/gecko/controllers/util/logger"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	quotapkg "github.com/openshift-online/gecko/platform-api/quota"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

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
// quotaReachedTime when the limit was already reached, and setting it when
// the limit is first reached.
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
	for _, resource := range resourceNames {
		current := counts[resource]
		limit := effectiveLimitFromSpec(quota, resource)

		var reachedTime *metav1.Time
		if current >= limit {
			if prev, ok := prevReached[resource]; ok {
				// Preserve the original time the limit was first reached.
				reachedTime = prev
			} else {
				// First time reaching the limit.
				t := now
				reachedTime = &t
			}
		}
		// If current < limit, reachedTime stays nil (limit no longer reached).

		resources = append(resources, privatev1.QuotaResourceStatus{
			Resource:         resource,
			Current:          current,
			QuotaReachedTime: reachedTime,
		})
	}

	return privatev1.QuotaStatus{Resources: resources}
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
// identical — same resources in the same order with the same counts and
// reached times. This avoids spurious status updates.
func statusEqual(a, b privatev1.QuotaStatus) bool {
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

func countsLogLine(counts map[string]int32) string {
	return fmt.Sprintf("hostedclusters=%d nodepools=%d", counts["hostedclusters"], counts["nodepools"])
}
