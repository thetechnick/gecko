package v1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
)

// ValidateCreate validates NodePool creation.
func (n *NodePool) ValidateCreate(ctx context.Context) error {
	if quotaCheck != nil {
		if err := quotaCheck(ctx, n.Namespace, "nodepools"); err != nil {
			return err
		}
	}
	return nil
}

// ValidateUpdate validates NodePool updates.
func (n *NodePool) ValidateUpdate(_ context.Context, oldObj runtime.Object) error {
	oldPool, ok := oldObj.(*NodePool)
	if !ok {
		return fmt.Errorf("expected old object to be *NodePool, got %T", oldObj)
	}
	if n.Spec.ClusterID != oldPool.Spec.ClusterID {
		return fmt.Errorf("spec.clusterID is immutable")
	}
	return nil
}

// ValidateDelete validates NodePool deletion.
func (n *NodePool) ValidateDelete(_ context.Context) error {
	return nil
}
