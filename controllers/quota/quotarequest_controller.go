// Package quota implements the QuotaRequest controller.
// The controller watches QuotaRequest objects and auto-approves those whose
// requestedLimit falls within the namespace's autoApproveThreshold. Requests
// that exceed the threshold are left in Pending phase for operator review.
// When multiple Pending requests target the same resource, all but the newest
// (by creationTimestamp, tie-broken by name) are transitioned to Superseded.
package quota

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/openshift-online/gecko/controllers/util/logger"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ─── Controller ──────────────────────────────────────────────────────────────

// Reconciler evaluates QuotaRequest objects, supersedes stale ones, and
// auto-approves those that fall within the namespace's autoApproveThreshold.
type Reconciler struct {
	log    logger.Logger
	client client.Client
}

// NewReconciler creates a new quota Reconciler.
func NewReconciler(log logger.Logger, c client.Client) *Reconciler {
	return &Reconciler{log: log, client: c}
}

// Reconcile processes one QuotaRequest event.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var qr privatev1.QuotaRequest
	if err := r.client.Get(ctx, req.NamespacedName, &qr); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("quota: get QuotaRequest %s/%s: %w", req.Namespace, req.Name, err)
	}

	// Only process Pending requests.
	if qr.Status.Phase != "" && qr.Status.Phase != privatev1.QuotaRequestPhasePending {
		return reconcile.Result{}, nil
	}

	// List all QuotaRequests in the namespace to find supersession candidates.
	var allRequests privatev1.QuotaRequestList
	if err := r.client.List(ctx, &allRequests, client.InNamespace(req.Namespace)); err != nil {
		return reconcile.Result{}, fmt.Errorf("quota: list QuotaRequests in namespace %s: %w", req.Namespace, err)
	}

	// Determine which Pending requests for this resource should be superseded.
	toSupersede, isNewest := SupersessionTargets(&qr, allRequests.Items)

	// Supersede all stale requests.
	now := metav1.NewTime(time.Now().UTC())
	newest := NewestPendingRequest(PendingForResource(allRequests.Items, qr.Spec.Resource))
	newestName := ""
	if newest != nil {
		newestName = newest.Name
	}
	for i := range toSupersede {
		note := SupersedeNote(newestName)
		updated := toSupersede[i].DeepCopy()
		updated.Status.Phase = privatev1.QuotaRequestPhaseSuperseded
		updated.Status.SupersededBy = &newestName
		updated.Status.DecidedAt = &now
		updated.Status.DecisionNote = &note
		if err := r.client.Status().Update(ctx, updated); err != nil {
			return reconcile.Result{}, fmt.Errorf("quota: supersede QuotaRequest %s/%s: %w",
				toSupersede[i].Namespace, toSupersede[i].Name, err)
		}
		r.log.Infof(ctx, "quota: superseded QuotaRequest %s/%s (resource=%s) by %s",
			toSupersede[i].Namespace, toSupersede[i].Name, toSupersede[i].Spec.Resource, newestName)
	}

	// If this request was itself superseded, stop processing it.
	if !isNewest {
		return reconcile.Result{}, nil
	}

	// Read the namespace's Quota to obtain the autoApproveThreshold.
	var quota privatev1.Quota
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: "default"}, &quota); err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Infof(ctx, "quota: no Quota object in namespace %s, leaving QuotaRequest %s in Pending",
				req.Namespace, req.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("quota: get Quota for namespace %s: %w", req.Namespace, err)
	}

	threshold, ok := AutoApproveThreshold(&quota, qr.Spec.Resource)
	if !ok {
		r.log.Infof(ctx, "quota: no autoApproveThreshold for resource %q in namespace %s, leaving QuotaRequest %s in Pending",
			qr.Spec.Resource, req.Namespace, req.Name)
		return reconcile.Result{}, nil
	}

	if !ShouldAutoApprove(qr.Spec.RequestedLimit, threshold) {
		r.log.Infof(ctx, "quota: QuotaRequest %s/%s requestedLimit=%d exceeds autoApproveThreshold=%d for %s, leaving Pending",
			req.Namespace, req.Name, qr.Spec.RequestedLimit, threshold, qr.Spec.Resource)
		return reconcile.Result{}, nil
	}

	return r.approve(ctx, &qr, &quota)
}

