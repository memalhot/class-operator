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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nercv1alpha1 "github.com/memalhot/class-operator/v1alpha1"
)

type ClassCullerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// UserCutoffInfo holds cutoff time and class name for a user
type UserCutoffInfo struct {
	ClassName string
	Cutoff    int
}

// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=classes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=classes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=classes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubeflow.org,resources=notebooks,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;delete
// +kubebuilder:rbac:groups=user.openshift.io,resources=groups,verbs=get;list;watch

// getGroupUsers retrieves users from an OpenShift group
func (r *ClassCullerReconciler) getGroupUsers(ctx context.Context, groupName string) ([]string, error) {
	logger := log.FromContext(ctx)

	if groupName == "" {
		return []string{}, nil
	}

	group := &userv1.Group{}
	if err := r.Get(ctx, types.NamespacedName{Name: groupName}, group); err != nil {
		return nil, fmt.Errorf("failed to get group %s: %w", groupName, err)
	}

	if len(group.Users) == 0 {
		logger.V(1).Info("Group is empty, this could lead to notebooks being deleted", "group", groupName)
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

// getNotebookRunningStartTime extracts the running container start time from notebook status
// Returns the time when the notebook container started running, or zero time if not running
func getNotebookRunningStartTime(notebook *unstructured.Unstructured) (time.Time, error) {
	// Navigate to .status.containerState.running.startedAt
	status, found, err := unstructured.NestedMap(notebook.Object, "status")
	if err != nil || !found {
		return time.Time{}, fmt.Errorf("status not found")
	}

	containerState, found, err := unstructured.NestedMap(status, "containerState")
	if err != nil || !found {
		return time.Time{}, fmt.Errorf("containerState not found")
	}

	running, found, err := unstructured.NestedMap(containerState, "running")
	if err != nil || !found {
		return time.Time{}, fmt.Errorf("notebook not running")
	}

	startedAtStr, found, err := unstructured.NestedString(running, "startedAt")
	if err != nil || !found {
		return time.Time{}, fmt.Errorf("startedAt not found")
	}

	startedAt, err := time.Parse(time.RFC3339, startedAtStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse startedAt: %w", err)
	}

	return startedAt, nil
}

// buildUserCutoffMap creates a map of username -> cutoff info from all classes in a namespace
// If a user is in multiple classes, the higher (more lenient) cutoff wins
func (r *ClassCullerReconciler) buildUserCutoffMap(
	ctx context.Context, namespace string,
) (map[string]UserCutoffInfo, error) {
	logger := log.FromContext(ctx)

	// List all classes
	classList := &nercv1alpha1.ClassList{}
	if err := r.List(ctx, classList); err != nil {
		return nil, fmt.Errorf("failed to list classes: %w", err)
	}

	userMap := make(map[string]UserCutoffInfo)

	// Find all classes that manage this namespace
	for _, class := range classList.Items {
		// Skip if culling not enabled
		if !class.Spec.NotebookCulling.Enabled {
			continue
		}

		// Skip if cutoff is invalid
		if class.Spec.NotebookCulling.Cutoff <= 0 {
			continue
		}

		// Check if this class manages the namespace
		if !slices.Contains(class.Status.Namespaces, namespace) {
			continue
		}

		// Get users from the students group
		users, err := r.getGroupUsers(ctx, class.Spec.StudentsGroup)
		if err != nil {
			logger.Error(err, "Failed to get group users", "class", class.Name, "group", class.Spec.StudentsGroup)
			continue
		}

		logger.Info("Loaded group for class", "class", class.Name, "group", class.Spec.StudentsGroup,
			"users", len(users), "cutoff", class.Spec.NotebookCulling.Cutoff)

		// Add users to map, taking max cutoff if user is in multiple classes
		for _, username := range users {
			username = strings.TrimSpace(username)
			if username == "" {
				continue
			}

			existing, exists := userMap[username]
			if exists {
				// User is in multiple classes, use the higher (more lenient) cutoff
				if class.Spec.NotebookCulling.Cutoff > existing.Cutoff {
					logger.Info("User in multiple classes, using more lenient cutoff",
						"user", username,
						"previousClass", existing.ClassName,
						"previousCutoff", existing.Cutoff,
						"newClass", class.Name,
						"newCutoff", class.Spec.NotebookCulling.Cutoff)
					userMap[username] = UserCutoffInfo{
						ClassName: class.Name,
						Cutoff:    class.Spec.NotebookCulling.Cutoff,
					}
				}
			} else {
				userMap[username] = UserCutoffInfo{
					ClassName: class.Name,
					Cutoff:    class.Spec.NotebookCulling.Cutoff,
				}
			}
		}
	}

	logger.Info("Built user cutoff map", "namespace", namespace, "totalUsers", len(userMap))
	return userMap, nil
}

// deleteNotebookAndPVC deletes a notebook and its associated PVC
func (r *ClassCullerReconciler) deleteNotebookAndPVC(ctx context.Context, notebookName, namespace string) error {
	logger := log.FromContext(ctx)

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

	logger.Info("Deleted notebook", "notebook", notebookName, "namespace", namespace)

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
		logger.V(1).Info("Could not delete PVC (may not exist)", "pvc", pvcName, "namespace", namespace, "error", err)
	} else {
		logger.Info("Deleted PVC", "pvc", pvcName, "namespace", namespace)
	}

	return nil
}

// cullNotebooksMultiNamespace handles notebook culling for multi-namespace classes
// It lists all notebooks across all namespaces and filters by namespace prefix
func (r *ClassCullerReconciler) cullNotebooksMultiNamespace(
	ctx context.Context, class *nercv1alpha1.Class, now time.Time,
) error {
	logger := log.FromContext(ctx)

	prefix := strings.TrimSpace(class.Spec.Deployment.StudentNamespacePrefix)
	if prefix == "" {
		logger.V(1).Info("No namespace prefix configured for multi-namespace class", "class", class.Name)
		return nil
	}

	cutoff := class.Spec.NotebookCulling.Cutoff

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

	// Get users from the students group for this class
	users, err := r.getGroupUsers(ctx, class.Spec.StudentsGroup)
	if err != nil {
		logger.Error(err, "Failed to get group users", "class", class.Name, "group", class.Spec.StudentsGroup)
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

	logger.Info("Processing multi-namespace class", "class", class.Name, "prefix", prefix,
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
			logger.V(1).Info("Notebook missing username annotation, skipping", "notebook", notebookName, "namespace", namespace)
			continue
		}

		// Check if user is in the class group
		if !userSet[username] {
			// User not in group - delete notebook and PVC
			logger.Info("Deleting notebook for user not in class group",
				"notebook", notebookName,
				"namespace", namespace,
				"class", class.Name,
				"user", username)

			if err := r.deleteNotebookAndPVC(ctx, notebookName, namespace); err != nil {
				logger.Error(err, "Failed to delete notebook and PVC", "notebook", notebookName, "namespace", namespace)
			}
			continue
		}

		// User is in the class group - check cutoff based on running time
		startedAt, err := getNotebookRunningStartTime(&notebook)
		if err != nil {
			// Notebook is not running or status unavailable, skip culling
			logger.V(1).Info("Notebook not running or status unavailable, skipping",
				"notebook", notebookName,
				"namespace", namespace,
				"error", err.Error())
			continue
		}

		cutoffDuration := time.Duration(cutoff) * time.Second
		runningSeconds := now.Sub(startedAt).Seconds()
		cutoffSeconds := cutoffDuration.Seconds()

		// Check if notebook has exceeded the cutoff time since it started running
		if runningSeconds > cutoffSeconds {
			logger.Info("Stopping notebook that exceeded cutoff time",
				"notebook", notebookName,
				"namespace", namespace,
				"class", class.Name,
				"user", username,
				"runningTime", fmt.Sprintf("%.0fs", runningSeconds),
				"cutoff", fmt.Sprintf("%.0fs", cutoffSeconds))

			// Add annotation to stop the notebook
			nowUTC := now.UTC().Format("2006-01-02T15:04:05Z")
			if annotations == nil {
				annotations = make(map[string]string)
			}
			annotations["kubeflow-resource-stopped"] = nowUTC
			notebook.SetAnnotations(annotations)

			if err := r.Update(ctx, &notebook); err != nil {
				logger.Error(err, "Failed to stop notebook", "notebook", notebookName, "namespace", namespace)
				continue
			}

			logger.Info("Successfully stopped notebook", "notebook", notebookName, "namespace", namespace)
		} else {
			logger.V(1).Info("Notebook within cutoff time",
				"notebook", notebookName,
				"namespace", namespace,
				"user", username,
				"runningTime", fmt.Sprintf("%.0fs", runningSeconds),
				"cutoff", fmt.Sprintf("%.0fs", cutoffSeconds))
		}
	}

	logger.Info("Matched notebooks in multi-namespace class", "class", class.Name, "matched", matched)
	return nil
}

func (r *ClassCullerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var class nercv1alpha1.Class
	if err := r.Get(ctx, req.NamespacedName, &class); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Skip if notebook culling is not enabled
	if !class.Spec.NotebookCulling.Enabled {
		logger.V(1).Info("Notebook culling not enabled for class", "class", class.Name)
		return ctrl.Result{}, nil
	}

	if class.Spec.NotebookCulling.Cutoff <= 0 {
		logger.V(1).Info("Invalid cutoff time for class", "class", class.Name)
		return ctrl.Result{}, nil
	}

	now := time.Now()

	// Handle multi-namespace classes differently
	if class.Spec.Deployment.MultiNamespace {
		if err := r.cullNotebooksMultiNamespace(ctx, &class, now); err != nil {
			logger.Error(err, "Failed to cull notebooks in multi-namespace class", "class", class.Name)
			return ctrl.Result{RequeueAfter: time.Minute * 5}, err
		}
	} else {
		// Single namespace mode - process specific namespaces from status
		namespaces := class.Status.Namespaces
		if len(namespaces) == 0 {
			logger.V(1).Info("No namespaces found for class", "class", class.Name)
			return ctrl.Result{}, nil
		}

		for _, namespace := range namespaces {
			if err := r.cullNotebooksInNamespace(ctx, namespace, now); err != nil {
				logger.Error(err, "Failed to cull notebooks in namespace", "namespace", namespace)
				// Continue with other namespaces even if one fails
				continue
			}
		}
	}

	// Requeue after a reasonable interval to check again
	return ctrl.Result{RequeueAfter: time.Minute * 30}, nil
}

func (r *ClassCullerReconciler) cullNotebooksInNamespace(ctx context.Context, namespace string, now time.Time) error {
	logger := log.FromContext(ctx)

	// Build user-to-cutoff map for all classes in this namespace
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
			logger.V(1).Info("Notebook missing username annotation, skipping", "notebook", notebookName, "namespace", namespace)
			continue
		}

		// Check if user is in any class group
		userInfo, userExists := userMap[username]
		if !userExists {
			// User not in any group - delete notebook and PVC
			logger.Info("Deleting notebook for user not in any class group",
				"notebook", notebookName,
				"namespace", namespace,
				"user", username)

			if err := r.deleteNotebookAndPVC(ctx, notebookName, namespace); err != nil {
				logger.Error(err, "Failed to delete notebook and PVC", "notebook", notebookName, "namespace", namespace)
			}
			continue
		}

		// User is in a class group - use their specific cutoff based on running time
		startedAt, err := getNotebookRunningStartTime(&notebook)
		if err != nil {
			// Notebook is not running or status unavailable, skip culling
			logger.V(1).Info("Notebook not running or status unavailable, skipping",
				"notebook", notebookName,
				"namespace", namespace,
				"error", err.Error())
			continue
		}

		userCutoffDuration := time.Duration(userInfo.Cutoff) * time.Second
		runningSeconds := now.Sub(startedAt).Seconds()
		cutoffSeconds := userCutoffDuration.Seconds()

		// Check if notebook has exceeded the user's cutoff time since it started running
		if runningSeconds > cutoffSeconds {
			logger.Info("Stopping notebook that exceeded user's cutoff time",
				"notebook", notebookName,
				"namespace", namespace,
				"user", username,
				"class", userInfo.ClassName,
				"runningTime", fmt.Sprintf("%.0fs", runningSeconds),
				"cutoff", fmt.Sprintf("%.0fs", cutoffSeconds))

			// Add annotation to stop the notebook
			nowUTC := now.UTC().Format("2006-01-02T15:04:05Z")
			if annotations == nil {
				annotations = make(map[string]string)
			}
			annotations["kubeflow-resource-stopped"] = nowUTC
			notebook.SetAnnotations(annotations)

			if err := r.Update(ctx, &notebook); err != nil {
				logger.Error(err, "Failed to stop notebook", "notebook", notebookName, "namespace", namespace)
				continue
			}

			logger.Info("Successfully stopped notebook", "notebook", notebookName, "namespace", namespace)
		} else {
			logger.V(1).Info("Notebook within cutoff time",
				"notebook", notebookName,
				"namespace", namespace,
				"user", username,
				"runningTime", fmt.Sprintf("%.0fs", runningSeconds),
				"cutoff", fmt.Sprintf("%.0fs", cutoffSeconds))
		}
	}

	return nil
}

