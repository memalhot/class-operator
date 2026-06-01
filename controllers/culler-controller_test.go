package controllers

import (
	"context"
	"testing"
	"time"

	userv1 "github.com/openshift/api/user/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nercv1alpha1 "github.com/memalhot/class-operator/v1alpha1"
)

func setupCullerReconciler(t *testing.T, objs ...client.Object) (*ClassCullerReconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := nercv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add nerc scheme: %v", err)
	}
	if err := userv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add user scheme: %v", err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&nercv1alpha1.Class{}).
		Build()

	reconciler := &ClassCullerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	return reconciler, fakeClient
}

func createNotebook(name, namespace, username string, creationTime time.Time, stopped bool) *unstructured.Unstructured {
	notebook := &unstructured.Unstructured{}
	notebook.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	notebook.SetName(name)
	notebook.SetNamespace(namespace)
	notebook.SetCreationTimestamp(metav1.Time{Time: creationTime})

	annotations := map[string]string{
		"opendatahub.io/username": username,
	}

	if stopped {
		annotations["kubeflow-resource-stopped"] = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}

	notebook.SetAnnotations(annotations)
	return notebook
}

func TestGetGroupUsers_Culler(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-group",
		},
		Users: []string{"alice", "bob"},
	}

	reconciler, _ := setupCullerReconciler(t, group)

	users, err := reconciler.getGroupUsers(context.Background(), "test-group")
	if err != nil {
		t.Fatalf("getGroupUsers failed: %v", err)
	}

	if len(users) != 2 {
		t.Errorf("Expected 2 users, got %d", len(users))
	}
}

func TestGetGroupUsers_Empty(t *testing.T) {
	reconciler, _ := setupCullerReconciler(t)

	users, err := reconciler.getGroupUsers(context.Background(), "")
	if err != nil {
		t.Fatalf("getGroupUsers failed: %v", err)
	}

	if len(users) != 0 {
		t.Errorf("Expected 0 users for empty group, got %d", len(users))
	}
}

func TestGetGroupUsers_NonExistent(t *testing.T) {
	reconciler, _ := setupCullerReconciler(t)

	_, err := reconciler.getGroupUsers(context.Background(), "non-existent")
	if err == nil {
		t.Error("Expected error for non-existent group")
	}
}

