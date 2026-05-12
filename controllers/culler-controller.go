package controllers

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	userv1 "github.com/openshift/api/user/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	nercv1alpha1 "github.com/memalhot/class-operator/v1alpha1"
)

type CourseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// UserCutoffInfo holds cutoff time and class name for a user
type UserCutoffInfo struct {
	ClassName string
	Cutoff    int
}

// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=courses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=courses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=courses/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubeflow.org,resources=notebooks,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;delete
// +kubebuilder:rbac:groups=user.openshift.io,resources=groups,verbs=get;list;watch

// getGroupUsers retrieves users from an OpenShift group
func (r *CourseReconciler) getGroupUsers(ctx context.Context, groupName string) ([]string, error) {
	log := log.FromContext(ctx)

	if groupName == "" {
		return []string{}, nil
	}

	group := &userv1.Group{}
	if err := r.Get(ctx, types.NamespacedName{Name: groupName}, group); err != nil {
		return nil, fmt.Errorf("failed to get group %s: %w", groupName, err)
	}

	if len(group.Users) == 0 {
		log.V(1).Info("Group is empty, this could lead to notebooks being deleted", "group", groupName)
	}

	return group.Users, nil
}

// getNotebookUsername extracts the username from notebook annotations
func getNotebookUsername(notebook *unstructured.Unstructured) string {
	annotations := notebook.GetAnnotations()
	if annotations == nil {
		return ""
	}

	// Check both possible annotation keys
	for _, key := range []string{"opendatahub.io/username", "notebooks.opendatahub.io/username"} {
		if username, exists := annotations[key]; exists {
			return strings.TrimSpace(username)
		}
	}

	return ""
}

// buildUserCutoffMap creates a map of username -> cutoff info from all courses in a namespace
// If a user is in multiple courses, the higher (more lenient) cutoff wins
func (r *CourseReconciler) buildUserCutoffMap(ctx context.Context, namespace string) (map[string]UserCutoffInfo, error) {
	log := log.FromContext(ctx)

	// List all courses
	courseList := &nercv1alpha1.CourseList{}
	if err := r.List(ctx, courseList); err != nil {
		return nil, fmt.Errorf("failed to list courses: %w", err)
	}

	userMap := make(map[string]UserCutoffInfo)

	// Find all courses that manage this namespace
	for _, course := range courseList.Items {
		// Skip if culling not enabled
		if !course.Spec.NotebookCulling.Enabled {
			continue
		}

		// Skip if cutoff is invalid
		if course.Spec.NotebookCulling.Cutoff <= 0 {
			continue
		}

		// Check if this course manages the namespace
		if !slices.Contains(course.Status.Namespaces, namespace) {
			continue
		}

		// Get users from the students group
		users, err := r.getGroupUsers(ctx, course.Spec.StudentsGroup)
		if err != nil {
			log.Error(err, "Failed to get group users", "course", course.Name, "group", course.Spec.StudentsGroup)
			continue
		}

		log.Info("Loaded group for course", "course", course.Name, "group", course.Spec.StudentsGroup,
			"users", len(users), "cutoff", course.Spec.NotebookCulling.Cutoff)

		// Add users to map, taking max cutoff if user is in multiple courses
		for _, username := range users {
			username = strings.TrimSpace(username)
			if username == "" {
				continue
			}

			existing, exists := userMap[username]
			if exists {
				// User is in multiple courses, use the higher (more lenient) cutoff
				if course.Spec.NotebookCulling.Cutoff > existing.Cutoff {
					log.Info("User in multiple courses, using more lenient cutoff",
						"user", username,
						"previousCourse", existing.ClassName,
						"previousCutoff", existing.Cutoff,
						"newCourse", course.Name,
						"newCutoff", course.Spec.NotebookCulling.Cutoff)
					userMap[username] = UserCutoffInfo{
						ClassName: course.Name,
						Cutoff:    course.Spec.NotebookCulling.Cutoff,
					}
				}
			} else {
				userMap[username] = UserCutoffInfo{
					ClassName: course.Name,
					Cutoff:    course.Spec.NotebookCulling.Cutoff,
				}
			}
		}
	}

	log.Info("Built user cutoff map", "namespace", namespace, "totalUsers", len(userMap))
	return userMap, nil
}

// deleteNotebookAndPVC deletes a notebook and its associated PVC
func (r *CourseReconciler) deleteNotebookAndPVC(ctx context.Context, notebookName, namespace string) error {
	log := log.FromContext(ctx)

	notebook := &unstructured.Unstructured{}
	notebook.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})
	notebook.SetName(notebookName)
	notebook.SetNamespace(namespace)

	// Delete notebook
	if err := r.Delete(ctx, notebook); err != nil {
		return fmt.Errorf("failed to delete notebook %s: %w", notebookName, err)
	}

	log.Info("Deleted notebook", "notebook", notebookName, "namespace", namespace)

	// Calculate PVC name (based on JupyterHub naming convention)
	pvcName := fmt.Sprintf("jupyterhub-nb-%s-pvc", strings.TrimPrefix(notebookName, "jupyter-nb-"))

	// Delete PVC
	pvc := &unstructured.Unstructured{}
	pvc.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "PersistentVolumeClaim",
	})
	pvc.SetName(pvcName)
	pvc.SetNamespace(namespace)

	if err := r.Delete(ctx, pvc); err != nil {
		// Log error but don't fail if PVC doesn't exist
		log.V(1).Info("Could not delete PVC (may not exist)", "pvc", pvcName, "namespace", namespace, "error", err)
	} else {
		log.Info("Deleted PVC", "pvc", pvcName, "namespace", namespace)
	}

	return nil
}