// notebookToClassRequests maps Notebook changes to Class reconcile requests
func (r *ClassCullerReconciler) notebookToClassRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	notebook, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}

	namespace := notebook.GetNamespace()

	// Find all Class resources that manage this namespace
	classList := &nercv1alpha1.ClassList{}
	if err := r.List(ctx, classList); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list classes for notebook watch", "notebook", notebook.GetName())
		return nil
	}

	var requests []reconcile.Request
	for _, class := range classList.Items {
		// Check if this class has culling enabled
		if !class.Spec.NotebookCulling.Enabled {
			continue
		}

		// Check if this class manages the namespace
		if slices.Contains(class.Status.Namespaces, namespace) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      class.Name,
					Namespace: class.Namespace,
				},
			})
		}
	}

	if len(requests) > 0 {
		log.FromContext(ctx).Info("Notebook change detected, triggering class reconciliation",
			"notebook", notebook.GetName(), "namespace", namespace, "classes", len(requests))
	}

	return requests
}

func (r *ClassCullerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create unstructured type for Notebook
	notebook := &unstructured.Unstructured{}
	notebook.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&nercv1alpha1.Class{}).
		Watches(
			notebook,
			handler.EnqueueRequestsFromMapFunc(r.notebookToClassRequests),
		).
		Named("culler-controller").
		Complete(r)
}
