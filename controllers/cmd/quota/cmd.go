package quota

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/openshift-online/gecko/controllers/quota"
	"github.com/openshift-online/gecko/controllers/util/setup"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// NewCommand returns the quota subcommand that runs both the QuotaRequest
// controller (auto-approval) and the Quota status controller (live counts).
func NewCommand(rf *setup.RootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "quota",
		Short: "Run the quota controllers",
		Long: `Run the quota controllers.

Two controllers are started in the same manager:

  QuotaRequest controller:
    Watches QuotaRequest objects and auto-approves those whose requestedLimit
    falls within the namespace's autoApproveThreshold on the Quota object.
    Requests that exceed the threshold are left in Pending phase for operator
    review.

  Quota status controller:
    Watches Cluster and NodePool objects. On every change it recomputes live
    resource consumption and updates Quota.status with current counts and
    quotaReachedTime. It also bootstraps the Quota singleton ("default") for
    any namespace that does not yet have one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			log, err := rf.NewLogger("quota-controller")
			if err != nil {
				return fmt.Errorf("create logger: %w", err)
			}

			scheme := setup.NewScheme()
			mgr, err := rf.NewManager(scheme, log)
			if err != nil {
				return fmt.Errorf("create manager: %w", err)
			}

			// QuotaRequest controller — auto-approves within the threshold.
			qrRec := quota.NewReconciler(log, mgr.GetClient())
			if err := ctrl.NewControllerManagedBy(mgr).
				For(&privatev1.QuotaRequest{}).
				WithOptions(rf.ControllerOpts()).
				Named("quotarequest").
				Complete(qrRec); err != nil {
				return fmt.Errorf("setup QuotaRequest controller: %w", err)
			}

			// Quota status controller — maintains Quota.status with live counts.
			// Triggered by Cluster and NodePool create/update/delete events,
			// mapped to the Quota singleton in the same namespace.
			statusRec := quota.NewQuotaStatusReconciler(log, mgr.GetClient())
			if err := ctrl.NewControllerManagedBy(mgr).
				For(&privatev1.Quota{}).
				Watches(&privatev1.Cluster{}, handler.EnqueueRequestsFromMapFunc(clusterToQuota)).
				Watches(&privatev1.NodePool{}, handler.EnqueueRequestsFromMapFunc(nodepoolToQuota)).
				WithOptions(rf.ControllerOpts()).
				Named("quota-status").
				Complete(statusRec); err != nil {
				return fmt.Errorf("setup Quota status controller: %w", err)
			}

			return mgr.Start(ctx)
		},
	}
}

// clusterToQuota maps a Cluster event to a reconcile.Request for the Quota
// singleton in the same namespace.
func clusterToQuota(_ context.Context, obj ctrlclient.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: ctrlclient.ObjectKey{
			Namespace: obj.GetNamespace(),
			Name:      "default",
		},
	}}
}

// nodepoolToQuota maps a NodePool event to a reconcile.Request for the Quota
// singleton in the same namespace.
func nodepoolToQuota(_ context.Context, obj ctrlclient.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: ctrlclient.ObjectKey{
			Namespace: obj.GetNamespace(),
			Name:      "default",
		},
	}}
}
