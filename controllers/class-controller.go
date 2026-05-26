package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	userv1 "github.com/openshift/api/user/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nercv1alpha1 "github.com/memalhot/class-operator/v1alpha1"
)

type ClassReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=classes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=classes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=classes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=resourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=user.openshift.io,resources=groups,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete

const classFinalizer = "nerc.mghpcc.org/class-finalizer"

func (r *ClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var class nercv1alpha1.Class
	if err := r.Get(ctx, req.NamespacedName, &class); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !class.DeletionTimestamp.IsZero() {
		if slices.Contains(class.Finalizers, classFinalizer) {
			// Class is being deleted, clean up namespaces
			if err := r.deleteNamespaces(ctx, &class); err != nil {
				logger.Error(err, "Failed to delete namespaces", "class", class.Name)
				return ctrl.Result{}, err
			}

			// Remove finalizer
			class.Finalizers = slices.DeleteFunc(class.Finalizers, func(s string) bool {
				return s == classFinalizer
			})
			if err := r.Update(ctx, &class); err != nil {
				logger.Error(err, "Failed to remove finalizer", "class", class.Name)
				return ctrl.Result{}, err
			}
			logger.Info("Successfully cleaned up class namespaces", "class", class.Name)
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !slices.Contains(class.Finalizers, classFinalizer) {
		class.Finalizers = append(class.Finalizers, classFinalizer)
		if err := r.Update(ctx, &class); err != nil {
			logger.Error(err, "Failed to add finalizer", "class", class.Name)
			return ctrl.Result{}, err
		}
		logger.Info("Added finalizer to class", "class", class.Name)
	}

	var createdNamespaces []string

	if class.Spec.Deployment.MultiNamespace {
		// Multi-namespace mode: create namespace per user
		logger.Info("Processing multi-namespace class", "class", class.Name)
		namespaces, err := r.createMultiNamespaces(ctx, &class)
		if err != nil {
			logger.Error(err, "Failed to create multi-namespaces", "class", class.Name)
			return ctrl.Result{}, err
		}
		createdNamespaces = namespaces
	} else {
		// Single-namespace mode: create one namespace
		namespaceName := generateNamespaceName(class.Spec.ClassCode, class.Spec.Semester)
		logger.Info("Processing single-namespace class", "class", class.Name, "namespace", namespaceName)

		if err := r.ensureNamespace(ctx, &class, namespaceName); err != nil {
			return ctrl.Result{}, err
		}

		// Grant edit permissions to all students in the shared namespace (if studentsGroup is specified)
		if class.Spec.StudentsGroup != "" {
			users, err := r.getGroupUsers(ctx, class.Spec.StudentsGroup)
			if err != nil {
				logger.Info("Could not get group users, skipping RoleBinding reconciliation", "class", class.Name, "group", class.Spec.StudentsGroup, "error", err.Error())
				// Don't fail the reconciliation - just skip RoleBinding reconciliation
			} else {
				// Reconcile RoleBindings - add new ones and remove old ones
				if err := r.reconcileRoleBindings(ctx, namespaceName, users); err != nil {
					logger.Error(err, "Failed to reconcile RoleBindings in shared namespace", "namespace", namespaceName)
					// Continue even if reconciliation fails
				}
			}
		}

		createdNamespaces = []string{namespaceName}
	}

	// Update status with all created namespaces
	if !slices.Equal(class.Status.Namespaces, createdNamespaces) {
		class.Status.Namespaces = createdNamespaces
		if err := r.Status().Update(ctx, &class); err != nil {
			logger.Error(err, "Failed to update class status", "class", class.Name)
			return ctrl.Result{}, err
		}
		logger.Info("Updated class status with namespaces", "class", class.Name, "count", len(createdNamespaces))
	}

	return ctrl.Result{}, nil
}

// ensureNamespace creates a namespace if it doesn't exist
func (r *ClassReconciler) ensureNamespace(ctx context.Context, class *nercv1alpha1.Class, namespaceName string) error {
	logger := log.FromContext(ctx)

	namespace := &corev1.Namespace{}
	err := r.Get(ctx, client.ObjectKey{Name: namespaceName}, namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create the namespace
			logger.Info("Creating namespace for class", "class", class.Name, "namespace", namespaceName)

			namespace = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespaceName,
					Labels: map[string]string{
						"nerc.mghpcc.org/class":      class.Name,
						"nerc.mghpcc.org/class-code": class.Spec.ClassCode,
						"nerc.mghpcc.org/semester":   class.Spec.Semester,
					},
				},
			}

			if err := r.Create(ctx, namespace); err != nil {
				logger.Error(err, "Failed to create namespace", "namespace", namespaceName)
				return err
			}

			logger.Info("Successfully created namespace", "namespace", namespaceName)
		} else {
			logger.Error(err, "Failed to get namespace", "namespace", namespaceName)
			return err
		}
	} else {
		logger.V(1).Info("Namespace already exists", "namespace", namespaceName)
	}

	return nil
}

