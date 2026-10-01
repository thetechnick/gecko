package v1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
)

// ValidateCreate validates QuotaRequest creation.
func (qr *QuotaRequest) ValidateCreate(_ context.Context) error {
	return nil
}

// ValidateUpdate validates QuotaRequest updates.
// The spec fields are protected by XValidation immutability rules in the schema;
// this method enforces any cross-field invariants that cannot be expressed there.
func (qr *QuotaRequest) ValidateUpdate(_ context.Context, _ runtime.Object) error {
	return nil
}

// ValidateDelete validates QuotaRequest deletion.
// Approved requests are immutable audit records and cannot be deleted via the
// public API. Users may delete Pending, Denied, or Superseded requests (e.g.
// to withdraw a request, clear a rejected one, or clean up superseded ones).
func (qr *QuotaRequest) ValidateDelete(_ context.Context) error {
	if qr.Status.Phase == QuotaRequestPhaseApproved {
		return fmt.Errorf(
			"quotaRequest %q is Approved and cannot be deleted; "+
				"approved requests are immutable audit records",
			qr.Name,
		)
	}
	return nil
}
