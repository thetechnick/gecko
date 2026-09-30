package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Quota is the authoritative per-namespace record of resource limits and live
// consumption for HostedCluster and NodePool objects. Each namespace has exactly
// one Quota object, named "default", created and maintained by the platform.
// Users have read-only access via the public API; operators manage limits and
// auto-approval thresholds via the private API.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
type Quota struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec contains the configured resource limits for this namespace.

	// +optional
	Spec QuotaSpec `json:"spec,omitempty"`

	// status contains live consumption data observed by the platform.

	// +optional
	Status QuotaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type QuotaList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	// items is the list of Quota objects.

	Items []Quota `json:"items"`
}

// QuotaSpec contains the configured resource limits for a namespace.
type QuotaSpec struct {
	// resources contains the per-resource quota configurations.

	// +optional
	// +listType=map
	// +listMapKey=resource
	Resources []QuotaResourceSpec `json:"resources,omitempty"`
}

// QuotaResourceSpec contains the quota configuration for a single resource kind.
type QuotaResourceSpec struct {
	// resource is the plural lowercase resource name, e.g. "hostedclusters" or "nodepools".

	// +required
	// +kubebuilder:validation:Enum=hostedclusters;nodepools
	Resource string `json:"resource"`

	// limit is the effective enforced limit for this resource in the namespace.
	// It reflects the base limit, adjusted by any approved QuotaRequest or
	// operator-set manualLimit.

	// +required
	// +kubebuilder:validation:Minimum=0
	Limit int32 `json:"limit"`
}

// QuotaStatus contains live consumption data for a namespace.
type QuotaStatus struct {
	// resources contains per-resource consumption data.

	// +optional
	// +listType=map
	// +listMapKey=resource
	Resources []QuotaResourceStatus `json:"resources,omitempty"`
}

// QuotaResourceStatus contains live consumption data for a single resource kind.
type QuotaResourceStatus struct {
	// resource is the plural lowercase resource name, e.g. "hostedclusters" or "nodepools".

	// +required
	// +kubebuilder:validation:Enum=hostedclusters;nodepools
	Resource string `json:"resource"`

	// current is the number of objects of this resource kind currently in the namespace.

	// +required
	// +kubebuilder:validation:Minimum=0
	Current int32 `json:"current"`

	// quotaReachedTime is the time at which the limit was first reached for this
	// resource. Null when the limit has not been reached.

	// +optional
	QuotaReachedTime *metav1.Time `json:"quotaReachedTime,omitempty"`
}

// QuotaRequest allows users to request a quota limit increase for a namespace.
// Users submit a QuotaRequest with a justification; the QuotaRequest controller
// automatically approves requests that fall within the namespace's
// autoApproveThreshold. Requests that exceed the threshold remain Pending for
// operator review.
//
// QuotaRequest objects are immutable after creation. Approved requests cannot
// be deleted via the public API, preserving them as an immutable audit record.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
type QuotaRequest struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the requested quota increase.

	// +required
	Spec QuotaRequestSpec `json:"spec"`

	// status reflects the current phase of the request.

	// +optional
	Status QuotaRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type QuotaRequestList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	// items is the list of QuotaRequests.

	Items []QuotaRequest `json:"items"`
}

// QuotaRequestSpec contains the user's requested limit increase.
type QuotaRequestSpec struct {
	// resource is the plural lowercase resource name for which a limit increase
	// is requested, e.g. "hostedclusters" or "nodepools".
	// This field is immutable after creation.

	// +required
	// +kubebuilder:validation:Enum=hostedclusters;nodepools
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="resource is immutable"
	Resource string `json:"resource"`

	// requestedLimit is the new limit value being requested.
	// Must be greater than the current effective limit.
	// This field is immutable after creation.

	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="requestedLimit is immutable"
	RequestedLimit int32 `json:"requestedLimit"`

	// reason describes why the limit increase is needed.
	// This field is immutable after creation.

	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="reason is immutable"
	Reason string `json:"reason"`

	// neededBy is the date by which the additional quota is required.
	// Providing this information helps the platform pre-provision shared
	// infrastructure before the new limit is reached.
	// This field is immutable after creation.

	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="neededBy is immutable"
	NeededBy *metav1.Time `json:"neededBy,omitempty"`
}

// QuotaRequestPhase represents the lifecycle phase of a QuotaRequest.
type QuotaRequestPhase string

// QuotaRequestStatus contains the current phase and decision details for a
// QuotaRequest.
type QuotaRequestStatus struct {
	// phase is the current lifecycle phase of the request.

	// +optional
	// +kubebuilder:validation:Enum=Pending;Approved;Denied
	Phase QuotaRequestPhase `json:"phase,omitempty"`

	// approvedLimit is the limit value that was granted. Set only when phase is Approved.

	// +optional
	// +kubebuilder:validation:Minimum=1
	ApprovedLimit *int32 `json:"approvedLimit,omitempty"`

	// decidedAt is the time at which the request was approved or denied.

	// +optional
	DecidedAt *metav1.Time `json:"decidedAt,omitempty"`

	// decisionNote is a human-readable explanation of the approval or denial decision.

	// +optional
	// +kubebuilder:validation:MaxLength=1024
	DecisionNote *string `json:"decisionNote,omitempty"`
}

func init() {
	register(&Quota{}, &QuotaList{})
	register(&QuotaRequest{}, &QuotaRequestList{})
}
