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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	nercv1alpha1 "github.com/memalhot/class-operator/v1alpha1"
)

type ClassReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=courses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=courses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=courses/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=user.openshift.io,resources=groups,verbs=get;list;watch

const courseFinalizer = "nerc.mghpcc.org/course-finalizer"

func (r *ClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var course nercv1alpha1.Course
	if err := r.Get(ctx, req.NamespacedName, &course); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !course.ObjectMeta.DeletionTimestamp.IsZero() {
		if slices.Contains(course.ObjectMeta.Finalizers, courseFinalizer) {
			// Course is being deleted, clean up namespaces
			if err := r.deleteNamespaces(ctx, &course); err != nil {
				log.Error(err, "Failed to delete namespaces", "course", course.Name)
				return ctrl.Result{}, err
			}

			// Remove finalizer
			course.ObjectMeta.Finalizers = removeString(course.ObjectMeta.Finalizers, courseFinalizer)
			if err := r.Update(ctx, &course); err != nil {
				log.Error(err, "Failed to remove finalizer", "course", course.Name)
				return ctrl.Result{}, err
			}
			log.Info("Successfully cleaned up course namespaces", "course", course.Name)
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !slices.Contains(course.ObjectMeta.Finalizers, courseFinalizer) {
		course.ObjectMeta.Finalizers = append(course.ObjectMeta.Finalizers, courseFinalizer)
		if err := r.Update(ctx, &course); err != nil {
			log.Error(err, "Failed to add finalizer", "course", course.Name)
			return ctrl.Result{}, err
		}
		log.Info("Added finalizer to course", "course", course.Name)
	}

	var createdNamespaces []string

	if course.Spec.Deployment.MultiNamespace {
		// Multi-namespace mode: create namespace per user
		log.Info("Processing multi-namespace course", "course", course.Name)
		namespaces, err := r.createMultiNamespaces(ctx, &course)
		if err != nil {
			log.Error(err, "Failed to create multi-namespaces", "course", course.Name)
			return ctrl.Result{}, err
		}
		createdNamespaces = namespaces
	} else {
		// Single-namespace mode: create one namespace
		namespaceName := generateNamespaceName(course.Spec.CourseCode, course.Spec.Semester)
		log.Info("Processing single-namespace course", "course", course.Name, "namespace", namespaceName)

		if err := r.ensureNamespace(ctx, &course, namespaceName); err != nil {
			return ctrl.Result{}, err
		}
		createdNamespaces = []string{namespaceName}
	}

	// Update status with all created namespaces
	if !slices.Equal(course.Status.Namespaces, createdNamespaces) {
		course.Status.Namespaces = createdNamespaces
		if err := r.Status().Update(ctx, &course); err != nil {
			log.Error(err, "Failed to update course status", "course", course.Name)
			return ctrl.Result{}, err
		}
		log.Info("Updated course status with namespaces", "course", course.Name, "count", len(createdNamespaces))
	}

	return ctrl.Result{}, nil
}

// ensureNamespace creates a namespace if it doesn't exist
func (r *ClassReconciler) ensureNamespace(ctx context.Context, course *nercv1alpha1.Course, namespaceName string) error {
	log := log.FromContext(ctx)

	namespace := &corev1.Namespace{}
	err := r.Get(ctx, client.ObjectKey{Name: namespaceName}, namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create the namespace
			log.Info("Creating namespace for course", "course", course.Name, "namespace", namespaceName)

			namespace = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespaceName,
					Labels: map[string]string{
						"nerc.mghpcc.org/course":      course.Name,
						"nerc.mghpcc.org/course-code": course.Spec.CourseCode,
						"nerc.mghpcc.org/semester":    course.Spec.Semester,
					},
				},
			}

			if err := r.Create(ctx, namespace); err != nil {
				log.Error(err, "Failed to create namespace", "namespace", namespaceName)
				return err
			}

			log.Info("Successfully created namespace", "namespace", namespaceName)
		} else {
			log.Error(err, "Failed to get namespace", "namespace", namespaceName)
			return err
		}
	} else {
		log.V(1).Info("Namespace already exists", "namespace", namespaceName)
	}

	return nil
}

