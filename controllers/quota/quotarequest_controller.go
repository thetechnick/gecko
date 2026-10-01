// Package quota implements the QuotaRequest controller.
// The controller watches QuotaRequest objects and auto-approves those whose
// requestedLimit falls within the namespace's autoApproveThreshold. Requests
// that exceed the threshold are left in Pending phase for operator review.
// When multiple Pending requests target the same resource, all but the newest
// (by creationTimestamp) are transitioned to Superseded.
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

// Reconciler evaluates QuotaRequest objects and auto-approves those that
// fall within the namespace's autoApproveThreshold.
type Reconciler struct {
	log    logger.Logger
	client client.Client
}

// NewReconciler creates a new quota Reconciler.
func NewReconciler(log logger.Logger, c client.Client) *Reconciler {
	return &Reconciler{
		log:    log,
		client: c,
	}
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

	// Supersession: list all Pending requests for the same resource and mark
	// all but the newest one as Superseded. The newest takes precedence.
	newest, err := r.supersedeStalePending(ctx, &qr)
	if err != nil {
		return reconcile.Result{}, err
	}
	// If this request was itself superseded, stop processing it.
	if !newest {
		return reconcile.Result{}, nil
	}

	// Read the namespace's Quota to obtain the autoApproveThreshold.
	var quota privatev1.Quota
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: "default"}, &quota); err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Infof(ctx, "quota: no Quota object in namespace %s, leaving QuotaRequest %s in Pending", req.Namespace, req.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("quota: get Quota for namespace %s: %w", req.Namespace, err)
	}

	threshold, ok := r.autoApproveThreshold(&quota, qr.Spec.Resource)
	if !ok {
		r.log.Infof(ctx, "quota: no autoApproveThreshold set for resource %q in namespace %s, leaving QuotaRequest %s in Pending",
			qr.Spec.Resource, req.Namespace, req.Name)
		return reconcile.Result{}, nil
	}

	if qr.Spec.RequestedLimit <= threshold {
		return r.approve(ctx, &qr, &quota)
	}

	// Above the auto-approve threshold — leave Pending for operator review.
	r.log.Infof(ctx, "quota: QuotaRequest %s/%s requestedLimit=%d exceeds autoApproveThreshold=%d for %s, leaving Pending",
		req.Namespace, req.Name, qr.Spec.RequestedLimit, threshold, qr.Spec.Resource)
	return reconcile.Result{}, nil
}

// supersedeStalePending lists all Pending QuotaRequests for the same resource
// in the same namespace. It marks every request that is older than the current
// one as Superseded (setting supersededBy to the current request's name).
// It returns true when the current request is the newest (and should continue
// to be processed), or false when the current request was itself superseded by
// a newer one (in which case the caller should stop processing it).
func (r *Reconciler) supersedeStalePending(ctx context.Context, current *privatev1.QuotaRequest) (bool, error) {
	var allRequests privatev1.QuotaRequestList
	if err := r.client.List(ctx, &allRequests, client.InNamespace(current.Namespace)); err != nil {
		return false, fmt.Errorf("quota: list QuotaRequests in namespace %s: %w", current.Namespace, err)
	}

	// Collect only Pending requests for the same resource (including current).
	pending := make([]*privatev1.QuotaRequest, 0, len(allRequests.Items))
	for i := range allRequests.Items {
		qr := &allRequests.Items[i]
		if qr.Spec.Resource == current.Spec.Resource &&
			(qr.Status.Phase == "" || qr.Status.Phase == privatev1.QuotaRequestPhasePending) {
			pending = append(pending, qr)
		}
	}

	if len(pending) <= 1 {
		// Only one Pending request for this resource — nothing to supersede.
		return true, nil
	}

	// Sort by creationTimestamp ascending; break ties by name for determinism.
	sort.Slice(pending, func(i, j int) bool {
		ti := pending[i].CreationTimestamp.Time
		tj := pending[j].CreationTimestamp.Time
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return pending[i].Name < pending[j].Name
	})

	newest := pending[len(pending)-1]

	// Supersede all requests that are older than the newest.
	for _, stale := range pending[:len(pending)-1] {
		if err := r.supersede(ctx, stale, newest.Name); err != nil {
			return false, err
		}
	}

	// Report whether the current request is the one that should proceed.
	return current.Name == newest.Name, nil
}