func TestGetNotebookUsername(t *testing.T) {
	tests := []struct {
		name       string
		annotation string
		key        string
		expected   string
	}{
		{
			name:       "opendatahub.io annotation",
			annotation: "alice",
			key:        "opendatahub.io/username",
			expected:   "alice",
		},
		{
			name:       "notebooks.opendatahub.io annotation",
			annotation: "bob",
			key:        "notebooks.opendatahub.io/username",
			expected:   "bob",
		},
		{
			name:       "with whitespace",
			annotation: "  charlie  ",
			key:        "opendatahub.io/username",
			expected:   "charlie",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notebook := &unstructured.Unstructured{}
			notebook.SetAnnotations(map[string]string{
				tt.key: tt.annotation,
			})

			result := getNotebookUsername(notebook)
			if result != tt.expected {
				t.Errorf("Expected username %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestGetNotebookUsername_NoAnnotations(t *testing.T) {
	notebook := &unstructured.Unstructured{}

	result := getNotebookUsername(notebook)
	if result != "" {
		t.Errorf("Expected empty username, got %q", result)
	}
}

func TestGetNotebookUsername_MissingUsernameAnnotation(t *testing.T) {
	notebook := &unstructured.Unstructured{}
	notebook.SetAnnotations(map[string]string{
		"some-other-annotation": "value",
	})

	result := getNotebookUsername(notebook)
	if result != "" {
		t.Errorf("Expected empty username, got %q", result)
	}
}

func TestBuildUserCutoffMap(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice", "bob"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	reconciler, _ := setupCullerReconciler(t, group, class)

	userMap, err := reconciler.buildUserCutoffMap(context.Background(), "test-namespace")
	if err != nil {
		t.Fatalf("buildUserCutoffMap failed: %v", err)
	}

	if len(userMap) != 2 {
		t.Errorf("Expected 2 users in map, got %d", len(userMap))
	}

	if info, exists := userMap["alice"]; !exists {
		t.Error("Expected alice in user map")
	} else {
		if info.ClassName != "test-class" {
			t.Errorf("Expected class name test-class, got %s", info.ClassName)
		}
		if info.Cutoff != 3600 {
			t.Errorf("Expected cutoff 3600, got %d", info.Cutoff)
		}
	}
}

func TestBuildUserCutoffMap_MultipleClasses(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class1 := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "class1",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  1800,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	class2 := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "class2",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	reconciler, _ := setupCullerReconciler(t, group, class1, class2)

	userMap, err := reconciler.buildUserCutoffMap(context.Background(), "test-namespace")
	if err != nil {
		t.Fatalf("buildUserCutoffMap failed: %v", err)
	}

	if len(userMap) != 1 {
		t.Fatalf("Expected 1 user in map, got %d", len(userMap))
	}

	if info, exists := userMap["alice"]; !exists {
		t.Error("Expected alice in user map")
	} else {
		if info.Cutoff != 3600 {
			t.Errorf("Expected higher cutoff (3600) to win, got %d", info.Cutoff)
		}
		if info.ClassName != "class2" {
			t.Errorf("Expected class2, got %s", info.ClassName)
		}
	}
}

func TestBuildUserCutoffMap_SkipsCullingDisabled(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: false,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	reconciler, _ := setupCullerReconciler(t, group, class)

	userMap, err := reconciler.buildUserCutoffMap(context.Background(), "test-namespace")
	if err != nil {
		t.Fatalf("buildUserCutoffMap failed: %v", err)
	}

	if len(userMap) != 0 {
		t.Errorf("Expected empty map when culling disabled, got %d users", len(userMap))
	}
}

func TestBuildUserCutoffMap_SkipsInvalidCutoff(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  0,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	reconciler, _ := setupCullerReconciler(t, group, class)

	userMap, err := reconciler.buildUserCutoffMap(context.Background(), "test-namespace")
	if err != nil {
		t.Fatalf("buildUserCutoffMap failed: %v", err)
	}

	if len(userMap) != 0 {
		t.Errorf("Expected empty map when cutoff is 0, got %d users", len(userMap))
	}
}

func TestBuildUserCutoffMap_SkipsWrongNamespace(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"other-namespace"},
		},
	}

	reconciler, _ := setupCullerReconciler(t, group, class)

	userMap, err := reconciler.buildUserCutoffMap(context.Background(), "test-namespace")
	if err != nil {
		t.Fatalf("buildUserCutoffMap failed: %v", err)
	}

	if len(userMap) != 0 {
		t.Errorf("Expected empty map when namespace doesn't match, got %d users", len(userMap))
	}
}

func TestBuildUserCutoffMap_WithWhitespace(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{" alice ", "  ", "bob"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	reconciler, _ := setupCullerReconciler(t, group, class)

	userMap, err := reconciler.buildUserCutoffMap(context.Background(), "test-namespace")
	if err != nil {
		t.Fatalf("buildUserCutoffMap failed: %v", err)
	}

	if len(userMap) != 2 {
		t.Errorf("Expected 2 users (empty string should be skipped), got %d", len(userMap))
	}

	if _, exists := userMap["alice"]; !exists {
		t.Error("Expected alice in map (whitespace should be trimmed)")
	}
}

func TestReconcile_CullingNotEnabled(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: false,
			},
		},
	}

	reconciler, _ := setupCullerReconciler(t, class)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Errorf("Reconcile should not error when culling disabled: %v", err)
	}

	if result.RequeueAfter > 0 {
		t.Error("Should not requeue when culling disabled")
	}
}

func TestReconcile_InvalidCutoff(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  -1,
			},
		},
	}

	reconciler, _ := setupCullerReconciler(t, class)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Errorf("Reconcile should not error with invalid cutoff: %v", err)
	}

	if result.RequeueAfter > 0 {
		t.Error("Should not requeue with invalid cutoff")
	}
}

func TestReconcile_NotFound(t *testing.T) {
	reconciler, _ := setupCullerReconciler(t)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Errorf("Reconcile should not error for non-existent class: %v", err)
	}

	if result.RequeueAfter > 0 {
		t.Error("Should not requeue for non-existent class")
	}
}

