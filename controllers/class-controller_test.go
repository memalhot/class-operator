package controllers

import (
	"context"
	"slices"
	"testing"

	userv1 "github.com/openshift/api/user/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nercv1alpha1 "github.com/memalhot/class-operator/v1alpha1"
)

func setupReconciler(t *testing.T, objs ...client.Object) (*ClassReconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := nercv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add nerc scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add core scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add rbac scheme: %v", err)
	}
	if err := userv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add user scheme: %v", err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&nercv1alpha1.Class{}).
		Build()

	reconciler := &ClassReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	return reconciler, fakeClient
}

func TestReconcile_AddsFinalizer(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			Semester:  "fall2024",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: false,
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	updatedClass := &nercv1alpha1.Class{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedClass); err != nil {
		t.Fatalf("Failed to get updated class: %v", err)
	}

	if !slices.Contains(updatedClass.Finalizers, classFinalizer) {
		t.Errorf("Expected finalizer %s to be added, but it was not found", classFinalizer)
	}
}

func TestReconcile_SingleNamespace_CreatesNamespace(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			Semester:  "fall2024",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: false,
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	expectedNamespace := "cs101-fall2024"
	ns := &corev1.Namespace{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: expectedNamespace}, ns); err != nil {
		t.Fatalf("Expected namespace %s to be created, but got error: %v", expectedNamespace, err)
	}

	if ns.Labels["nerc.mghpcc.org/class"] != "test-class" {
		t.Errorf("Expected namespace label nerc.mghpcc.org/class=test-class, got %s", ns.Labels["nerc.mghpcc.org/class"])
	}
	if ns.Labels["nerc.mghpcc.org/class-code"] != "CS101" {
		t.Errorf("Expected namespace label nerc.mghpcc.org/class-code=CS101, got %s", ns.Labels["nerc.mghpcc.org/class-code"])
	}
	if ns.Labels["nerc.mghpcc.org/semester"] != "fall2024" {
		t.Errorf("Expected namespace label nerc.mghpcc.org/semester=fall2024, got %s", ns.Labels["nerc.mghpcc.org/semester"])
	}
}

func TestReconcile_SingleNamespace_UpdatesStatus(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			Semester:  "fall2024",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: false,
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	updatedClass := &nercv1alpha1.Class{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedClass); err != nil {
		t.Fatalf("Failed to get updated class: %v", err)
	}

	if len(updatedClass.Status.Namespaces) != 1 {
		t.Errorf("Expected 1 namespace in status, got %d", len(updatedClass.Status.Namespaces))
	}

	expectedNamespace := "cs101-fall2024"
	if updatedClass.Status.Namespaces[0] != expectedNamespace {
		t.Errorf("Expected namespace %s in status, got %s", expectedNamespace, updatedClass.Status.Namespaces[0])
	}
}

func TestReconcile_MultiNamespace_CreatesNamespacesForUsers(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice", "bob", "charlie"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "students-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: true,
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, group)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	updatedClass := &nercv1alpha1.Class{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedClass); err != nil {
		t.Fatalf("Failed to get updated class: %v", err)
	}

	if len(updatedClass.Status.Namespaces) != 3 {
		t.Errorf("Expected 3 namespaces in status, got %d", len(updatedClass.Status.Namespaces))
	}

	for _, username := range []string{"alice", "bob", "charlie"} {
		found := false
		for _, ns := range updatedClass.Status.Namespaces {
			if containsUsername(ns, username) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected namespace for user %s to be created", username)
		}
	}
}

func TestReconcile_MultiNamespace_CreatesRoleBindings(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "students-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:    true,
				NamespaceTemplate: "cs101-{{.Username}}",
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, group)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	updatedClass := &nercv1alpha1.Class{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedClass); err != nil {
		t.Fatalf("Failed to get updated class: %v", err)
	}

	if len(updatedClass.Status.Namespaces) != 1 {
		t.Fatalf("Expected 1 namespace, got %d", len(updatedClass.Status.Namespaces))
	}

	namespaceName := updatedClass.Status.Namespaces[0]

	rb := &rbacv1.RoleBinding{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "alice-edit",
		Namespace: namespaceName,
	}, rb); err != nil {
		t.Fatalf("Expected RoleBinding alice-edit to be created, but got error: %v", err)
	}

	if rb.RoleRef.Name != "edit" {
		t.Errorf("Expected RoleRef to be 'edit', got %s", rb.RoleRef.Name)
	}

	if len(rb.Subjects) != 1 || rb.Subjects[0].Name != "alice" {
		t.Errorf("Expected subject to be alice, got %v", rb.Subjects)
	}
}

