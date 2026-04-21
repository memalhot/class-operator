package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DeploymentSpec struct {
	MultiNamespace         bool   `json:"multiNamespace"`
	NamespaceTemplate      string `json:"namespaceTemplate,omitempty"`
	StudentNamespacePrefix string `json:"studentNamespacePrefix,omitempty"`
}

type ResourceQuotaSpec struct {
	CPU                    string `json:"cpu,omitempty"`
	Memory                 string `json:"memory,omitempty"`
	Pods                   string `json:"pods,omitempty"`
	PersistentVolumeClaims string `json:"persistentvolumeclaims,omitempty"`
	GPUs                   string `json:"requests.nvidia.com/gpu,omitempty"`
}

type StorageSpec struct {
	Mode             string   `json:"mode,omitempty"` // "perPod", "shared", "both"
	PerPodPVCSize    string   `json:"perPodPvcSize,omitempty"`
	SharedPVCSize    string   `json:"sharedPvcSize,omitempty"`
	StorageClassName string   `json:"storageClassName,omitempty"`
	AccessModes      []string `json:"accessModes,omitempty"`
}

type ServicesSpec struct {
	CreateSharedWorkbenches bool `json:"createSharedWorkbenches,omitempty"`
	CreateGrafanaFolder     bool `json:"createGrafanaFolder,omitempty"`
	CreatePushgateway       bool `json:"createPushgateway,omitempty"`
}

type NotebookCullingSpec struct {
	Enabled bool `json:"enabled,omitempty"`
	Cutoff  int  `json:"cutoff,omitempty"` // Idle timeout in seconds before culling
}

type CourseSpec struct {
	CourseCode         string              `json:"courseCode"`
	Semester           string              `json:"semester"`
	DisplayName        string              `json:"displayName,omitempty"`
	Professors         []string            `json:"professors"`
	TeachingAssistants []string            `json:"teachingAssistants,omitempty"`
	ColdFrontProject   string              `json:"coldFrontProject,omitempty"`
	Deployment         DeploymentSpec      `json:"deployment"`
	ResourceQuota      ResourceQuotaSpec   `json:"resourceQuota,omitempty"`
	AllowedImages      []string            `json:"allowedImages,omitempty"`
	Storage            StorageSpec         `json:"storage,omitempty"`
	StudentsGroup      string              `json:"studentsGroup,omitempty"`
	InstructorsGroup   string              `json:"instructorsGroup,omitempty"`
	Services           ServicesSpec        `json:"services,omitempty"`
	NotebookCulling    NotebookCullingSpec `json:"notebookCulling,omitempty"`
}

type CourseStatus struct {
	Phase              string             `json:"phase,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Namespaces         []string           `json:"namespaces,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=course
type Course struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CourseSpec   `json:"spec,omitempty"`
	Status CourseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CourseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Course `json:"items"`
}
