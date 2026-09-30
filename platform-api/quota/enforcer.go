// Package quota implements quota enforcement for the platform API.
// It provides a StoreQuotaEnforcer that reads quota limits from the Quota
// store and counts existing resources to determine whether a creation request
// would exceed the namespace's quota.
package quota

import (
	"context"
	"fmt"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	"k8s.io/apimachinery/pkg/api/meta"
)

const (
	// QuotaName is the singleton Quota object name per namespace.
	QuotaName = "default"

	// DefaultClusterLimit is the base limit for HostedClusters per namespace.
	DefaultClusterLimit int32 = 50
	// DefaultNodePoolLimit is the base limit for NodePools per namespace.
	DefaultNodePoolLimit int32 = 500

	// DefaultAutoApproveThresholdClusters is the default auto-approve ceiling for clusters (3× base).
	DefaultAutoApproveThresholdClusters int32 = 150
	// DefaultAutoApproveThresholdNodePools is the default auto-approve ceiling for nodepools (3× base).
	DefaultAutoApproveThresholdNodePools int32 = 1500
)

// Enforcer checks whether creating a resource of the given kind in the
// given namespace would exceed the namespace's quota. If the quota would be
// exceeded it returns a non-nil error whose message is suitable for surfacing
// directly to the API caller.
type Enforcer interface {
	CheckQuota(ctx context.Context, namespace, resource string) error
}

// StoreEnforcer implements Enforcer using the platform API stores for Quota
// objects and the resource being created.
type StoreEnforcer struct {
	quotaStore    storage.ResourceStore
	clusterStore  storage.ResourceStore
	nodepoolStore storage.ResourceStore
}

// NewStoreEnforcer creates a StoreEnforcer using the provided stores.
func NewStoreEnforcer(
	quotaStore storage.ResourceStore,
	clusterStore storage.ResourceStore,
	nodepoolStore storage.ResourceStore,
) *StoreEnforcer {
	return &StoreEnforcer{
		quotaStore:    quotaStore,
		clusterStore:  clusterStore,
		nodepoolStore: nodepoolStore,
	}
}

// CheckQuota verifies that creating one more resource of the given kind in the
// given namespace would not exceed the namespace's quota limit.
func (e *StoreEnforcer) CheckQuota(ctx context.Context, namespace, resource string) error {
	limit := e.effectiveLimit(ctx, namespace, resource)

	current, err := e.countCurrent(ctx, namespace, resource)
	if err != nil {
		return fmt.Errorf("quota check failed: %w", err)
	}

	if current >= limit {
		return fmt.Errorf(
			"quota exceeded: namespace %q has %d %s (limit: %d); "+
				"submit a QuotaRequest to request a limit increase",
			namespace, current, resource, limit,
		)
	}
	return nil
}

func (e *StoreEnforcer) effectiveLimit(ctx context.Context, namespace, resource string) int32 {
	obj, err := e.quotaStore.Get(ctx, namespace, QuotaName)
	if err != nil {
		return defaultLimit(resource)
	}

	quota, ok := obj.(*privatev1.Quota)
	if !ok {
		return defaultLimit(resource)
	}

	for _, r := range quota.Spec.Resources {
		if r.Resource == resource {
			// manualLimit takes precedence when set.
			if r.ManualLimit != nil {
				return *r.ManualLimit
			}
			return r.Limit
		}
	}
	return defaultLimit(resource)
}

func (e *StoreEnforcer) countCurrent(ctx context.Context, namespace, resource string) (int32, error) {
	store := e.storeFor(resource)
	if store == nil {
		return 0, fmt.Errorf("no store available for resource %q", resource)
	}

	list, err := store.List(ctx, storage.ListOptions{Namespace: namespace})
	if err != nil {
		return 0, fmt.Errorf("listing %s in namespace %q: %w", resource, namespace, err)
	}

	items, err := meta.ExtractList(list)
	if err != nil {
		return 0, fmt.Errorf("extracting list items for %s: %w", resource, err)
	}
	return int32(len(items)), nil
}

func (e *StoreEnforcer) storeFor(resource string) storage.ResourceStore {
	switch resource {
	case "hostedclusters":
		return e.clusterStore
	case "nodepools":
		return e.nodepoolStore
	default:
		return nil
	}
}

func defaultLimit(resource string) int32 {
	if resource == "nodepools" {
		return DefaultNodePoolLimit
	}
	return DefaultClusterLimit
}