// ensureRoleBinding creates a RoleBinding if it doesn't exist to grant edit permissions to a user
func (r *ClassReconciler) ensureRoleBinding(ctx context.Context, namespaceName, username string) error {
	logger := log.FromContext(ctx)

	roleBindingName := fmt.Sprintf("%s-edit", username)
	roleBinding := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: roleBindingName, Namespace: namespaceName}, roleBinding)

	if err != nil {
		if errors.IsNotFound(err) {
			// Create the RoleBinding
			logger.Info("Creating RoleBinding for user", "user", username, "namespace", namespaceName)

			roleBinding = &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      roleBindingName,
					Namespace: namespaceName,
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
						Name: username,
					},
				},
			}

			if err := r.Create(ctx, roleBinding); err != nil {
				logger.Error(err, "Failed to create RoleBinding", "user", username, "namespace", namespaceName)
				return err
			}

			logger.Info("Successfully created RoleBinding", "user", username, "namespace", namespaceName)
		} else {
			logger.Error(err, "Failed to get RoleBinding", "user", username, "namespace", namespaceName)
			return err
		}
	} else {
		logger.V(1).Info("RoleBinding already exists", "user", username, "namespace", namespaceName)
	}

	return nil
}

// reconcileRoleBindings ensures RoleBindings match the current user list
// It creates RoleBindings for new users and removes RoleBindings for users no longer in the list
func (r *ClassReconciler) reconcileRoleBindings(ctx context.Context, namespaceName string, currentUsers []string) error {
	logger := log.FromContext(ctx)

	// Create a set of current usernames for quick lookup
	currentUserSet := make(map[string]bool)
	for _, username := range currentUsers {
		username = strings.TrimSpace(username)
		if username != "" {
			currentUserSet[username] = true
		}
	}

	// Get all RoleBindings in the namespace managed by class-operator
	roleBindingList := &rbacv1.RoleBindingList{}
	if err := r.List(ctx, roleBindingList, client.InNamespace(namespaceName), client.MatchingLabels{
		"nerc.mghpcc.org/managed-by": "class-operator",
	}); err != nil {
		logger.Error(err, "Failed to list RoleBindings", "namespace", namespaceName)
		return err
	}

	// Remove RoleBindings for users no longer in the group
	for _, rb := range roleBindingList.Items {
		// Extract username from RoleBinding name (format: {username}-edit)
		if !strings.HasSuffix(rb.Name, "-edit") {
			continue
		}
		username := strings.TrimSuffix(rb.Name, "-edit")

		if !currentUserSet[username] {
			// User is no longer in the group, delete the RoleBinding
			logger.Info("Removing RoleBinding for user no longer in group", "user", username, "namespace", namespaceName)
			if err := r.Delete(ctx, &rb); err != nil {
				if !errors.IsNotFound(err) {
					logger.Error(err, "Failed to delete RoleBinding", "user", username, "namespace", namespaceName)
					// Continue with other RoleBindings even if one fails
				}
			} else {
				logger.Info("Successfully removed RoleBinding", "user", username, "namespace", namespaceName)
			}
		}
	}

	// Create RoleBindings for current users
	for username := range currentUserSet {
		if err := r.ensureRoleBinding(ctx, namespaceName, username); err != nil {
			logger.Error(err, "Failed to ensure RoleBinding for user", "user", username, "namespace", namespaceName)
			// Continue with other users even if one fails
		}
	}

	return nil
}

// createMultiNamespaces creates a namespace for each user in the students group
func (r *ClassReconciler) createMultiNamespaces(ctx context.Context, class *nercv1alpha1.Class) ([]string, error) {
	logger := log.FromContext(ctx)

	// Get users from the students group
	users, err := r.getGroupUsers(ctx, class.Spec.StudentsGroup)
	if err != nil {
		logger.Info("Could not get group users, no namespaces will be created", "class", class.Name, "group", class.Spec.StudentsGroup, "error", err.Error())
		return []string{}, nil
	}

	if len(users) == 0 {
		logger.Info("No users found in group", "class", class.Name, "group", class.Spec.StudentsGroup)
		return []string{}, nil
	}

	logger.Info("Creating namespaces for users", "class", class.Name, "userCount", len(users))

	var createdNamespaces []string
	template := class.Spec.Deployment.NamespaceTemplate
	if template == "" {
		// Default template if not specified
		template = fmt.Sprintf("%s-{{.Username}}", class.Spec.ClassCode)
	}

	for _, username := range users {
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}

		// Generate namespace name from template
		namespaceName := strings.ReplaceAll(template, "{{.Username}}", username)

		// Add hash suffix for uniqueness
		hash := generateUserHash(class.Name, username)
		namespaceName = fmt.Sprintf("%s-%s", namespaceName, hash)
		namespaceName = normalizeNamespaceName(namespaceName)

		// Create namespace for this user
		if err := r.ensureNamespace(ctx, class, namespaceName); err != nil {
			logger.Error(err, "Failed to create namespace for user", "user", username, "namespace", namespaceName)
			continue
		}

		// Reconcile RoleBindings for this user's namespace (should only have this one user)
		if err := r.reconcileRoleBindings(ctx, namespaceName, []string{username}); err != nil {
			logger.Error(err, "Failed to reconcile RoleBindings for user", "user", username, "namespace", namespaceName)
			// Don't skip adding to createdNamespaces - namespace exists even if RoleBinding failed
		}

		createdNamespaces = append(createdNamespaces, namespaceName)
	}

	logger.Info("Completed namespace creation", "class", class.Name, "created", len(createdNamespaces))

	// Clean up namespaces for users who are no longer in the group
	// Compare current namespaces in status with what we just created
	createdNamespaceSet := make(map[string]bool)
	for _, ns := range createdNamespaces {
		createdNamespaceSet[ns] = true
	}

	// Check if there are namespaces in status that shouldn't exist anymore
	for _, statusNamespace := range class.Status.Namespaces {
		if !createdNamespaceSet[statusNamespace] {
			// This namespace exists but the user is no longer in the group
			logger.Info("Removing namespace for user no longer in group", "namespace", statusNamespace)
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: statusNamespace,
				},
			}
			if err := r.Delete(ctx, namespace); err != nil {
				if !errors.IsNotFound(err) {
					logger.Error(err, "Failed to delete namespace for removed user", "namespace", statusNamespace)
					// Don't return error, continue with cleanup
				}
			} else {
				logger.Info("Successfully removed namespace", "namespace", statusNamespace)
			}
		}
	}

	return createdNamespaces, nil
}