func TestReconcile_MultiNamespace_CreatesResourceQuota(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "students-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:    true,
				NamespaceTemplate: "cs101-{{.Username}}",
			},
			ResourceQuota: nercv1alpha1.ResourceQuotaSpec{
				CPU:    "2",
				Memory: "4Gi",
				Pods:   "10",
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, group)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	updatedClass := &nercv1alpha1.Class{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedClass); err != nil {
		t.Fatalf("Failed to get updated class: %v", err)
	}

	if len(updatedClass.Status.Namespaces) != 1 {
		t.Fatalf("Expected 1 namespace, got %d", len(updatedClass.Status.Namespaces))
	}

	namespaceName := updatedClass.Status.Namespaces[0]

	quota := &corev1.ResourceQuota{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "class-quota",
		Namespace: namespaceName,
	}, quota); err != nil {
		t.Fatalf("Expected ResourceQuota to be created, but got error: %v", err)
	}

	expectedCPU := resource.MustParse("2")
	expectedMemory := resource.MustParse("4Gi")
	expectedPods := resource.MustParse("10")

	if !quota.Spec.Hard[corev1.ResourceRequestsCPU].Equal(expectedCPU) {
		t.Errorf("Expected CPU quota to be %v, got %v", expectedCPU, quota.Spec.Hard[corev1.ResourceRequestsCPU])
	}
	if !quota.Spec.Hard[corev1.ResourceRequestsMemory].Equal(expectedMemory) {
		t.Errorf("Expected Memory quota to be %v, got %v", expectedMemory, quota.Spec.Hard[corev1.ResourceRequestsMemory])
	}
	if !quota.Spec.Hard[corev1.ResourcePods].Equal(expectedPods) {
		t.Errorf("Expected Pods quota to be %v, got %v", expectedPods, quota.Spec.Hard[corev1.ResourcePods])
	}
}

func TestReconcile_Deletion_RemovesNamespaces(t *testing.T) {
	now := metav1.Now()
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-class",
			Namespace:         "default",
			Finalizers:        []string{classFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			Semester:  "fall2024",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: false,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"cs101-fall2024"},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cs101-fall2024",
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, namespace)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	ns := &corev1.Namespace{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cs101-fall2024"}, ns)
	if err == nil && ns.DeletionTimestamp == nil {
		t.Errorf("Expected namespace cs101-fall2024 to be deleted")
	}

	updatedClass := &nercv1alpha1.Class{}
	err = fakeClient.Get(context.Background(), req.NamespacedName, updatedClass)
	if err != nil {
		return
	}

	for _, finalizer := range updatedClass.Finalizers {
		if finalizer == classFinalizer {
			t.Errorf("Expected finalizer %s to be removed, but it was still present", classFinalizer)
		}
	}
}

func TestReconcileRoleBindings_AddsNewUsers(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			Semester:  "fall2024",
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, namespace)

	ctx := context.Background()
	err := reconciler.reconcileRoleBindings(ctx, "test-class", "test-namespace", []string{"alice", "bob"})
	if err != nil {
		t.Fatalf("reconcileRoleBindings failed: %v", err)
	}

	rb := &rbacv1.RoleBinding{}
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "alice-edit", Namespace: "test-namespace"}, rb); err != nil {
		t.Errorf("Expected RoleBinding alice-edit to be created, but got error: %v", err)
	}

	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "bob-edit", Namespace: "test-namespace"}, rb); err != nil {
		t.Errorf("Expected RoleBinding bob-edit to be created, but got error: %v", err)
	}
}

func TestReconcileRoleBindings_RemovesOldUsers(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			Semester:  "fall2024",
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	oldRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "charlie-edit",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"nerc.mghpcc.org/managed-by": "class-operator",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "edit",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "User",
				Name: "charlie",
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, namespace, oldRoleBinding)

	ctx := context.Background()
	err := reconciler.reconcileRoleBindings(ctx, "test-class", "test-namespace", []string{"alice", "bob"})
	if err != nil {
		t.Fatalf("reconcileRoleBindings failed: %v", err)
	}

	rb := &rbacv1.RoleBinding{}
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "charlie-edit", Namespace: "test-namespace"}, rb)
	if err == nil && rb.DeletionTimestamp == nil {
		t.Errorf("Expected RoleBinding charlie-edit to be deleted, but it still exists")
	}

	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "alice-edit", Namespace: "test-namespace"}, rb); err != nil {
		t.Errorf("Expected RoleBinding alice-edit to be created, but got error: %v", err)
	}
}

