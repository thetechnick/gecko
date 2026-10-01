package v1

import (
	"context"
	"errors"
	"testing"
)

// errQuotaExceeded is a test-only sentinel error returned by quota hook stubs.
var errQuotaExceeded = errors.New("quota exceeded")

// ─── QuotaRequest.ValidateDelete ─────────────────────────────────────────────

func TestQuotaRequestValidateDelete_ApprovedIsRejected(t *testing.T) {
	qr := &QuotaRequest{}
	qr.Name = "req-1"
	qr.Status.Phase = QuotaRequestPhaseApproved

	if err := qr.ValidateDelete(context.Background()); err == nil {
		t.Error("expected error deleting Approved QuotaRequest, got nil")
	}
}

func TestQuotaRequestValidateDelete_PendingIsAllowed(t *testing.T) {
	qr := &QuotaRequest{}
	qr.Status.Phase = QuotaRequestPhasePending

	if err := qr.ValidateDelete(context.Background()); err != nil {
		t.Errorf("expected no error deleting Pending QuotaRequest, got: %v", err)
	}
}

func TestQuotaRequestValidateDelete_DeniedIsAllowed(t *testing.T) {
	qr := &QuotaRequest{}
	qr.Status.Phase = QuotaRequestPhaseDenied

	if err := qr.ValidateDelete(context.Background()); err != nil {
		t.Errorf("expected no error deleting Denied QuotaRequest, got: %v", err)
	}
}

func TestQuotaRequestValidateDelete_SupersededIsAllowed(t *testing.T) {
	qr := &QuotaRequest{}
	qr.Status.Phase = QuotaRequestPhaseSuperseded

	if err := qr.ValidateDelete(context.Background()); err != nil {
		t.Errorf("expected no error deleting Superseded QuotaRequest, got: %v", err)
	}
}

func TestQuotaRequestValidateDelete_EmptyPhaseIsAllowed(t *testing.T) {
	// A newly-created QuotaRequest may have an empty phase before the
	// controller sets it to Pending.
	qr := &QuotaRequest{}
	qr.Status.Phase = ""

	if err := qr.ValidateDelete(context.Background()); err != nil {
		t.Errorf("expected no error deleting QuotaRequest with empty phase, got: %v", err)
	}
}

// ─── QuotaRequest.ValidateCreate / ValidateUpdate ────────────────────────────

func TestQuotaRequestValidateCreate_AlwaysSucceeds(t *testing.T) {
	qr := &QuotaRequest{}
	if err := qr.ValidateCreate(context.Background()); err != nil {
		t.Errorf("ValidateCreate should always succeed, got: %v", err)
	}
}

func TestQuotaRequestValidateUpdate_AlwaysSucceeds(t *testing.T) {
	qr := &QuotaRequest{}
	if err := qr.ValidateUpdate(context.Background(), &QuotaRequest{}); err != nil {
		t.Errorf("ValidateUpdate should always succeed, got: %v", err)
	}
}

// ─── Cluster/NodePool quota hook ─────────────────────────────────────────────

func TestClusterValidateCreate_QuotaHookCalled(t *testing.T) {
	called := false
	SetQuotaCheckFunc(func(_ context.Context, ns, resource string) error {
		called = true
		if ns != "my-ns" {
			t.Errorf("namespace: got %q, want %q", ns, "my-ns")
		}
		if resource != "hostedclusters" {
			t.Errorf("resource: got %q, want %q", resource, "hostedclusters")
		}
		return nil
	})
	t.Cleanup(func() { SetQuotaCheckFunc(nil) })

	c := &Cluster{}
	c.Name = "c1"
	c.Namespace = "my-ns"
	c.UID = "uid-1"
	c.Spec.SafeName = DefaultSafeName(c.Name, c.UID)

	if err := c.ValidateCreate(context.Background()); err != nil {
		t.Errorf("ValidateCreate: %v", err)
	}
	if !called {
		t.Error("quota check was not called")
	}
}

func TestClusterValidateCreate_QuotaErrorPropagated(t *testing.T) {
	SetQuotaCheckFunc(func(_ context.Context, _, _ string) error {
		return errQuotaExceeded
	})
	t.Cleanup(func() { SetQuotaCheckFunc(nil) })

	c := &Cluster{}
	c.Name = "c1"
	c.Namespace = "ns"
	c.UID = "uid-1"
	c.Spec.SafeName = DefaultSafeName(c.Name, c.UID)

	if err := c.ValidateCreate(context.Background()); err == nil {
		t.Error("expected quota error to be returned, got nil")
	}
}

func TestClusterValidateCreate_NoHookIsNoop(t *testing.T) {
	SetQuotaCheckFunc(nil)

	c := &Cluster{}
	c.Name = "c1"
	c.Namespace = "ns"
	c.UID = "uid-1"
	c.Spec.SafeName = DefaultSafeName(c.Name, c.UID)

	if err := c.ValidateCreate(context.Background()); err != nil {
		t.Errorf("expected no error with nil quota hook, got: %v", err)
	}
}

func TestNodePoolValidateCreate_QuotaHookCalled(t *testing.T) {
	called := false
	SetQuotaCheckFunc(func(_ context.Context, ns, resource string) error {
		called = true
		if resource != "nodepools" {
			t.Errorf("resource: got %q, want %q", resource, "nodepools")
		}
		return nil
	})
	t.Cleanup(func() { SetQuotaCheckFunc(nil) })

	np := &NodePool{}
	np.Namespace = "my-ns"

	if err := np.ValidateCreate(context.Background()); err != nil {
		t.Errorf("ValidateCreate: %v", err)
	}
	if !called {
		t.Error("quota check was not called for NodePool")
	}
}

func TestNodePoolValidateCreate_QuotaErrorPropagated(t *testing.T) {
	SetQuotaCheckFunc(func(_ context.Context, _, _ string) error {
		return errQuotaExceeded
	})
	t.Cleanup(func() { SetQuotaCheckFunc(nil) })

	np := &NodePool{}
	if err := np.ValidateCreate(context.Background()); err == nil {
		t.Error("expected quota error to be returned, got nil")
	}
}

func TestNodePoolValidateUpdate_ClusterIDImmutable(t *testing.T) {
	old := &NodePool{}
	old.Spec.ClusterID = "cluster-a"

	updated := &NodePool{}
	updated.Spec.ClusterID = "cluster-b"

	if err := updated.ValidateUpdate(context.Background(), old); err == nil {
		t.Error("expected error when clusterID changes, got nil")
	}
}

func TestNodePoolValidateUpdate_ClusterIDUnchanged(t *testing.T) {
	old := &NodePool{}
	old.Spec.ClusterID = "cluster-a"

	updated := &NodePool{}
	updated.Spec.ClusterID = "cluster-a"

	if err := updated.ValidateUpdate(context.Background(), old); err != nil {
		t.Errorf("expected no error when clusterID unchanged, got: %v", err)
	}
}
