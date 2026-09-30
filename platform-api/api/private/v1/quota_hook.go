package v1

import "context"

// quotaCheck is the installed quota check function. It is nil until
// SetQuotaCheckFunc is called at server startup.
var quotaCheck func(ctx context.Context, namespace, resource string) error

// SetQuotaCheckFunc installs the package-level quota check function. Call this
// once during server initialisation, before the API server starts serving requests.
// The function should return a non-nil error when the creation would exceed quota.
func SetQuotaCheckFunc(f func(ctx context.Context, namespace, resource string) error) {
	quotaCheck = f
}
