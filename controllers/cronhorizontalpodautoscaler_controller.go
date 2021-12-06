/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	autoscalingv1 "example.com/cronhpa/api/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	log "k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// newReconciler returns a new reconcile.Reconciler
func NewReconciler(mgr manager.Manager) *CronHorizontalPodAutoscalerReconciler {
	var stopChan chan struct{}
	//crdmap debug server used
	crdMap := make(map[string]autoscalingv1.CronHPAContent)
	cm := NewCronManager(mgr.GetConfig(), mgr.GetClient(), mgr.GetEventRecorderFor("CronHorizontalPodAutoscaler"))
	r := &CronHorizontalPodAutoscalerReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), CronManager: cm, CrdMap: crdMap}
	go func(cronManager *CronManager, stopChan chan struct{}) {
		cm.Run(stopChan)
		<-stopChan
	}(cm, stopChan)

	go func(cronManager *CronManager, stopChan chan struct{}, crdmap map[string]autoscalingv1.CronHPAContent) {
		server := NewWebServer(mgr.GetClient(), cronManager, crdmap)
		server.serve()
	}(cm, stopChan, crdMap)
	return r
}

var _ reconcile.Reconciler = &CronHorizontalPodAutoscalerReconciler{}

// CronHorizontalPodAutoscalerReconciler reconciles a CronHorizontalPodAutoscaler object
type CronHorizontalPodAutoscalerReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	CronManager *CronManager
	CrdMap      map[string]autoscalingv1.CronHPAContent
}