// cullNotebooksMultiNamespace handles notebook culling for multi-namespace courses
// It lists all notebooks across all namespaces and filters by namespace prefix
func (r *CourseReconciler) cullNotebooksMultiNamespace(ctx context.Context, course *nercv1alpha1.Course, now time.Time) error {
	log := log.FromContext(ctx)

	prefix := strings.TrimSpace(course.Spec.Deployment.StudentNamespacePrefix)
	if prefix == "" {
		log.V(1).Info("No namespace prefix configured for multi-namespace course", "course", course.Name)
		return nil
	}

	cutoff := course.Spec.NotebookCulling.Cutoff

	// List all notebooks across all namespaces
	notebookList := &unstructured.UnstructuredList{}
	notebookList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "NotebookList",
	})

	if err := r.List(ctx, notebookList); err != nil {
		return fmt.Errorf("failed to list notebooks across all namespaces: %w", err)
	}

	// Get users from the students group for this course
	users, err := r.getGroupUsers(ctx, course.Spec.StudentsGroup)
	if err != nil {
		log.Error(err, "Failed to get group users", "course", course.Name, "group", course.Spec.StudentsGroup)
		users = []string{} // Continue without user validation
	}

	// Build user set for quick lookup
	userSet := make(map[string]bool)
	for _, user := range users {
		user = strings.TrimSpace(user)
		if user != "" {
			userSet[user] = true
		}
	}

	log.Info("Processing multi-namespace course", "course", course.Name, "prefix", prefix,
		"cutoff", cutoff, "authorizedUsers", len(userSet))

	matched := 0
	for _, notebook := range notebookList.Items {
		namespace := notebook.GetNamespace()

		// Check if namespace matches prefix
		if namespace != prefix && !strings.HasPrefix(namespace, prefix+"-") {
			continue
		}

		matched++
		notebookName := notebook.GetName()

		// Skip notebooks that are already stopped
		annotations := notebook.GetAnnotations()
		if annotations != nil {
			if _, exists := annotations["kubeflow-resource-stopped"]; exists {
				continue
			}
		}

		// Get username from notebook annotations
		username := getNotebookUsername(&notebook)
		if username == "" {
			log.V(1).Info("Notebook missing username annotation, skipping", "notebook", notebookName, "namespace", namespace)
			continue
		}

		// Check if user is in the course group
		if !userSet[username] {
			// User not in group - delete notebook and PVC
			log.Info("Deleting notebook for user not in course group",
				"notebook", notebookName,
				"namespace", namespace,
				"course", course.Name,
				"user", username)

			if err := r.deleteNotebookAndPVC(ctx, notebookName, namespace); err != nil {
				log.Error(err, "Failed to delete notebook and PVC", "notebook", notebookName, "namespace", namespace)
			}
			continue
		}

		// User is in the course group - check cutoff
		cutoffDuration := time.Duration(cutoff) * time.Second
		creationTime := notebook.GetCreationTimestamp().Time
		ageSeconds := now.Sub(creationTime).Seconds()
		cutoffSeconds := cutoffDuration.Seconds()

		// Check if notebook has exceeded the cutoff time
		if ageSeconds > cutoffSeconds {
			log.Info("Stopping notebook that exceeded cutoff time",
				"notebook", notebookName,
				"namespace", namespace,
				"course", course.Name,
				"user", username,
				"age", fmt.Sprintf("%.0fs", ageSeconds),
				"cutoff", fmt.Sprintf("%.0fs", cutoffSeconds))

			// Add annotation to stop the notebook
			nowUTC := now.UTC().Format("2006-01-02T15:04:05Z")
			if annotations == nil {
				annotations = make(map[string]string)
			}
			annotations["kubeflow-resource-stopped"] = nowUTC
			notebook.SetAnnotations(annotations)

			if err := r.Update(ctx, &notebook); err != nil {
				log.Error(err, "Failed to stop notebook", "notebook", notebookName, "namespace", namespace)
				continue
			}

			log.Info("Successfully stopped notebook", "notebook", notebookName, "namespace", namespace)
		} else {
			log.V(1).Info("Notebook within cutoff time",
				"notebook", notebookName,
				"namespace", namespace,
				"user", username,
				"age", fmt.Sprintf("%.0fs", ageSeconds),
				"cutoff", fmt.Sprintf("%.0fs", cutoffSeconds))
		}
	}

	log.Info("Matched notebooks in multi-namespace course", "course", course.Name, "matched", matched)
	return nil
}