// createMultiNamespaces creates a namespace for each user in the students group
func (r *ClassReconciler) createMultiNamespaces(ctx context.Context, course *nercv1alpha1.Course) ([]string, error) {
	log := log.FromContext(ctx)

	// Get users from the students group
	users, err := r.getGroupUsers(ctx, course.Spec.StudentsGroup)
	if err != nil {
		log.Error(err, "Failed to get group users", "course", course.Name, "group", course.Spec.StudentsGroup)
		return nil, err
	}

	if len(users) == 0 {
		log.Info("No users found in group", "course", course.Name, "group", course.Spec.StudentsGroup)
		return []string{}, nil
	}

	log.Info("Creating namespaces for users", "course", course.Name, "userCount", len(users))

	var createdNamespaces []string
	template := course.Spec.Deployment.NamespaceTemplate
	if template == "" {
		// Default template if not specified
		template = fmt.Sprintf("%s-{{.Username}}", course.Spec.CourseCode)
	}

	for _, username := range users {
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}

		// Apply template to generate namespace name
		namespaceName := applyNamespaceTemplate(template, username)

		// Add hash suffix for uniqueness (in case of duplicate usernames)
		hash := generateUserHash(course.Name, username)
		namespaceName = fmt.Sprintf("%s-%s", namespaceName, hash)
		namespaceName = normalizeNamespaceName(namespaceName)

		// Create namespace for this user
		if err := r.ensureNamespace(ctx, course, namespaceName); err != nil {
			log.Error(err, "Failed to create namespace for user", "user", username, "namespace", namespaceName)
			continue
		}

		createdNamespaces = append(createdNamespaces, namespaceName)
	}

	log.Info("Completed namespace creation", "course", course.Name, "created", len(createdNamespaces))
	return createdNamespaces, nil
}

// getGroupUsers retrieves users from an OpenShift group
func (r *ClassReconciler) getGroupUsers(ctx context.Context, groupName string) ([]string, error) {
	log := log.FromContext(ctx)

	if groupName == "" {
		return []string{}, nil
	}

	group := &userv1.Group{}
	if err := r.Get(ctx, types.NamespacedName{Name: groupName}, group); err != nil {
		return nil, fmt.Errorf("failed to get group %s: %w", groupName, err)
	}

	if len(group.Users) == 0 {
		log.V(1).Info("Group is empty", "group", groupName)
	}

	return group.Users, nil
}

// applyNamespaceTemplate replaces {{.Username}} with the actual username
func applyNamespaceTemplate(template, username string) string {
	return strings.ReplaceAll(template, "{{.Username}}", username)
}

// generateUserHash creates a deterministic short hash from course name and username
// This ensures uniqueness even if multiple students have the same username
func generateUserHash(courseName, username string) string {
	// Combine course name and username to ensure uniqueness per course
	data := fmt.Sprintf("%s:%s", courseName, username)
	hash := sha256.Sum256([]byte(data))
	// Return first 6 characters of hex hash (sufficient for uniqueness in classroom settings)
	return hex.EncodeToString(hash[:])[:6]
}

// deleteNamespaces deletes all namespaces associated with a course
func (r *ClassReconciler) deleteNamespaces(ctx context.Context, course *nercv1alpha1.Course) error {
	log := log.FromContext(ctx)

	if len(course.Status.Namespaces) == 0 {
		log.Info("No namespaces to delete", "course", course.Name)
		return nil
	}

	log.Info("Deleting namespaces for course", "course", course.Name, "count", len(course.Status.Namespaces))

	for _, namespaceName := range course.Status.Namespaces {
		namespace := &corev1.Namespace{}
		namespace.Name = namespaceName

		if err := r.Delete(ctx, namespace); err != nil {
			if errors.IsNotFound(err) {
				log.V(1).Info("Namespace already deleted", "namespace", namespaceName)
				continue
			}
			log.Error(err, "Failed to delete namespace", "namespace", namespaceName)
			return err
		}
		log.Info("Deleted namespace", "namespace", namespaceName)
	}

	return nil
}

// removeString removes a string from a slice
func removeString(slice []string, s string) []string {
	result := []string{}
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}

// generateNamespaceName creates a valid namespace name from course code and semester
func generateNamespaceName(courseCode, semester string) string {
	name := fmt.Sprintf("%s-%s", courseCode, semester)
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

func (r *ClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nercv1alpha1.Course{}).
		Named("class-controller").
		Complete(r)
}
