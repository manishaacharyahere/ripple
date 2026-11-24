// Package v1alpha1 defines the ModuleRelease API.
// +kubebuilder:object:generate=true
// +groupName=ripple.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "ripple.dev", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)

func init() { SchemeBuilder.Register(&ModuleRelease{}, &ModuleReleaseList{}) }

// ModuleRelease rolls a module version out to its consumers wave by wave,
// stopping at the first wave where an upgrade would hurt a consumer.
type ModuleReleaseSpec struct {
	// Repo is the module's git clone URL.
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`
	// From is the version consumers run today.
	// +kubebuilder:validation:MinLength=1
	From string `json:"from"`
	// To is the version being released.
	// +kubebuilder:validation:MinLength=1
	To string `json:"to"`
	// Waves run in order; a wave starts only after every check in the
	// previous one passed.
	// +kubebuilder:validation:MinItems=1
	Waves []Wave `json:"waves"`
	// RunnerImage runs `ripple upgrade-plan`.
	// +kubebuilder:default="ripple-runner:dev"
	RunnerImage string `json:"runnerImage,omitempty"`
}

type Wave struct {
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,18}[a-z0-9])?$`
	Name string `json:"name"`
	// Consumers are directories in the repo (e.g. examples/complete) that
	// call the module with a relative source.
	// +kubebuilder:validation:MinItems=1
	Consumers []string `json:"consumers"`
}

// +kubebuilder:validation:Enum=Pending;Running;Halted;Succeeded
type Phase string

const (
	PhasePending   Phase = "Pending"
	PhaseRunning   Phase = "Running"
	PhaseHalted    Phase = "Halted"
	PhaseSucceeded Phase = "Succeeded"
)

type CheckResult struct {
	Wave     string `json:"wave"`
	Consumer string `json:"consumer"`
	Job      string `json:"job"`
	// Status is the runner verdict (clean, changes, destructive, plan-failed,
	// baseline-failed) or "error" when the Job produced no verdict.
	Status  string `json:"status"`
	Create  int    `json:"create,omitempty"`
	Update  int    `json:"update,omitempty"`
	Replace int    `json:"replace,omitempty"`
	Delete  int    `json:"delete,omitempty"`
	Message string `json:"message,omitempty"`
}

type ModuleReleaseStatus struct {
	Phase Phase `json:"phase,omitempty"`
	// CurrentWave is the wave being checked, or the one that halted.
	CurrentWave string `json:"currentWave,omitempty"`
	// Reason explains a halt.
	Reason             string        `json:"reason,omitempty"`
	Results            []CheckResult `json:"results,omitempty"`
	ObservedGeneration int64         `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mrel
// +kubebuilder:printcolumn:name="From",type=string,JSONPath=`.spec.from`
// +kubebuilder:printcolumn:name="To",type=string,JSONPath=`.spec.to`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Wave",type=string,JSONPath=`.status.currentWave`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`,priority=1
type ModuleRelease struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModuleReleaseSpec   `json:"spec,omitempty"`
	Status ModuleReleaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ModuleReleaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModuleRelease `json:"items"`
}

var _ runtime.Object = &ModuleRelease{}