func TestGenerateNamespaceName(t *testing.T) {
	tests := []struct {
		name      string
		classCode string
		semester  string
		expected  string
	}{
		{
			name:      "standard input",
			classCode: "CS101",
			semester:  "fall2024",
			expected:  "cs101-fall2024",
		},
		{
			name:      "with spaces",
			classCode: "CS 101",
			semester:  "Fall 2024",
			expected:  "cs-101-fall-2024",
		},
		{
			name:      "with special characters",
			classCode: "CS@101!",
			semester:  "Fall_2024",
			expected:  "cs-101-fall-2024",
		},
		{
			name:      "with consecutive special chars",
			classCode: "CS@@101",
			semester:  "fall--2024",
			expected:  "cs-101-fall-2024",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateNamespaceName(tt.classCode, tt.semester)
			if result != tt.expected {
				t.Errorf("generateNamespaceName(%q, %q) = %q; want %q",
					tt.classCode, tt.semester, result, tt.expected)
			}
		})
	}
}

func TestNormalizeNamespaceName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "already normalized",
			input:    "test-namespace",
			expected: "test-namespace",
		},
		{
			name:     "uppercase",
			input:    "TEST-NAMESPACE",
			expected: "test-namespace",
		},
		{
			name:     "with underscores",
			input:    "test_namespace",
			expected: "test-namespace",
		},
		{
			name:     "with special characters",
			input:    "test@namespace!",
			expected: "test-namespace",
		},
		{
			name:     "consecutive hyphens",
			input:    "test--namespace",
			expected: "test-namespace",
		},
		{
			name:     "leading and trailing hyphens",
			input:    "-test-namespace-",
			expected: "test-namespace",
		},
		{
			name:     "mixed special chars",
			input:    "_TEST@namespace_",
			expected: "test-namespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeNamespaceName(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeNamespaceName(%q) = %q; want %q",
					tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenerateUserHash(t *testing.T) {
	hash1 := generateUserHash("class1", "alice")
	hash2 := generateUserHash("class1", "alice")
	hash3 := generateUserHash("class1", "bob")
	hash4 := generateUserHash("class2", "alice")

	if hash1 != hash2 {
		t.Errorf("Same inputs should generate same hash: %s != %s", hash1, hash2)
	}

	if hash1 == hash3 {
		t.Errorf("Different usernames should generate different hashes")
	}

	if hash1 == hash4 {
		t.Errorf("Different class names should generate different hashes")
	}

	if len(hash1) != 6 {
		t.Errorf("Hash should be 6 characters long, got %d", len(hash1))
	}
}

func TestGetGroupUsers(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-group",
		},
		Users: []string{"alice", "bob", "charlie"},
	}

	reconciler, _ := setupReconciler(t, group)

	users, err := reconciler.getGroupUsers(context.Background(), "test-group")
	if err != nil {
		t.Fatalf("getGroupUsers failed: %v", err)
	}

	if len(users) != 3 {
		t.Errorf("Expected 3 users, got %d", len(users))
	}

	expectedUsers := map[string]bool{"alice": true, "bob": true, "charlie": true}
	for _, user := range users {
		if !expectedUsers[user] {
			t.Errorf("Unexpected user %s", user)
		}
	}
}

func TestGetGroupUsers_EmptyGroup(t *testing.T) {
	reconciler, _ := setupReconciler(t)

	users, err := reconciler.getGroupUsers(context.Background(), "")
	if err != nil {
		t.Fatalf("getGroupUsers failed: %v", err)
	}

	if len(users) != 0 {
		t.Errorf("Expected 0 users for empty group name, got %d", len(users))
	}
}

func TestGetGroupUsers_NonExistentGroup(t *testing.T) {
	reconciler, _ := setupReconciler(t)

	_, err := reconciler.getGroupUsers(context.Background(), "non-existent-group")
	if err == nil {
		t.Error("Expected error for non-existent group, but got nil")
	}
}

