package controllers

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	nercv1alpha1 "github.com/memalhot/class-op.git/v1alpha1"
)

type CourseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=courses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=courses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nerc.mghpcc.org,resources=courses/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubeflow.org,resources=notebooks,verbs=get;list;watch;update;patch

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

	// Get the namespaces associated with this course
	namespaces := course.Status.Namespaces
	if len(namespaces) == 0 {
		log.V(1).Info("No namespaces found for course", "course", course.Name)
		return ctrl.Result{}, nil
	}

	cutoffDuration := time.Duration(course.Spec.NotebookCulling.Cutoff) * time.Second
	now := time.Now()

	for _, namespace := range namespaces {
		if err := r.cullNotebooksInNamespace(ctx, namespace, cutoffDuration, now); err != nil {
			log.Error(err, "Failed to cull notebooks in namespace", "namespace", namespace)
			// Continue with other namespaces even if one fails
			continue
		}
	}

	// Requeue after a reasonable interval to check again
	return ctrl.Result{RequeueAfter: time.Minute * 5}, nil
}

func (r *CourseReconciler) cullNotebooksInNamespace(ctx context.Context, namespace string, cutoffDuration time.Duration, now time.Time) error {
	log := log.FromContext(ctx)

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
		// Skip notebooks that are already stopped
		annotations := notebook.GetAnnotations()
		if annotations != nil {
			if _, exists := annotations["kubeflow-resource-stopped"]; exists {
				continue
			}
		}

		// Calculate how long the notebook has been running
		creationTime := notebook.GetCreationTimestamp().Time
		ageSeconds := now.Sub(creationTime).Seconds()
		cutoffSeconds := cutoffDuration.Seconds()

		// Check if notebook has exceeded the cutoff time
		if ageSeconds > cutoffSeconds {
			log.Info("Patching notebook that exceeded cutoff time",
				"notebook", notebook.GetName(),
				"namespace", namespace,
				"age", fmt.Sprintf("%.0fs", ageSeconds),
				"cutoff", fmt.Sprintf("%.0fs", cutoffSeconds))

			// Add annotation to stop the notebook
			// nowUTC := now.UTC().Format("2006-01-02T15:04:05Z")
			// if annotations == nil {
			// 	annotations = make(map[string]string)
			// }
			// annotations["kubeflow-resource-stopped"] = nowUTC
			// notebook.SetAnnotations(annotations)

			// if err := r.Update(ctx, &notebook); err != nil {
			// 	log.Error(err, "Failed to patch notebook", "notebook", notebook.GetName(), "namespace", namespace)
			// 	continue
			// }

			log.Info("Successfully patched notebook", "notebook", notebook.GetName(), "namespace", namespace)
		}
	}

	return nil
}

func (r *CourseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nercv1alpha1.Course{}).
		Complete(r)
}