// supersede transitions a QuotaRequest to the Superseded phase, recording the
// name of the newer request that replaced it.
func (r *Reconciler) supersede(ctx context.Context, qr *privatev1.QuotaRequest, newerName string) error {
	now := metav1.NewTime(time.Now().UTC())
	note := fmt.Sprintf("superseded by a newer QuotaRequest %q for the same resource", newerName)

	updated := qr.DeepCopy()
	updated.Status.Phase = privatev1.QuotaRequestPhaseSuperseded
	updated.Status.SupersededBy = &newerName
	updated.Status.DecidedAt = &now
	updated.Status.DecisionNote = &note

	if err := r.client.Status().Update(ctx, updated); err != nil {
		return fmt.Errorf("quota: supersede QuotaRequest %s/%s: %w", qr.Namespace, qr.Name, err)
	}
	r.log.Infof(ctx, "quota: superseded QuotaRequest %s/%s (resource=%s) by %s",
		qr.Namespace, qr.Name, qr.Spec.Resource, newerName)
	return nil
}

// approve auto-approves the QuotaRequest and updates the Quota limit.
func (r *Reconciler) approve(ctx context.Context, qr *privatev1.QuotaRequest, quota *privatev1.Quota) (reconcile.Result, error) {
	now := metav1.NewTime(time.Now().UTC())
	trueVal := true

	// Update QuotaRequest status to Approved.
	approvedLimit := qr.Spec.RequestedLimit
	qr.Status.Phase = privatev1.QuotaRequestPhaseApproved
	qr.Status.ApprovedLimit = &approvedLimit
	qr.Status.DecidedAt = &now
	qr.Status.AutoApproved = &trueVal
	note := fmt.Sprintf("automatically approved: requestedLimit %d is within the autoApproveThreshold", approvedLimit)
	qr.Status.DecisionNote = &note

	if err := r.client.Status().Update(ctx, qr); err != nil {
		return reconcile.Result{}, fmt.Errorf("quota: update QuotaRequest %s/%s status: %w", qr.Namespace, qr.Name, err)
	}
	r.log.Infof(ctx, "quota: auto-approved QuotaRequest %s/%s: %s=%d", qr.Namespace, qr.Name, qr.Spec.Resource, approvedLimit)

	// Update the Quota limit.
	if err := r.updateQuotaLimit(ctx, quota, qr.Spec.Resource, approvedLimit); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

// updateQuotaLimit sets the effective limit for the resource on the Quota object.
func (r *Reconciler) updateQuotaLimit(ctx context.Context, quota *privatev1.Quota, resource string, newLimit int32) error {
	updated := false
	for i, res := range quota.Spec.Resources {
		if res.Resource == resource {
			quota.Spec.Resources[i].Limit = newLimit
			updated = true
			break
		}
	}
	if !updated {
		quota.Spec.Resources = append(quota.Spec.Resources, privatev1.QuotaResourceSpec{
			Resource: resource,
			Limit:    newLimit,
		})
	}

	if err := r.client.Update(ctx, quota); err != nil {
		return fmt.Errorf("quota: update Quota %s/%s limit for %s: %w", quota.Namespace, quota.Name, resource, err)
	}
	r.log.Infof(ctx, "quota: updated Quota %s/%s: %s limit set to %d", quota.Namespace, quota.Name, resource, newLimit)
	return nil
}

// autoApproveThreshold returns the auto-approve threshold for the given resource
// in the Quota. Returns (0, false) when no threshold is configured.
func (r *Reconciler) autoApproveThreshold(quota *privatev1.Quota, resource string) (int32, bool) {
	for _, res := range quota.Spec.Resources {
		if res.Resource == resource && res.AutoApproveThreshold != nil {
			return *res.AutoApproveThreshold, true
		}
	}
	return 0, false
}