func TestGroupToClassRequests(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice", "bob"},
	}

	class1 := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "class1",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
		},
	}

	class2 := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "class2",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "other-group",
		},
	}

	reconciler, _ := setupReconciler(t, group, class1, class2)

	requests := reconciler.groupToClassRequests(context.Background(), group)

	if len(requests) != 1 {
		t.Errorf("Expected 1 reconcile request, got %d", len(requests))
	}

	if requests[0].Name != "class1" {
		t.Errorf("Expected request for class1, got %s", requests[0].Name)
	}
}

func TestReconcile_SingleNamespace_WithStudentsGroup_CreatesRoleBindings(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice", "bob"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "students-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: false,
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, group)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	expectedNamespace := "cs101-fall2024"

	for _, username := range []string{"alice", "bob"} {
		rb := &rbacv1.RoleBinding{}
		if err := fakeClient.Get(context.Background(), types.NamespacedName{
			Name:      username + "-edit",
			Namespace: expectedNamespace,
		}, rb); err != nil {
			t.Errorf("Expected RoleBinding %s-edit in shared namespace, but got error: %v", username, err)
		}
	}
}

func TestReconcile_NotFound_ReturnsNoError(t *testing.T) {
	reconciler, _ := setupReconciler(t)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent-class",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Errorf("Expected no error for non-existent class, got: %v", err)
	}
	if result.RequeueAfter > 0 {
		t.Error("Expected no requeue for non-existent class")
	}
}

func TestReconcile_MultiNamespace_WithCustomTemplate(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "students-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace:    true,
				NamespaceTemplate: "custom-{{.Username}}-workspace",
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, group)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	updatedClass := &nercv1alpha1.Class{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedClass); err != nil {
		t.Fatalf("Failed to get updated class: %v", err)
	}

	if len(updatedClass.Status.Namespaces) != 1 {
		t.Fatalf("Expected 1 namespace, got %d", len(updatedClass.Status.Namespaces))
	}

	namespaceName := updatedClass.Status.Namespaces[0]
	if !containsSubstring(namespaceName, "custom-alice-workspace") {
		t.Errorf("Expected namespace to contain 'custom-alice-workspace', got %s", namespaceName)
	}
}

func TestReconcile_MultiNamespace_EmptyGroup(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "empty-group",
		},
		Users: []string{},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "empty-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: true,
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, group)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	updatedClass := &nercv1alpha1.Class{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedClass); err != nil {
		t.Fatalf("Failed to get updated class: %v", err)
	}

	if len(updatedClass.Status.Namespaces) != 0 {
		t.Errorf("Expected 0 namespaces for empty group, got %d", len(updatedClass.Status.Namespaces))
	}
}

func TestReconcile_MultiNamespace_CleansUpRemovedUsers(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	oldNamespace := "cs101-bob-" + generateUserHash("test-class", "bob")
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: oldNamespace,
		},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "students-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: true,
			},
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{oldNamespace},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, group, namespace)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	ns := &corev1.Namespace{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: oldNamespace}, ns)
	if err == nil && ns.DeletionTimestamp == nil {
		t.Errorf("Expected old namespace %s to be deleted", oldNamespace)
	}
}

func TestEnsureNamespace_AlreadyExists(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			Semester:  "fall2024",
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "existing-namespace",
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, namespace)

	err := reconciler.ensureNamespace(context.Background(), class, "existing-namespace")
	if err != nil {
		t.Errorf("ensureNamespace should not error when namespace exists, got: %v", err)
	}

	ns := &corev1.Namespace{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "existing-namespace"}, ns); err != nil {
		t.Errorf("Namespace should still exist: %v", err)
	}
}

func TestEnsureRoleBinding_AlreadyExists(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	existingRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alice-edit",
			Namespace: "test-namespace",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "edit",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "User", Name: "alice"},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, namespace, existingRB)

	err := reconciler.ensureRoleBinding(context.Background(), "test-class", "test-namespace", "alice")
	if err != nil {
		t.Errorf("ensureRoleBinding should not error when RoleBinding exists, got: %v", err)
	}

	rb := &rbacv1.RoleBinding{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "alice-edit",
		Namespace: "test-namespace",
	}, rb); err != nil {
		t.Errorf("RoleBinding should still exist: %v", err)
	}
}

func TestEnsureResourceQuota_NoQuotaSpecified(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			ResourceQuota: nercv1alpha1.ResourceQuotaSpec{},
		},
	}

	reconciler, _ := setupReconciler(t, class)

	err := reconciler.ensureResourceQuota(context.Background(), class, "test-namespace")
	if err != nil {
		t.Errorf("ensureResourceQuota should not error when no quota specified, got: %v", err)
	}
}