func TestReconcile_SingleNamespace_NoNamespaces(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: false,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{},
		},
	}

	reconciler, _ := setupCullerReconciler(t, class)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Errorf("Reconcile should not error with no namespaces: %v", err)
	}

	if result.RequeueAfter > 0 {
		t.Error("Should not requeue with no namespaces")
	}
}

func TestReconcile_MultiNamespace_NoPrefix(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:         true,
				StudentNamespacePrefix: "",
			},
		},
	}

	reconciler, _ := setupCullerReconciler(t, class)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Errorf("Reconcile should not error with no prefix: %v", err)
	}

	if result.RequeueAfter != 5*time.Minute {
		t.Error("Should still requeue after 5 minutes")
	}
}

func TestNotebookToClassRequests(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	reconciler, _ := setupCullerReconciler(t, class)

	notebook := createNotebook("test-notebook", "test-namespace", "alice", time.Now(), false)

	requests := reconciler.notebookToClassRequests(context.Background(), notebook)

	if len(requests) != 1 {
		t.Errorf("Expected 1 request, got %d", len(requests))
	}

	if requests[0].Name != "test-class" {
		t.Errorf("Expected request for test-class, got %s", requests[0].Name)
	}
}

func TestNotebookToClassRequests_CullingDisabled(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: false,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	reconciler, _ := setupCullerReconciler(t, class)

	notebook := createNotebook("test-notebook", "test-namespace", "alice", time.Now(), false)

	requests := reconciler.notebookToClassRequests(context.Background(), notebook)

	if len(requests) != 0 {
		t.Errorf("Expected 0 requests when culling disabled, got %d", len(requests))
	}
}

func TestNotebookToClassRequests_WrongNamespace(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"other-namespace"},
		},
	}

	reconciler, _ := setupCullerReconciler(t, class)

	notebook := createNotebook("test-notebook", "test-namespace", "alice", time.Now(), false)

	requests := reconciler.notebookToClassRequests(context.Background(), notebook)

	if len(requests) != 0 {
		t.Errorf("Expected 0 requests for wrong namespace, got %d", len(requests))
	}
}

func TestNotebookToClassRequests_WrongObjectType(t *testing.T) {
	reconciler, _ := setupCullerReconciler(t)

	wrongObject := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name: "not-a-notebook",
		},
	}

	requests := reconciler.notebookToClassRequests(context.Background(), wrongObject)

	if len(requests) != 0 {
		t.Errorf("Expected 0 requests for wrong object type, got %d", len(requests))
	}
}

func TestDeleteNotebookAndPVC(t *testing.T) {
	notebook := createNotebook("jupyter-nb-test", "test-namespace", "alice", time.Now(), false)

	pvc := &unstructured.Unstructured{}
	pvc.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "PersistentVolumeClaim",
	})
	pvc.SetName("jupyterhub-nb-test-pvc")
	pvc.SetNamespace("test-namespace")

	reconciler, fakeClient := setupCullerReconciler(t, notebook, pvc)

	err := reconciler.deleteNotebookAndPVC(context.Background(), "jupyter-nb-test", "test-namespace")
	if err != nil {
		t.Fatalf("deleteNotebookAndPVC failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "jupyter-nb-test",
		Namespace: "test-namespace",
	}, nb)
	if err == nil && nb.GetDeletionTimestamp() == nil {
		t.Error("Expected notebook to be deleted")
	}
}

func TestDeleteNotebookAndPVC_NoPVC(t *testing.T) {
	notebook := createNotebook("jupyter-nb-test", "test-namespace", "alice", time.Now(), false)

	reconciler, _ := setupCullerReconciler(t, notebook)

	err := reconciler.deleteNotebookAndPVC(context.Background(), "jupyter-nb-test", "test-namespace")
	if err != nil {
		t.Fatalf("deleteNotebookAndPVC should not fail when PVC doesn't exist: %v", err)
	}
}