//+kubebuilder:rbac:groups=autoscaling.example.com,resources=cronhorizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autoscaling.example.com,resources=cronhorizontalpodautoscalers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=autoscaling.example.com,resources=cronhorizontalpodautoscalers/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the CronHorizontalPodAutoscaler object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.10.0/pkg/reconcile
func (r *CronHorizontalPodAutoscalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = klog.FromContext(ctx)

	// Fetch the CronHorizontalPodAutoscaler instance
	log.Infof("Start to handle cronHPA %s in %s namespace", req.Name, req.Namespace)
	//delete bebug server crd
	crdkey := fmt.Sprintf("%s%s", req.Namespace, req.Name)
	if _, ok := r.CrdMap[crdkey]; ok {
		delete(r.CrdMap, crdkey)
	}
	instance := &autoscalingv1.CronHorizontalPodAutoscaler{}
	err := r.Get(ctx, req.NamespacedName, instance)
	fmt.Println("================")
	fmt.Println(instance.Spec)
	fmt.Println(instance.Status)
	fmt.Println("================")
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			fmt.Println("start gc")
			go r.CronManager.GC()
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	} else {
		//write crd resource to map
		r.CrdMap[crdkey] = autoscalingv1.CronHPAContent{
			Namespace: instance.Namespace,
			Name:      instance.Name,
			Spec:      instance.Spec,
		}
	}
	//log.Infof("%v is handled by cron-hpa controller", instance.Name)
	conditions := instance.Status.Conditions

	leftConditions := make([]autoscalingv1.Condition, 0)
	// check scaleTargetRef and excludeDates
	if checkGlobalParamsChanges(instance.Status, instance.Spec) {
		fmt.Println("global changed")
		for _, cJob := range conditions {
			err := r.CronManager.delete(cJob.JobId)
			if err != nil {
				log.Errorf("Failed to delete job %s,because of %v", cJob.Name, err)
			}
		}
		// update scaleTargetRef and excludeDates
		instance.Status.ScaleTargetRef = instance.Spec.ScaleTargetRef
		instance.Status.ExcludeDates = instance.Spec.ExcludeDates
	} else {
		fmt.Println("global not changed")
		// check status and delete the expired job
		for _, cJob := range conditions {
			skip := false
			for _, job := range instance.Spec.Jobs {
				if cJob.Name == job.Name {
					fmt.Println("job name is same")
					// schedule has changed or RunOnce changed
					if cJob.Schedule != job.Schedule || cJob.RunOnce != job.RunOnce || cJob.TargetSize != job.TargetSize || cJob.MaxSize != job.MaxSize || cJob.OnlySetMax != job.OnlySetMax {
						// jobId exists and remove the job from cronManager
						fmt.Println("job property has changed")
						if cJob.JobId != "" {
							fmt.Println("start delete job")
							err := r.CronManager.delete(cJob.JobId)
							if err != nil {
								log.Errorf("Failed to delete expired job %s,because of %v", cJob.Name, err)
							}
						}
						continue
					}
					skip = true
				}
			}

			if !skip {
				if cJob.JobId != "" {
					err := r.CronManager.delete(cJob.JobId)
					if err != nil {
						log.Errorf("Failed to delete expired job %s,because of %v", cJob.Name, err)
					}
				}
			}

			// need remove this condition because this is not job spec
			if skip {
				leftConditions = append(leftConditions, cJob)
			}
		}
	}

	// update the left to next step
	instance.Status.Conditions = leftConditions
	leftConditionsMap := convertConditionMaps(leftConditions)

	noNeedUpdateStatus := true

	for _, job := range instance.Spec.Jobs {
		jobCondition := autoscalingv1.Condition{
			Name:          job.Name,
			Schedule:      job.Schedule,
			RunOnce:       job.RunOnce,
			TargetSize:    job.TargetSize,
			MaxSize:       job.MaxSize,
			OnlySetMax:    job.OnlySetMax,
			LastProbeTime: metav1.Time{Time: time.Now()},
		}
		j, err := CronHPAJobFactory(instance, job, r.CronManager.scaler, r.CronManager.mapper, r.Client)

		if err != nil {
			jobCondition.State = autoscalingv1.Failed
			jobCondition.Message = fmt.Sprintf("Failed to create cron hpa job %s,because of %v", job.Name, err)
			log.Errorf("Failed to create cron hpa job %s,because of %v", job.Name, err)
		} else {
			name := job.Name
			if c, ok := leftConditionsMap[name]; ok {
				jobId := c.JobId
				j.SetID(jobId)

				// run once and return when reaches the final state
				if runOnce(job) && (c.State == autoscalingv1.Succeed || c.State == autoscalingv1.Failed) {
					err := r.CronManager.delete(jobId)
					if err != nil {
						log.Errorf("cron hpa %s(%s) has ran once but fail to exit,because of %v", name, jobId, err)
					}
					continue
				}
			}

			jobCondition.JobId = j.ID()
			err := r.CronManager.createOrUpdate(j)
			if err != nil {
				if _, ok := err.(*NoNeedUpdate); ok {
					continue
				} else {
					jobCondition.State = autoscalingv1.Failed
					jobCondition.Message = fmt.Sprintf("Failed to update cron hpa job %s,because of %v", job.Name, err)
				}
			} else {
				jobCondition.State = autoscalingv1.Submitted
			}
		}
		noNeedUpdateStatus = false
		instance.Status.Conditions = updateConditions(instance.Status.Conditions, jobCondition)
	}
	// conditions doesn't changed and no need to update.
	if !noNeedUpdateStatus || len(leftConditions) != len(conditions) {
		err := r.Status().Update(ctx, instance)
		if err != nil {
			log.Errorf("Failed to update cron hpa %s status,because of %v", instance.Name, err)
			return ctrl.Result{}, err
		} else {
			err = r.Update(ctx, instance)
			if err != nil {
				log.Errorf("Failed to update cron hpa %s,because of %v", instance.Name, err)
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CronHorizontalPodAutoscalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&autoscalingv1.CronHorizontalPodAutoscaler{}).
		Complete(r)
}

func convertConditionMaps(conditions []autoscalingv1.Condition) map[string]autoscalingv1.Condition {
	m := make(map[string]autoscalingv1.Condition)
	for _, condition := range conditions {
		m[condition.Name] = condition
	}
	return m
}

func updateConditions(conditions []autoscalingv1.Condition, condition autoscalingv1.Condition) []autoscalingv1.Condition {
	r := make([]autoscalingv1.Condition, 0)
	m := convertConditionMaps(conditions)
	m[condition.Name] = condition
	for _, condition := range m {
		r = append(r, condition)
	}
	return r
}

// if global params changed then all jobs need to be recreated.
func checkGlobalParamsChanges(status autoscalingv1.CronHorizontalPodAutoscalerStatus, spec autoscalingv1.CronHorizontalPodAutoscalerSpec) bool {
	if &status.ScaleTargetRef != nil && (status.ScaleTargetRef.Kind != spec.ScaleTargetRef.Kind || status.ScaleTargetRef.ApiVersion != spec.ScaleTargetRef.ApiVersion ||
		status.ScaleTargetRef.Name != spec.ScaleTargetRef.Name) {
		return true
	}

	excludeDatesMap := make(map[string]bool)
	for _, date := range spec.ExcludeDates {
		excludeDatesMap[date] = true
	}

	for _, date := range status.ExcludeDates {
		if excludeDatesMap[date] {
			delete(excludeDatesMap, date)
		} else {
			return true
		}
	}
	// excludeMap change
	return len(excludeDatesMap) != 0
}

func runOnce(job autoscalingv1.Job) bool {
	if strings.Contains(job.Schedule, "@date ") || job.RunOnce {
		return true
	}
	return false
}