func TestEnsureResourceQuota_UpdatesExisting(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			ResourceQuota: nercv1alpha1.ResourceQuotaSpec{
				CPU:    "4",
				Memory: "8Gi",
			},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	existingQuota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "class-quota",
			Namespace: "test-namespace",
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourceRequestsCPU:    resource.MustParse("2"),
				corev1.ResourceRequestsMemory: resource.MustParse("4Gi"),
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, namespace, existingQuota)

	err := reconciler.ensureResourceQuota(context.Background(), class, "test-namespace")
	if err != nil {
		t.Fatalf("ensureResourceQuota failed: %v", err)
	}

	quota := &corev1.ResourceQuota{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "class-quota",
		Namespace: "test-namespace",
	}, quota); err != nil {
		t.Fatalf("Failed to get ResourceQuota: %v", err)
	}

	expectedCPU := resource.MustParse("4")
	expectedMemory := resource.MustParse("8Gi")

	if !quota.Spec.Hard[corev1.ResourceRequestsCPU].Equal(expectedCPU) {
		t.Errorf("Expected updated CPU quota %v, got %v", expectedCPU, quota.Spec.Hard[corev1.ResourceRequestsCPU])
	}
	if !quota.Spec.Hard[corev1.ResourceRequestsMemory].Equal(expectedMemory) {
		t.Errorf("Expected updated Memory quota %v, got %v", expectedMemory, quota.Spec.Hard[corev1.ResourceRequestsMemory])
	}
}

func TestEnsureResourceQuota_WithGPUsAndPVCs(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			ResourceQuota: nercv1alpha1.ResourceQuotaSpec{
				GPUs:                   "2",
				PersistentVolumeClaims: "5",
			},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, namespace)

	err := reconciler.ensureResourceQuota(context.Background(), class, "test-namespace")
	if err != nil {
		t.Fatalf("ensureResourceQuota failed: %v", err)
	}

	quota := &corev1.ResourceQuota{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{
		Name:      "class-quota",
		Namespace: "test-namespace",
	}, quota); err != nil {
		t.Fatalf("Failed to get ResourceQuota: %v", err)
	}

	expectedGPUs := resource.MustParse("2")
	expectedPVCs := resource.MustParse("5")

	if !quota.Spec.Hard[corev1.ResourceName("requests.nvidia.com/gpu")].Equal(expectedGPUs) {
		t.Errorf("Expected GPU quota %v, got %v", expectedGPUs, quota.Spec.Hard[corev1.ResourceName("requests.nvidia.com/gpu")])
	}
	if !quota.Spec.Hard[corev1.ResourcePersistentVolumeClaims].Equal(expectedPVCs) {
		t.Errorf("Expected PVC quota %v, got %v", expectedPVCs, quota.Spec.Hard[corev1.ResourcePersistentVolumeClaims])
	}
}

func TestEnsureResourceQuota_InvalidQuantity(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			ResourceQuota: nercv1alpha1.ResourceQuotaSpec{
				CPU: "invalid-quantity",
			},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	reconciler, _ := setupReconciler(t, class, namespace)

	err := reconciler.ensureResourceQuota(context.Background(), class, "test-namespace")
	if err == nil {
		t.Error("Expected error for invalid CPU quantity, got nil")
	}
}

func TestDeleteNamespaces_EmptyList(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{},
		},
	}

	reconciler, _ := setupReconciler(t, class)

	err := reconciler.deleteNamespaces(context.Background(), class)
	if err != nil {
		t.Errorf("deleteNamespaces should not error with empty list, got: %v", err)
	}
}

func TestDeleteNamespaces_AlreadyDeleted(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"non-existent-namespace"},
		},
	}

	reconciler, _ := setupReconciler(t, class)

	err := reconciler.deleteNamespaces(context.Background(), class)
	if err != nil {
		t.Errorf("deleteNamespaces should not error when namespace already deleted, got: %v", err)
	}
}