func TestCullNotebooksInNamespace_StopsOldNotebook(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	notebook := createNotebook("test-notebook", "test-namespace", "alice", oldTime, false)

	reconciler, fakeClient := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksInNamespace(context.Background(), "test-namespace", time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksInNamespace failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-notebook",
		Namespace: "test-namespace",
	}, nb)
	if err != nil {
		t.Fatalf("Failed to get notebook: %v", err)
	}

	annotations := nb.GetAnnotations()
	if _, exists := annotations["kubeflow-resource-stopped"]; !exists {
		t.Error("Expected notebook to be stopped (have kubeflow-resource-stopped annotation)")
	}
}

func TestCullNotebooksInNamespace_KeepsRecentNotebook(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	recentTime := time.Now().Add(-30 * time.Minute)
	notebook := createNotebook("test-notebook", "test-namespace", "alice", recentTime, false)

	reconciler, fakeClient := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksInNamespace(context.Background(), "test-namespace", time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksInNamespace failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-notebook",
		Namespace: "test-namespace",
	}, nb)
	if err != nil {
		t.Fatalf("Failed to get notebook: %v", err)
	}

	annotations := nb.GetAnnotations()
	if _, exists := annotations["kubeflow-resource-stopped"]; exists {
		t.Error("Expected notebook to NOT be stopped (should be within cutoff time)")
	}
}

func TestCullNotebooksInNamespace_DeletesNotebookForNonGroupUser(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	notebook := createNotebook("test-notebook", "test-namespace", "bob", time.Now(), false)

	reconciler, fakeClient := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksInNamespace(context.Background(), "test-namespace", time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksInNamespace failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-notebook",
		Namespace: "test-namespace",
	}, nb)
	if err == nil && nb.GetDeletionTimestamp() == nil {
		t.Error("Expected notebook for non-group user to be deleted")
	}
}

func TestCullNotebooksInNamespace_SkipsStoppedNotebook(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	notebook := createNotebook("test-notebook", "test-namespace", "alice", oldTime, true)

	reconciler, fakeClient := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksInNamespace(context.Background(), "test-namespace", time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksInNamespace failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-notebook",
		Namespace: "test-namespace",
	}, nb)
	if err != nil {
		t.Fatalf("Failed to get notebook: %v", err)
	}
}

func TestCullNotebooksInNamespace_SkipsNoUsernameAnnotation(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	notebook := &unstructured.Unstructured{}
	notebook.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	notebook.SetName("test-notebook")
	notebook.SetNamespace("test-namespace")
	notebook.SetCreationTimestamp(metav1.Time{Time: time.Now().Add(-2 * time.Hour)})

	reconciler, _ := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksInNamespace(context.Background(), "test-namespace", time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksInNamespace should not fail with missing username: %v", err)
	}
}

func TestCullNotebooksMultiNamespace_StopsOldNotebook(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:         true,
				StudentNamespacePrefix: "cs101",
			},
		},
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	notebook := createNotebook("test-notebook", "cs101-alice", "alice", oldTime, false)

	reconciler, fakeClient := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksMultiNamespace(context.Background(), class, time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksMultiNamespace failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-notebook",
		Namespace: "cs101-alice",
	}, nb)
	if err != nil {
		t.Fatalf("Failed to get notebook: %v", err)
	}

	annotations := nb.GetAnnotations()
	if _, exists := annotations["kubeflow-resource-stopped"]; !exists {
		t.Error("Expected notebook to be stopped (have kubeflow-resource-stopped annotation)")
	}
}

func TestCullNotebooksMultiNamespace_KeepsRecentNotebook(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:         true,
				StudentNamespacePrefix: "cs101",
			},
		},
	}

	recentTime := time.Now().Add(-30 * time.Minute)
	notebook := createNotebook("test-notebook", "cs101-alice", "alice", recentTime, false)

	reconciler, fakeClient := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksMultiNamespace(context.Background(), class, time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksMultiNamespace failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-notebook",
		Namespace: "cs101-alice",
	}, nb)
	if err != nil {
		t.Fatalf("Failed to get notebook: %v", err)
	}

	annotations := nb.GetAnnotations()
	if _, exists := annotations["kubeflow-resource-stopped"]; exists {
		t.Error("Expected notebook to NOT be stopped (should be within cutoff time)")
	}
}