// getGroupUsers retrieves users from an OpenShift group
func (r *ClassReconciler) getGroupUsers(ctx context.Context, groupName string) ([]string, error) {
	logger := log.FromContext(ctx)

	if groupName == "" {
		return []string{}, nil
	}

	group := &userv1.Group{}
	if err := r.Get(ctx, types.NamespacedName{Name: groupName}, group); err != nil {
		return nil, fmt.Errorf("failed to get group %s: %w", groupName, err)
	}

	if len(group.Users) == 0 {
		logger.V(1).Info("Group is empty", "group", groupName)
	}

	return group.Users, nil
}

// generateUserHash creates a deterministic short hash from class name and username
// This ensures uniqueness even if multiple students have the same username
func generateUserHash(className, username string) string {
	// Combine class name and username to ensure uniqueness per class
	data := fmt.Sprintf("%s:%s", className, username)
	hash := sha256.Sum256([]byte(data))
	// Return first 6 characters of hex hash (sufficient for uniqueness in classroom settings)
	return hex.EncodeToString(hash[:])[:6]
}

// deleteNamespaces deletes all namespaces associated with a class
func (r *ClassReconciler) deleteNamespaces(ctx context.Context, class *nercv1alpha1.Class) error {
	logger := log.FromContext(ctx)

	if len(class.Status.Namespaces) == 0 {
		logger.Info("No namespaces to delete", "class", class.Name)
		return nil
	}

	logger.Info("Deleting namespaces for class", "class", class.Name, "count", len(class.Status.Namespaces))

	for _, namespaceName := range class.Status.Namespaces {
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespaceName,
			},
		}

		if err := r.Delete(ctx, namespace); err != nil {
			if errors.IsNotFound(err) {
				logger.V(1).Info("Namespace already deleted", "namespace", namespaceName)
				continue
			}
			logger.Error(err, "Failed to delete namespace", "namespace", namespaceName)
			return err
		}
		logger.Info("Deleted namespace", "namespace", namespaceName)
	}

	return nil
}

// generateNamespaceName creates a valid namespace name from class code and semester
func generateNamespaceName(classCode, semester string) string {
	name := fmt.Sprintf("%s-%s", classCode, semester)
	return normalizeNamespaceName(name)
}

// normalizeNamespaceName converts a string to a valid Kubernetes namespace name
func normalizeNamespaceName(name string) string {
	// Convert to lowercase and replace invalid characters
	name = strings.ToLower(name)
	// Replace any character that's not alphanumeric or hyphen
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, name)
	// Remove consecutive hyphens
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	// Trim hyphens from start and end
	name = strings.Trim(name, "-")
	return name
}

// groupToClassRequests maps Group changes to Class reconcile requests
func (r *ClassReconciler) groupToClassRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	group, ok := obj.(*userv1.Group)
	if !ok {
		return nil
	}

	// Find all Class resources that reference this group
	classList := &nercv1alpha1.ClassList{}
	if err := r.List(ctx, classList); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list classes for group watch", "group", group.Name)
		return nil
	}

	var requests []reconcile.Request
	for _, class := range classList.Items {
		if class.Spec.StudentsGroup == group.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      class.Name,
					Namespace: class.Namespace,
				},
			})
		}
	}

	if len(requests) > 0 {
		log.FromContext(ctx).Info("Group change detected, triggering class reconciliation",
			"group", group.Name, "classes", len(requests))
	}

	return requests
}

func (r *ClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nercv1alpha1.Class{}).
		Watches(
			&userv1.Group{},
			handler.EnqueueRequestsFromMapFunc(r.groupToClassRequests),
		).
		Named("class-controller").
		Complete(r)
}