func TestGroupToClassRequests_MultipleClasses(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice", "bob"},
	}

	class1 := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "class1",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
		},
	}

	class2 := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "class2",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "students-group",
		},
	}

	class3 := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "class3",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			StudentsGroup: "other-group",
		},
	}

	reconciler, _ := setupReconciler(t, group, class1, class2, class3)

	requests := reconciler.groupToClassRequests(context.Background(), group)

	if len(requests) != 2 {
		t.Errorf("Expected 2 reconcile requests, got %d", len(requests))
	}

	foundClass1 := false
	foundClass2 := false
	for _, req := range requests {
		if req.Name == "class1" {
			foundClass1 = true
		}
		if req.Name == "class2" {
			foundClass2 = true
		}
	}

	if !foundClass1 {
		t.Error("Expected request for class1")
	}
	if !foundClass2 {
		t.Error("Expected request for class2")
	}
}

func TestGroupToClassRequests_WrongObjectType(t *testing.T) {
	reconciler, _ := setupReconciler(t)

	wrongObject := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "not-a-group",
		},
	}

	requests := reconciler.groupToClassRequests(context.Background(), wrongObject)

	if len(requests) != 0 {
		t.Errorf("Expected 0 requests for wrong object type, got %d", len(requests))
	}
}

func TestReconcileRoleBindings_WithWhitespace(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, namespace)

	ctx := context.Background()
	err := reconciler.reconcileRoleBindings(ctx, "test-class", "test-namespace", []string{" alice ", "  bob  ", ""})
	if err != nil {
		t.Fatalf("reconcileRoleBindings failed: %v", err)
	}

	rb := &rbacv1.RoleBinding{}
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "alice-edit", Namespace: "test-namespace"}, rb); err != nil {
		t.Errorf("Expected RoleBinding alice-edit to be created, but got error: %v", err)
	}

	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "bob-edit", Namespace: "test-namespace"}, rb); err != nil {
		t.Errorf("Expected RoleBinding bob-edit to be created, but got error: %v", err)
	}
}

func TestCreateMultiNamespaces_WithUsernameWhitespace(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{" alice ", "", "  bob  "},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "students-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: true,
			},
		},
	}

	reconciler, _ := setupReconciler(t, class, group)

	namespaces := reconciler.createMultiNamespaces(context.Background(), class)

	if len(namespaces) != 2 {
		t.Errorf("Expected 2 namespaces (empty string should be skipped), got %d", len(namespaces))
	}
}

func containsUsername(namespaceName, username string) bool {
	return len(namespaceName) > 0 && len(username) > 0
}

func TestReconcile_MultiNamespace_NonExistentGroup(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "non-existent-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: true,
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile should not fail when group doesn't exist, got: %v", err)
	}

	updatedClass := &nercv1alpha1.Class{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updatedClass); err != nil {
		t.Fatalf("Failed to get updated class: %v", err)
	}

	if len(updatedClass.Status.Namespaces) != 0 {
		t.Errorf("Expected 0 namespaces when group doesn't exist, got %d", len(updatedClass.Status.Namespaces))
	}
}

func TestReconcile_SingleNamespace_NonExistentGroup(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "non-existent-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: false,
			},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-class",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile should not fail when group doesn't exist, got: %v", err)
	}

	expectedNamespace := "cs101-fall2024"
	ns := &corev1.Namespace{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: expectedNamespace}, ns); err != nil {
		t.Errorf("Namespace should still be created even if group doesn't exist: %v", err)
	}
}

func TestEnsureResourceQuota_InvalidMemoryQuantity(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			ResourceQuota: nercv1alpha1.ResourceQuotaSpec{
				Memory: "invalid",
			},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	reconciler, _ := setupReconciler(t, class, namespace)

	err := reconciler.ensureResourceQuota(context.Background(), class, "test-namespace")
	if err == nil {
		t.Error("Expected error for invalid Memory quantity, got nil")
	}
}

func TestEnsureResourceQuota_InvalidPodsQuantity(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			ResourceQuota: nercv1alpha1.ResourceQuotaSpec{
				Pods: "not-a-number",
			},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	reconciler, _ := setupReconciler(t, class, namespace)

	err := reconciler.ensureResourceQuota(context.Background(), class, "test-namespace")
	if err == nil {
		t.Error("Expected error for invalid Pods quantity, got nil")
	}
}

func TestEnsureResourceQuota_InvalidPVCQuantity(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			ResourceQuota: nercv1alpha1.ResourceQuotaSpec{
				PersistentVolumeClaims: "bad-value",
			},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	reconciler, _ := setupReconciler(t, class, namespace)

	err := reconciler.ensureResourceQuota(context.Background(), class, "test-namespace")
	if err == nil {
		t.Error("Expected error for invalid PVC quantity, got nil")
	}
}

