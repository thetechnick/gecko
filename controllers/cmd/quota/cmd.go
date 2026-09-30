package quota

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/openshift-online/gecko/controllers/quota"
	"github.com/openshift-online/gecko/controllers/util/setup"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	ctrl "sigs.k8s.io/controller-runtime"
)

// NewCommand returns the quota subcommand that runs the QuotaRequest controller.
func NewCommand(rf *setup.RootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "quota",
		Short: "Run the QuotaRequest controller",
		Long: `Run the QuotaRequest controller.

The controller watches QuotaRequest objects and auto-approves those whose
requestedLimit falls within the namespace's autoApproveThreshold stored on
the Quota object. Requests that exceed the threshold are left in Pending
phase for operator review.`,
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

			rec := quota.NewReconciler(log, mgr.GetClient())

			if err := ctrl.NewControllerManagedBy(mgr).
				For(&privatev1.QuotaRequest{}).
				WithOptions(rf.ControllerOpts()).
				Complete(rec); err != nil {
				return fmt.Errorf("setup quota controller: %w", err)
			}

			return mgr.Start(ctx)
		},
	}
}