func TestCullNotebooksMultiNamespace_DeletesNotebookForNonGroupUser(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:         true,
				StudentNamespacePrefix: "cs101",
			},
		},
	}

	notebook := createNotebook("test-notebook", "cs101-bob", "bob", time.Now(), false)

	reconciler, fakeClient := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksMultiNamespace(context.Background(), class, time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksMultiNamespace failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-notebook",
		Namespace: "cs101-bob",
	}, nb)
	if err == nil && nb.GetDeletionTimestamp() == nil {
		t.Error("Expected notebook for non-group user to be deleted")
	}
}

func TestCullNotebooksMultiNamespace_SkipsStoppedNotebook(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:         true,
				StudentNamespacePrefix: "cs101",
			},
		},
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	notebook := createNotebook("test-notebook", "cs101-alice", "alice", oldTime, true)

	reconciler, fakeClient := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksMultiNamespace(context.Background(), class, time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksMultiNamespace failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-notebook",
		Namespace: "cs101-alice",
	}, nb)
	if err != nil {
		t.Fatalf("Failed to get notebook: %v", err)
	}
}

func TestCullNotebooksMultiNamespace_SkipsWrongNamespace(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:         true,
				StudentNamespacePrefix: "cs101",
			},
		},
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	notebook := createNotebook("test-notebook", "other-namespace", "alice", oldTime, false)

	reconciler, fakeClient := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksMultiNamespace(context.Background(), class, time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksMultiNamespace failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-notebook",
		Namespace: "other-namespace",
	}, nb)
	if err != nil {
		t.Fatalf("Failed to get notebook: %v", err)
	}

	annotations := nb.GetAnnotations()
	if _, exists := annotations["kubeflow-resource-stopped"]; exists {
		t.Error("Expected notebook in wrong namespace to NOT be stopped")
	}
}

func TestCullNotebooksMultiNamespace_SkipsNoUsernameAnnotation(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:         true,
				StudentNamespacePrefix: "cs101",
			},
		},
	}

	notebook := &unstructured.Unstructured{}
	notebook.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	notebook.SetName("test-notebook")
	notebook.SetNamespace("cs101-alice")
	notebook.SetCreationTimestamp(metav1.Time{Time: time.Now().Add(-2 * time.Hour)})

	reconciler, _ := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksMultiNamespace(context.Background(), class, time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksMultiNamespace should not fail with missing username: %v", err)
	}
}

func TestCullNotebooksMultiNamespace_MatchesPrefixExactly(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:         true,
				StudentNamespacePrefix: "cs101",
			},
		},
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	notebook := createNotebook("test-notebook", "cs101", "alice", oldTime, false)

	reconciler, fakeClient := setupCullerReconciler(t, group, class, notebook)

	err := reconciler.cullNotebooksMultiNamespace(context.Background(), class, time.Now())
	if err != nil {
		t.Fatalf("cullNotebooksMultiNamespace failed: %v", err)
	}

	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "test-notebook",
		Namespace: "cs101",
	}, nb)
	if err != nil {
		t.Fatalf("Failed to get notebook: %v", err)
	}

	annotations := nb.GetAnnotations()
	if _, exists := annotations["kubeflow-resource-stopped"]; !exists {
		t.Error("Expected notebook in exact prefix match to be stopped")
	}
}

func TestReconcile_SingleNamespace_Success(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: false,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"test-namespace"},
		},
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	notebook := createNotebook("test-notebook", "test-namespace", "alice", oldTime, false)

	reconciler, _ := setupCullerReconciler(t, group, class, notebook)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("Expected requeue after 5 minutes, got %v", result.RequeueAfter)
	}
}

func TestReconcile_MultiNamespace_Success(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
			NotebookCulling: nercv1alpha1.NotebookCullingSpec{
				Enabled: true,
				Cutoff:  3600,
			},
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:         true,
				StudentNamespacePrefix: "cs101",
			},
		},
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	notebook := createNotebook("test-notebook", "cs101-alice", "alice", oldTime, false)

	reconciler, _ := setupCullerReconciler(t, group, class, notebook)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("Expected requeue after 5 minutes, got %v", result.RequeueAfter)
	}
}
