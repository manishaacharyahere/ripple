// Package controller reconciles ModuleReleases into waves of upgrade-check Jobs.
//
// The reconciler keeps no memory between calls and never trusts its own
// status: every pass rebuilds progress from the Jobs that exist. That makes a
// crash, a restart or a stale status harmless, which is exactly what the
// fault-injection tests in this package try to break (after Sieve, OSDI '22).
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ripplev1 "github.com/manishaacharyahere/ripple/api/v1alpha1"
)

// Verdicts that let a wave pass. Everything else, including a Job that
// produced no verdict at all, halts the rollout: fail closed.
var passing = map[string]bool{"clean": true, "changes": true}

const (
	// verdictGrace is how long a finished Job may show no verdict before it
	// counts as "error". Jobs and pods are cached by separate informers, so
	// the Job can look finished before its pod's termination message arrives.
	verdictGrace = 60 * time.Second
	// verdictRecheck paces those re-reads: pods aren't watched, so no event
	// would wake the reconciler when the message lands.
	verdictRecheck = 5 * time.Second
)

type ReleaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Now    func() time.Time // nil = time.Now; tests set it
}

func (r *ReleaseReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *ReleaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ripplev1.ModuleRelease{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}

func (r *ReleaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rel ripplev1.ModuleRelease
	if err := r.Get(ctx, req.NamespacedName, &rel); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !rel.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil // owner references garbage-collect the Jobs
	}

	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(rel.Namespace), client.MatchingLabels{LabelRelease: rel.Name}); err != nil {
		return ctrl.Result{}, err
	}
	byName := map[string]*batchv1.Job{}
	for i := range jobs.Items {
		byName[jobs.Items[i].Name] = &jobs.Items[i]
	}

	next := ripplev1.ModuleReleaseStatus{ObservedGeneration: rel.Generation}
	awaiting := false // some finished Job's verdict may still be in flight
	for _, wave := range rel.Spec.Waves {
		next.CurrentWave = wave.Name
		var missing []string
		running, failed := false, []string{}
		for _, consumer := range wave.Consumers {
			job, ok := byName[jobName(&rel, wave.Name, consumer)]
			if !ok {
				missing = append(missing, consumer)
				continue
			}
			res, st, err := r.result(ctx, job, wave.Name, consumer)
			if err != nil {
				return ctrl.Result{}, err
			}
			if st != checkDone {
				running = true
				awaiting = awaiting || st == checkAwaitingVerdict
				continue
			}
			next.Results = append(next.Results, res)
			if !passing[res.Status] {
				failed = append(failed, fmt.Sprintf("%s: %s", consumer, res.Status))
			}
		}

		if len(failed) > 0 {
			// Halt as soon as any finished check failed, even while others in
			// the wave still run: nothing after this wave may ever start.
			next.Phase = ripplev1.PhaseHalted
			next.Reason = "halted in wave " + wave.Name + ": " + strings.Join(failed, ", ")
			return ctrl.Result{}, r.writeStatus(ctx, &rel, next)
		}
		if len(missing) > 0 {
			for _, consumer := range missing {
				if err := r.createJob(ctx, &rel, wave.Name, consumer); err != nil {
					return ctrl.Result{}, err
				}
			}
			running = true
		}
		if running {
			next.Phase = ripplev1.PhaseRunning
			res := ctrl.Result{}
			if awaiting {
				res.RequeueAfter = verdictRecheck
			}
			return res, r.writeStatus(ctx, &rel, next)
		}
		// Wave passed: fall through to the next one.
	}
	next.Phase = ripplev1.PhaseSucceeded
	next.Reason = ""
	return ctrl.Result{}, r.writeStatus(ctx, &rel, next)
}

func (r *ReleaseReconciler) createJob(ctx context.Context, rel *ripplev1.ModuleRelease, wave, consumer string) error {
	job := buildJob(rel, wave, consumer)
	if err := controllerutil.SetControllerReference(rel, job, r.Scheme); err != nil {
		return err
	}
	err := r.Create(ctx, job)
	if apierrors.IsAlreadyExists(err) {
		return nil // our cache was stale; the Job is already there
	}
	if err == nil {
		log.FromContext(ctx).Info("started check", "job", job.Name, "wave", wave, "consumer", consumer)
	}
	return err
}

type checkState int

const (
	checkRunning checkState = iota
	checkAwaitingVerdict
	checkDone
)

// result reads a finished Job's verdict from the verdict container's
// termination message.
func (r *ReleaseReconciler) result(ctx context.Context, job *batchv1.Job, wave, consumer string) (ripplev1.CheckResult, checkState, error) {
	res := ripplev1.CheckResult{Wave: wave, Consumer: consumer, Job: job.Name}
	finishedAt, finished := jobFinished(job)
	if !finished {
		return res, checkRunning, nil
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(job.Namespace), client.MatchingLabels{"batch.kubernetes.io/job-name": job.Name}); err != nil {
		return res, checkRunning, err
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name != runnerContainer || cs.State.Terminated == nil || cs.State.Terminated.Message == "" {
				continue
			}
			var v struct {
				Status                          string
				Error                           string
				Create, Update, Replace, Delete int
			}
			if err := json.Unmarshal([]byte(cs.State.Terminated.Message), &v); err != nil || v.Status == "" {
				continue
			}
			res.Status, res.Create, res.Update, res.Replace, res.Delete = v.Status, v.Create, v.Update, v.Replace, v.Delete
			res.Message = errorLine(v.Error)
			return res, checkDone, nil
		}
	}
	if r.now().Sub(finishedAt) < verdictGrace {
		return res, checkAwaitingVerdict, nil // the pod cache may not have caught up yet
	}
	res.Status, res.Message = "error", "job finished without a verdict (runner crashed, timed out, was rejected before evidence, or pod was lost)"
	return res, checkDone, nil
}

// jobFinished reports whether the Job ended and when. A zero time (no
// timestamp recorded) counts as long ago, so it never delays a verdict.
func jobFinished(job *batchv1.Job) (time.Time, bool) {
	for _, c := range job.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return c.LastTransitionTime.Time, true
		}
	}
	return time.Time{}, false
}

func (r *ReleaseReconciler) writeStatus(ctx context.Context, rel *ripplev1.ModuleRelease, next ripplev1.ModuleReleaseStatus) error {
	if reflect.DeepEqual(rel.Status, next) {
		return nil
	}
	rel.Status = next
	return r.Status().Update(ctx, rel) // a conflict just requeues; the next pass recomputes from scratch
}

// errorLine picks Terraform's "Error: ..." line out of a runner error, falling
// back to the first line.
func errorLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	s = lines[0]
	for _, l := range lines {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "Error:") {
			s = l
			break
		}
	}
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