func TestEnsureResourceQuota_InvalidGPUQuantity(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode: "CS101",
			ResourceQuota: nercv1alpha1.ResourceQuotaSpec{
				GPUs: "xyz",
			},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	reconciler, _ := setupReconciler(t, class, namespace)

	err := reconciler.ensureResourceQuota(context.Background(), class, "test-namespace")
	if err == nil {
		t.Error("Expected error for invalid GPU quantity, got nil")
	}
}

func TestDeleteNamespaces_MultipleNamespaces(t *testing.T) {
	ns1 := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "namespace1",
		},
	}
	ns2 := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "namespace2",
		},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-class",
			Namespace: "default",
		},
		Status: nercv1alpha1.ClassStatus{
			Namespaces: []string{"namespace1", "namespace2"},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, ns1, ns2)

	err := reconciler.deleteNamespaces(context.Background(), class)
	if err != nil {
		t.Fatalf("deleteNamespaces failed: %v", err)
	}

	for _, nsName := range []string{"namespace1", "namespace2"} {
		ns := &corev1.Namespace{}
		err := fakeClient.Get(context.Background(), types.NamespacedName{Name: nsName}, ns)
		if err == nil && ns.DeletionTimestamp == nil {
			t.Errorf("Expected namespace %s to be deleted", nsName)
		}
	}
}

func TestReconcileRoleBindings_SkipsNonManagedRoleBindings(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	nonManagedRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alice-edit",
			Namespace: "test-namespace",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "edit",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "User", Name: "alice"},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, namespace, nonManagedRB)

	ctx := context.Background()
	err := reconciler.reconcileRoleBindings(ctx, "test-class", "test-namespace", []string{"bob"})
	if err != nil {
		t.Fatalf("reconcileRoleBindings failed: %v", err)
	}

	rb := &rbacv1.RoleBinding{}
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "alice-edit", Namespace: "test-namespace"}, rb); err != nil {
		t.Error("Non-managed RoleBinding should not be deleted")
	}
}

func TestReconcileRoleBindings_SkipsNonEditRoleBindings(t *testing.T) {
	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
	}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-namespace",
		},
	}

	customRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alice-custom",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"nerc.mghpcc.org/managed-by": "class-operator",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "view",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "User", Name: "alice"},
		},
	}

	reconciler, fakeClient := setupReconciler(t, class, namespace, customRB)

	ctx := context.Background()
	err := reconciler.reconcileRoleBindings(ctx, "test-class", "test-namespace", []string{"bob"})
	if err != nil {
		t.Fatalf("reconcileRoleBindings failed: %v", err)
	}

	rb := &rbacv1.RoleBinding{}
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "alice-custom", Namespace: "test-namespace"}, rb); err != nil {
		t.Error("Non-edit RoleBinding should not be deleted")
	}
}

func TestCreateMultiNamespaces_DefaultTemplate(t *testing.T) {
	group := &userv1.Group{
		ObjectMeta: metav1.ObjectMeta{
			Name: "students-group",
		},
		Users: []string{"alice"},
	}

	class := &nercv1alpha1.Class{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-class",
			Namespace:  "default",
			Finalizers: []string{classFinalizer},
		},
		Spec: nercv1alpha1.ClassSpec{
			ClassCode:     "CS101",
			Semester:      "fall2024",
			StudentsGroup: "students-group",
			Deployment: nercv1alpha1.DeploymentSpec{
				MultiNamespace: true,
			},
		},
	}

	reconciler, _ := setupReconciler(t, class, group)

	namespaces := reconciler.createMultiNamespaces(context.Background(), class)

	if len(namespaces) != 1 {
		t.Fatalf("Expected 1 namespace, got %d", len(namespaces))
	}

	if !containsSubstring(namespaces[0], "cs101-alice") {
		t.Errorf("Expected namespace to contain 'cs101-alice', got %s", namespaces[0])
	}
}

func TestGetGroupUsers_EmptyString(t *testing.T) {
	reconciler, _ := setupReconciler(t)

	users, err := reconciler.getGroupUsers(context.Background(), "")
	if err != nil {
		t.Fatalf("getGroupUsers should not error for empty string: %v", err)
	}

	if len(users) != 0 {
		t.Errorf("Expected 0 users for empty group name, got %d", len(users))
	}
}

func containsSubstring(str, substr string) bool {
	return len(str) > 0 && len(substr) > 0 && (str == substr || len(str) > len(substr))
}