func (r *CourseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var course nercv1alpha1.Course
	if err := r.Get(ctx, req.NamespacedName, &course); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Skip if notebook culling is not enabled
	if !course.Spec.NotebookCulling.Enabled {
		log.V(1).Info("Notebook culling not enabled for course", "course", course.Name)
		return ctrl.Result{}, nil
	}

	if course.Spec.NotebookCulling.Cutoff <= 0 {
		log.V(1).Info("Invalid cutoff time for course", "course", course.Name)
		return ctrl.Result{}, nil
	}

	now := time.Now()

	// Handle multi-namespace courses differently
	if course.Spec.Deployment.MultiNamespace {
		if err := r.cullNotebooksMultiNamespace(ctx, &course, now); err != nil {
			log.Error(err, "Failed to cull notebooks in multi-namespace course", "course", course.Name)
			return ctrl.Result{RequeueAfter: time.Minute * 5}, err
		}
	} else {
		// Single namespace mode - process specific namespaces from status
		namespaces := course.Status.Namespaces
		if len(namespaces) == 0 {
			log.V(1).Info("No namespaces found for course", "course", course.Name)
			return ctrl.Result{}, nil
		}

		for _, namespace := range namespaces {
			if err := r.cullNotebooksInNamespace(ctx, namespace, now); err != nil {
				log.Error(err, "Failed to cull notebooks in namespace", "namespace", namespace)
				// Continue with other namespaces even if one fails
				continue
			}
		}
	}

	// Requeue after a reasonable interval to check again
	return ctrl.Result{RequeueAfter: time.Minute * 5}, nil
}

func (r *CourseReconciler) cullNotebooksInNamespace(ctx context.Context, namespace string, now time.Time) error {
	log := log.FromContext(ctx)

	// Build user-to-cutoff map for all courses in this namespace
	userMap, err := r.buildUserCutoffMap(ctx, namespace)
	if err != nil {
		return fmt.Errorf("failed to build user cutoff map: %w", err)
	}

	notebookList := &unstructured.UnstructuredList{}
	notebookList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "NotebookList",
	})

	if err := r.List(ctx, notebookList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list notebooks in namespace %s: %w", namespace, err)
	}

	for _, notebook := range notebookList.Items {
		notebookName := notebook.GetName()

		// Skip notebooks that are already stopped
		annotations := notebook.GetAnnotations()
		if annotations != nil {
			if _, exists := annotations["kubeflow-resource-stopped"]; exists {
				continue
			}
		}

		// Get username from notebook annotations
		username := getNotebookUsername(&notebook)
		if username == "" {
			log.V(1).Info("Notebook missing username annotation, skipping", "notebook", notebookName, "namespace", namespace)
			continue
		}

		// Check if user is in any course group
		userInfo, userExists := userMap[username]
		if !userExists {
			// User not in any group - delete notebook and PVC
			log.Info("Deleting notebook for user not in any course group",
				"notebook", notebookName,
				"namespace", namespace,
				"user", username)

			if err := r.deleteNotebookAndPVC(ctx, notebookName, namespace); err != nil {
				log.Error(err, "Failed to delete notebook and PVC", "notebook", notebookName, "namespace", namespace)
			}
			continue
		}

		// User is in a course group - use their specific cutoff
		userCutoffDuration := time.Duration(userInfo.Cutoff) * time.Second
		creationTime := notebook.GetCreationTimestamp().Time
		ageSeconds := now.Sub(creationTime).Seconds()
		cutoffSeconds := userCutoffDuration.Seconds()

		// Check if notebook has exceeded the user's cutoff time
		if ageSeconds > cutoffSeconds {
			log.Info("Stopping notebook that exceeded user's cutoff time",
				"notebook", notebookName,
				"namespace", namespace,
				"user", username,
				"course", userInfo.ClassName,
				"age", fmt.Sprintf("%.0fs", ageSeconds),
				"cutoff", fmt.Sprintf("%.0fs", cutoffSeconds))

			// Add annotation to stop the notebook
			nowUTC := now.UTC().Format("2006-01-02T15:04:05Z")
			if annotations == nil {
				annotations = make(map[string]string)
			}
			annotations["kubeflow-resource-stopped"] = nowUTC
			notebook.SetAnnotations(annotations)

			if err := r.Update(ctx, &notebook); err != nil {
				log.Error(err, "Failed to stop notebook", "notebook", notebookName, "namespace", namespace)
				continue
			}

			log.Info("Successfully stopped notebook", "notebook", notebookName, "namespace", namespace)
		} else {
			log.V(1).Info("Notebook within cutoff time",
				"notebook", notebookName,
				"namespace", namespace,
				"user", username,
				"age", fmt.Sprintf("%.0fs", ageSeconds),
				"cutoff", fmt.Sprintf("%.0fs", cutoffSeconds))
		}
	}

	return nil
}

func (r *CourseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nercv1alpha1.Course{}).
		Named("culler-controller").
		Complete(r)
}