// approve patches the QuotaRequest to Approved and raises the Quota limit.
func (r *Reconciler) approve(ctx context.Context, qr *privatev1.QuotaRequest, quota *privatev1.Quota) (reconcile.Result, error) {
	now := metav1.NewTime(time.Now().UTC())
	trueVal := true
	approvedLimit := qr.Spec.RequestedLimit
	note := ApproveNote(approvedLimit)

	updated := qr.DeepCopy()
	updated.Status.Phase = privatev1.QuotaRequestPhaseApproved
	updated.Status.ApprovedLimit = &approvedLimit
	updated.Status.DecidedAt = &now
	updated.Status.AutoApproved = &trueVal
	updated.Status.DecisionNote = &note

	if err := r.client.Status().Update(ctx, updated); err != nil {
		return reconcile.Result{}, fmt.Errorf("quota: update QuotaRequest %s/%s status: %w", qr.Namespace, qr.Name, err)
	}
	r.log.Infof(ctx, "quota: auto-approved QuotaRequest %s/%s: %s=%d", qr.Namespace, qr.Name, qr.Spec.Resource, approvedLimit)

	if err := r.updateQuotaLimit(ctx, quota, qr.Spec.Resource, approvedLimit); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

// updateQuotaLimit sets the effective limit for the resource on the Quota spec.
func (r *Reconciler) updateQuotaLimit(ctx context.Context, quota *privatev1.Quota, resource string, newLimit int32) error {
	updated := quota.DeepCopy()
	found := false
	for i, res := range updated.Spec.Resources {
		if res.Resource == resource {
			updated.Spec.Resources[i].Limit = newLimit
			found = true
			break
		}
	}
	if !found {
		updated.Spec.Resources = append(updated.Spec.Resources, privatev1.QuotaResourceSpec{
			Resource: resource,
			Limit:    newLimit,
		})
	}
	if err := r.client.Update(ctx, updated); err != nil {
		return fmt.Errorf("quota: update Quota %s/%s limit for %s: %w", quota.Namespace, quota.Name, resource, err)
	}
	r.log.Infof(ctx, "quota: updated Quota %s/%s: %s limit set to %d", quota.Namespace, quota.Name, resource, newLimit)
	return nil
}

// ─── Pure business logic (package-level, easily unit-tested) ─────────────────

// PendingForResource returns all requests in the list that are Pending and
// target the given resource.
func PendingForResource(all []privatev1.QuotaRequest, resource string) []privatev1.QuotaRequest {
	var out []privatev1.QuotaRequest
	for _, qr := range all {
		if qr.Spec.Resource == resource &&
			(qr.Status.Phase == "" || qr.Status.Phase == privatev1.QuotaRequestPhasePending) {
			out = append(out, qr)
		}
	}
	return out
}

// NewestPendingRequest returns the newest request from the provided slice,
// sorted by creationTimestamp descending and then name ascending for
// determinism when timestamps are equal.
// Returns nil when the slice is empty.
func NewestPendingRequest(pending []privatev1.QuotaRequest) *privatev1.QuotaRequest {
	if len(pending) == 0 {
		return nil
	}
	sorted := make([]privatev1.QuotaRequest, len(pending))
	copy(sorted, pending)
	sort.Slice(sorted, func(i, j int) bool {
		ti := sorted[i].CreationTimestamp.Time
		tj := sorted[j].CreationTimestamp.Time
		if !ti.Equal(tj) {
			return ti.Before(tj) // ascending → newest is last
		}
		return sorted[i].Name < sorted[j].Name
	})
	newest := sorted[len(sorted)-1]
	return &newest
}

// SupersessionTargets determines which requests in all should be superseded
// given that current is the request being reconciled.
//
// It returns:
//   - toSupersede: the Pending requests (excluding current) that are older than
//     the newest Pending request for the same resource.
//   - isNewest: whether current itself is the newest Pending request and should
//     continue to the auto-approval step.
func SupersessionTargets(current *privatev1.QuotaRequest, all []privatev1.QuotaRequest) (toSupersede []privatev1.QuotaRequest, isNewest bool) {
	pending := PendingForResource(all, current.Spec.Resource)

	if len(pending) <= 1 {
		return nil, true
	}

	newest := NewestPendingRequest(pending)

	for _, qr := range pending {
		if qr.Name != newest.Name {
			toSupersede = append(toSupersede, qr)
		}
	}

	return toSupersede, current.Name == newest.Name
}

// AutoApproveThreshold returns the auto-approve threshold for the given
// resource from the Quota spec. Returns (0, false) when not configured.
func AutoApproveThreshold(quota *privatev1.Quota, resource string) (int32, bool) {
	for _, res := range quota.Spec.Resources {
		if res.Resource == resource && res.AutoApproveThreshold != nil {
			return *res.AutoApproveThreshold, true
		}
	}
	return 0, false
}

// ShouldAutoApprove reports whether a QuotaRequest with the given
// requestedLimit should be automatically approved given the threshold.
func ShouldAutoApprove(requestedLimit, threshold int32) bool {
	return requestedLimit <= threshold
}

// ApproveNote returns the decision note string for an auto-approved request.
func ApproveNote(approvedLimit int32) string {
	return fmt.Sprintf("automatically approved: requestedLimit %d is within the autoApproveThreshold", approvedLimit)
}

// SupersedeNote returns the decision note string for a superseded request.
func SupersedeNote(newerName string) string {
	return fmt.Sprintf("superseded by a newer QuotaRequest %q for the same resource", newerName)
}
