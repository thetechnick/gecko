package v1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
)

// Default sets private, system-derived defaults for Cluster.
func (c *Cluster) Default(_ context.Context) error {
	if c.Spec.SafeName == "" {
		c.Spec.SafeName = DefaultSafeName(c.Name, c.UID)
	}
	return nil
}

// ValidateCreate validates Cluster creation.
func (c *Cluster) ValidateCreate(ctx context.Context) error {
	if c.Spec.SafeName != DefaultSafeName(c.Name, c.UID) {
		return fmt.Errorf("spec.safeName must be the default safe name for metadata.name")
	}
	if quotaCheck != nil {
		if err := quotaCheck(ctx, c.Namespace, "hostedclusters"); err != nil {
			return err
		}
	}
	return nil
}

// ValidateUpdate validates Cluster updates.
func (c *Cluster) ValidateUpdate(_ context.Context, oldObj runtime.Object) error {
	oldCluster, ok := oldObj.(*Cluster)
	if !ok {
		return fmt.Errorf("expected old object to be *Cluster, got %T", oldObj)
	}

	if oldCluster.Spec.SafeName == "" {
		if c.Spec.SafeName != DefaultSafeName(c.Name, c.UID) {
			return fmt.Errorf("spec.safeName must be the default safe name for metadata.name")
		}
		return nil
	}
	if c.Spec.SafeName != oldCluster.Spec.SafeName {
		return fmt.Errorf("spec.safeName is immutable")
	}
	return nil
}

// ValidateDelete validates Cluster deletion.
func (c *Cluster) ValidateDelete(_ context.Context) error {
	return nil
}
