package v1beta1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type GrafanaOrganizationInternal struct {
	Name string `json:"name,omitempty"`

	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +optional
	JSONData json.RawMessage `json:"jsonData,omitempty"`

	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +optional
	SecureJSONData json.RawMessage `json:"secureJsonData,omitempty"`
}

// GrafanaOrganizationSpec defines the desired state of GrafanaOrganization
type GrafanaOrganizationSpec struct {
	GrafanaCommonSpec  `json:",inline"`
	GrafanaContentSpec `json:",inline"`

	Organization *GrafanaOrganizationInternal `json:"organization"`
}

// GrafanaOrganizationStatus defines the observed state of GrafanaOrganization
type GrafanaOrganizationStatus struct {
	GrafanaCommonStatus  `json:",inline"`
	GrafanaContentStatus `json:",inline"`

	// The dashboard instanceSelector can't find matching grafana instances
	NoMatchingInstances bool `json:"NoMatchingInstances,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// GrafanaOrganization is the Schema for the grafanaorganizations API
// +kubebuilder:printcolumn:name="No matching instances",type="boolean",JSONPath=".status.NoMatchingInstances",description=""
// +kubebuilder:printcolumn:name="Last resync",type="date",format="date-time",JSONPath=".status.lastResync",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
type GrafanaOrganization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GrafanaOrganizationSpec   `json:"spec,omitempty"`
	Status GrafanaOrganizationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// GrafanaOrganizationList contains a list of GrafanaOrganization
type GrafanaOrganizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GrafanaOrganization `json:"items"`
}

// Conditions implements FolderReferencer.
func (in *GrafanaOrganization) Conditions() *[]metav1.Condition {
	return &in.Status.Conditions
}

// CurrentGeneration implements FolderReferencer.
func (in *GrafanaOrganization) CurrentGeneration() int64 {
	return in.Generation
}

// GrafanaContentSpec implements GrafanaContentResource
func (in *GrafanaOrganization) GrafanaContentSpec() *GrafanaContentSpec {
	return &in.Spec.GrafanaContentSpec
}

// GrafanaContentSpec implements GrafanaContentResource
func (in *GrafanaOrganization) GrafanaContentStatus() *GrafanaContentStatus {
	return &in.Status.GrafanaContentStatus
}

func (in *GrafanaOrganizationList) Exists(namespace, name string) bool {
	for _, item := range in.Items {
		if item.Namespace == namespace && item.Name == name {
			return true
		}
	}

	return false
}

func (in *GrafanaOrganization) MatchLabels() *metav1.LabelSelector {
	return in.Spec.InstanceSelector
}

func (in *GrafanaOrganization) MatchNamespace() string {
	return in.Namespace
}

func (in *GrafanaOrganization) AllowCrossNamespace() bool {
	return in.Spec.AllowCrossNamespaceImport
}

func (in *GrafanaOrganization) CommonStatus() *GrafanaCommonStatus {
	return &in.Status.GrafanaCommonStatus
}

func (in *GrafanaOrganization) NamespacedResource(uid string) NamespacedResource {
	// Not enough context to call content.CustomUIDOrUID(uid).
	// Hence, use uid from args as the caller has more context
	return NewNamespacedResource(in.Namespace, in.Name, uid)
}

func init() {
	SchemeBuilder.Register(&GrafanaOrganization{}, &GrafanaOrganizationList{})
}
