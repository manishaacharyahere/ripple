package controller

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ripplev1 "github.com/manishaacharyahere/ripple/api/v1alpha1"
)

// ---- harness -------------------------------------------------------------

type env struct {
	t      *testing.T
	ctx    context.Context
	scheme *runtime.Scheme
	c      client.Client // the "real" cluster state
	rel    types.NamespacedName
}

func newEnv(t *testing.T, waves ...ripplev1.Wave) *env {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := ripplev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	rel := &ripplev1.ModuleRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "s3", Namespace: "ripple", UID: "uid-1", Generation: 1},
		Spec:       ripplev1.ModuleReleaseSpec{Repo: "https://example/repo", From: "v1.0.0", To: "v1.1.0", Waves: waves},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rel).WithStatusSubresource(rel).Build()
	return &env{t: t, ctx: context.Background(), scheme: s, c: c, rel: client.ObjectKeyFromObject(rel)}
}

func waves() []ripplev1.Wave {
	return []ripplev1.Wave{
		{Name: "dev", Consumers: []string{"examples/a", "examples/b"}},
		{Name: "staging", Consumers: []string{"examples/c"}},
		{Name: "prod", Consumers: []string{"examples/d", "examples/e"}},
	}
}

// reconcile runs one pass with a fresh reconciler (no state survives between passes).
func (e *env) reconcile(c client.Client) error {
	r := &ReleaseReconciler{Client: c, Scheme: e.scheme}
	_, err := r.Reconcile(e.ctx, ctrl.Request{NamespacedName: e.rel})
	return err
}

func (e *env) mustReconcile() {
	e.t.Helper()
	if err := e.reconcile(e.c); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) release() *ripplev1.ModuleRelease {
	e.t.Helper()
	var rel ripplev1.ModuleRelease
	if err := e.c.Get(e.ctx, e.rel, &rel); err != nil {
		e.t.Fatal(err)
	}
	return &rel
}

func (e *env) jobs() []batchv1.Job {
	e.t.Helper()
	var l batchv1.JobList
	if err := e.c.List(e.ctx, &l, client.InNamespace("ripple")); err != nil {
		e.t.Fatal(err)
	}
	return l.Items
}

// finish plays the kubelet: the Job ends and its runner leaves a verdict.
// verdict "" means the pod vanished without one.
func (e *env) finish(job *batchv1.Job, verdict string) {
	e.t.Helper()
	cond := batchv1.JobComplete
	if verdict != "clean" && verdict != "changes" {
		cond = batchv1.JobFailed // runner exits non-zero
	}
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: cond, Status: corev1.ConditionTrue})
	if err := e.c.Status().Update(e.ctx, job); err != nil {
		e.t.Fatal(err)
	}
	if verdict == "" {
		return
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-pod", Namespace: job.Namespace,
			Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: runnerContainer, Image: "x"}}},
	}
	if err := e.c.Create(e.ctx, pod); err != nil {
		e.t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: runnerContainer, State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{Message: fmt.Sprintf(`{"status":%q,"delete":1}`, verdict)}}}}
	if err := e.c.Status().Update(e.ctx, pod); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) finishWave(wave string, verdict func(consumer string) string) {
	for _, j := range e.jobs() {
		if j.Labels[LabelWave] == wave && !finished(&j) {
			e.finish(&j, verdict(j.Annotations[AnnConsumer]))
		}
	}
}

func finished(j *batchv1.Job) bool { _, ok := jobFinished(j); return ok }

func all(v string) func(string) string { return func(string) string { return v } }

func waveCount(jobs []batchv1.Job) map[string]int {
	m := map[string]int{}
	for _, j := range jobs {
		m[j.Labels[LabelWave]]++
	}
	return m
}

// ---- behaviour -----------------------------------------------------------

func TestHappyPathRunsWavesInOrder(t *testing.T) {
	e := newEnv(t, waves()...)
	e.mustReconcile()
	if got := waveCount(e.jobs()); got["dev"] != 2 || got["staging"]+got["prod"] != 0 {
		t.Fatalf("after first pass: %v", got)
	}
	if rel := e.release(); rel.Status.Phase != ripplev1.PhaseRunning || rel.Status.CurrentWave != "dev" {
		t.Fatalf("status %+v", rel.Status)
	}
	for _, w := range []string{"dev", "staging", "prod"} {
		e.finishWave(w, all("clean"))
		e.mustReconcile()
	}
	rel := e.release()
	if rel.Status.Phase != ripplev1.PhaseSucceeded || len(rel.Status.Results) != 5 {
		t.Fatalf("status %+v", rel.Status)
	}
	if got := waveCount(e.jobs()); got["dev"] != 2 || got["staging"] != 1 || got["prod"] != 2 {
		t.Fatalf("jobs %v", got)
	}
}

func TestDestructiveHaltsBeforeNextWave(t *testing.T) {
	e := newEnv(t, waves()...)
	e.mustReconcile()
	e.finishWave("dev", func(c string) string {
		if c == "examples/b" {
			return "destructive"
		}
		return "clean"
	})
	for i := 0; i < 5; i++ { // halting is sticky across any number of passes
		e.mustReconcile()
	}
	rel := e.release()
	if rel.Status.Phase != ripplev1.PhaseHalted || rel.Status.CurrentWave != "dev" {
		t.Fatalf("status %+v", rel.Status)
	}
	if got := waveCount(e.jobs()); got["staging"]+got["prod"] != 0 {
		t.Fatalf("later waves started after a halt: %v", got)
	}
}

func TestHaltsWithoutWaitingForRestOfWave(t *testing.T) {
	e := newEnv(t, waves()...)
	e.mustReconcile()
	j := e.jobs()[0]
	e.finish(&j, "plan-failed") // the other dev check is still running
	e.mustReconcile()
	if rel := e.release(); rel.Status.Phase != ripplev1.PhaseHalted {
		t.Fatalf("status %+v", rel.Status)
	}
}

func TestMissingVerdictFailsClosed(t *testing.T) {
	for _, verdict := range []string{"", "baseline-failed"} {
		e := newEnv(t, waves()...)
		e.mustReconcile()
		e.finishWave("dev", all(verdict))
		e.mustReconcile()
		rel := e.release()
		if rel.Status.Phase != ripplev1.PhaseHalted {
			t.Fatalf("verdict %q: status %+v", verdict, rel.Status)
		}
	}
}

func TestSpecChangeStartsFreshChecks(t *testing.T) {
	e := newEnv(t, waves()...)
	e.mustReconcile()
	e.finishWave("dev", all("destructive"))
	e.mustReconcile()
	rel := e.release()
	rel.Spec.To = "v1.1.1" // the fix release
	if err := e.c.Update(e.ctx, rel); err != nil {
		t.Fatal(err)
	}
	e.mustReconcile()
	if rel := e.release(); rel.Status.Phase != ripplev1.PhaseRunning {
		t.Fatalf("old verdicts leaked into the new release: %+v", rel.Status)
	}
	if got := waveCount(e.jobs()); got["dev"] != 4 {
		t.Fatalf("want 2 old + 2 new dev jobs, got %v", got)
	}
}

func TestErrorLine(t *testing.T) {
	for in, want := range map[string]string{
		"apply: Terraform used the selected providers\n  + create\n\nError: creating S3 Control thing: 501\n  with x": "Error: creating S3 Control thing: 501",
		"git clone: exit 128": "git clone: exit 128",
		"":                    "",
	} {
		if got := errorLine(in); got != want {
			t.Errorf("errorLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJobNamesAreValidAndStable(t *testing.T) {
	rel := &ripplev1.ModuleRelease{ObjectMeta: metav1.ObjectMeta{Name: "a-very-long-release-name-that-goes-on", UID: "u"},
		Spec: ripplev1.ModuleReleaseSpec{From: "v1", To: "v2"}}
	n := jobName(rel, "production-wave-01-", "examples/x")
	if len(n) > 63 || n != jobName(rel, "production-wave-01-", "examples/x") {
		t.Fatalf("bad or unstable name %q", n)
	}
	if n == jobName(rel, "production-wave-01-", "examples/y") {
		t.Fatal("different consumers share a Job name")
	}
}

// ---- fault injection (after Sieve, OSDI '22) ------------------------------

// faulty wraps the cluster client with the perturbations Sieve found most
// bug-revealing for controllers: stale reads and crashes mid-reconcile.
type faulty struct {
	client.Client
	staleJobs       *batchv1.JobList // if set, List(Jobs) returns this old snapshot
	failCreateAfter int              // crash after this many successful Creates (-1: never)
	creates         int
}

var errCrash = errors.New("injected crash")

func (f *faulty) List(ctx context.Context, l client.ObjectList, opts ...client.ListOption) error {
	if jl, ok := l.(*batchv1.JobList); ok && f.staleJobs != nil {
		f.staleJobs.DeepCopyInto(jl)
		return nil
	}
	return f.Client.List(ctx, l, opts...)
}

func (f *faulty) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	if f.failCreateAfter >= 0 && f.creates >= f.failCreateAfter {
		return errCrash
	}
	f.creates++
	return f.Client.Create(ctx, o, opts...)
}

func TestCrashMidWaveLeavesNoDuplicates(t *testing.T) {
	e := newEnv(t, waves()...)
	if err := e.reconcile(&faulty{Client: e.c, failCreateAfter: 1}); !errors.Is(err, errCrash) {
		t.Fatalf("want injected crash, got %v", err)
	}
	if n := len(e.jobs()); n != 1 {
		t.Fatalf("crash should leave exactly one job, got %d", n)
	}
	e.mustReconcile() // the restarted operator
	e.mustReconcile()
	if got := waveCount(e.jobs()); got["dev"] != 2 {
		t.Fatalf("after restart: %v", got)
	}
}

func TestStaleCacheNeverDuplicatesOrSkipsAhead(t *testing.T) {
	e := newEnv(t, waves()...)
	empty := &batchv1.JobList{}
	e.mustReconcile() // dev jobs exist in the cluster
	// Cache still shows no Jobs at all: the reconciler must try to recreate
	// dev (getting AlreadyExists), not conclude anything about later waves.
	if err := e.reconcile(&faulty{Client: e.c, staleJobs: empty, failCreateAfter: -1}); err != nil {
		t.Fatal(err)
	}
	if got := waveCount(e.jobs()); got["dev"] != 2 || got["staging"] != 0 {
		t.Fatalf("stale cache caused %v", got)
	}
}

func TestStaleCacheHidingAFailureCannotAdvance(t *testing.T) {
	e := newEnv(t, waves()...)
	e.mustReconcile()
	snapshot := &batchv1.JobList{}
	if err := e.c.List(e.ctx, snapshot); err != nil { // dev jobs, still running
		t.Fatal(err)
	}
	e.finishWave("dev", all("destructive"))
	// The cache has not seen the Jobs finish yet: it must keep waiting, never advance.
	if err := e.reconcile(&faulty{Client: e.c, staleJobs: snapshot, failCreateAfter: -1}); err != nil {
		t.Fatal(err)
	}
	if got := waveCount(e.jobs()); got["staging"] != 0 {
		t.Fatalf("advanced on stale data: %v", got)
	}
	e.mustReconcile()
	if rel := e.release(); rel.Status.Phase != ripplev1.PhaseHalted {
		t.Fatalf("status %+v", rel.Status)
	}
}

func TestLyingStatusIsIgnored(t *testing.T) {
	e := newEnv(t, waves()...)
	e.mustReconcile()
	e.finishWave("dev", all("destructive"))
	rel := e.release()
	rel.Status = ripplev1.ModuleReleaseStatus{Phase: ripplev1.PhaseRunning, CurrentWave: "prod"} // stale or tampered
	if err := e.c.Status().Update(e.ctx, rel); err != nil {
		t.Fatal(err)
	}
	e.mustReconcile()
	if got := waveCount(e.jobs()); got["staging"]+got["prod"] != 0 {
		t.Fatalf("trusted status over observed Jobs: %v", got)
	}
	if rel := e.release(); rel.Status.Phase != ripplev1.PhaseHalted {
		t.Fatalf("status %+v", rel.Status)
	}
}

// ---- randomized oracle check (after Acto, SOSP '23) -----------------------

// TestRandomFaultSchedules interleaves Job completions, crashes and stale
// reads at random and checks three oracles after every step:
//   - safety:    no Job of wave k+1 exists unless every check in waves <= k passed
//   - no dupes:  at most one Job per (wave, consumer)
//   - liveness:  once faults stop and Jobs finish, the release ends Succeeded
//     iff every verdict passed, otherwise Halted
func TestRandomFaultSchedules(t *testing.T) {
	verdicts := []string{"clean", "clean", "clean", "changes", "destructive", "plan-failed", ""}
	for seed := int64(0); seed < 300; seed++ {
		rng := rand.New(rand.NewSource(seed))
		e := newEnv(t, waves()...)
		var snapshot *batchv1.JobList
		assigned := map[string]string{} // consumer -> verdict, fixed per run

		check := func(step int) {
			jobs := e.jobs()
			seen := map[string]bool{}
			for _, j := range jobs {
				k := j.Labels[LabelWave] + "/" + j.Annotations[AnnConsumer]
				if seen[k] {
					t.Fatalf("seed %d step %d: duplicate job for %s", seed, step, k)
				}
				seen[k] = true
			}
			for wi, w := range waves() {
				if wi == 0 {
					continue
				}
				started := false
				for _, j := range jobs {
					started = started || j.Labels[LabelWave] == w.Name
				}
				if !started {
					continue
				}
				for _, prev := range waves()[:wi] {
					for _, c := range prev.Consumers {
						if v, ok := assigned[c]; !ok || !passing[v] {
							t.Fatalf("seed %d step %d: wave %s started but %s in %s has verdict %q",
								seed, step, w.Name, c, prev.Name, v)
						}
					}
				}
			}
		}

		for step := 0; step < 40; step++ {
			switch rng.Intn(4) {
			case 0: // a running Job finishes
				for _, j := range e.jobs() {
					if !finished(&j) {
						c := j.Annotations[AnnConsumer]
						assigned[c] = verdicts[rng.Intn(len(verdicts))]
						e.finish(&j, assigned[c])
						break
					}
				}
			case 1: // crash partway through creating Jobs
				_ = e.reconcile(&faulty{Client: e.c, failCreateAfter: rng.Intn(2)})
			case 2: // reconcile on a stale cache
				if snapshot != nil {
					_ = e.reconcile(&faulty{Client: e.c, staleJobs: snapshot, failCreateAfter: -1})
				}
			default:
				e.mustReconcile()
			}
			if rng.Intn(3) == 0 {
				snapshot = &batchv1.JobList{}
				if err := e.c.List(e.ctx, snapshot); err != nil {
					t.Fatal(err)
				}
			}
			check(step)
		}

		// Faults stop: let everything run to completion.
		for i := 0; i < 20; i++ {
			e.mustReconcile()
			for _, j := range e.jobs() {
				if !finished(&j) {
					c := j.Annotations[AnnConsumer]
					assigned[c] = verdicts[rng.Intn(len(verdicts))]
					e.finish(&j, assigned[c])
				}
			}
			check(100 + i)
		}
		e.mustReconcile()

		allPassed := true
		for _, w := range waves() {
			for _, c := range w.Consumers {
				v, ok := assigned[c]
				if ok && !passing[v] {
					allPassed = false
				}
			}
		}
		rel := e.release()
		want := ripplev1.PhaseSucceeded
		if !allPassed {
			want = ripplev1.PhaseHalted
		}
		if rel.Status.Phase != want {
			t.Fatalf("seed %d: final phase %s, want %s (verdicts %v)", seed, rel.Status.Phase, want, assigned)
		}
	}
}

// A Job can look finished in the cache before its pod's termination message
// does (separate informers). That must wait and recheck, not halt; only a
// verdict still missing after the grace period fails closed.
func TestVerdictGraceAvoidsFalseHalt(t *testing.T) {
	e := newEnv(t, waves()...)
	e.mustReconcile()
	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for _, j := range e.jobs() {
		j.Status.Conditions = append(j.Status.Conditions, batchv1.JobCondition{
			Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(t0)})
		if err := e.c.Status().Update(e.ctx, &j); err != nil {
			t.Fatal(err)
		}
	}
	at := func(d time.Duration) ctrl.Result {
		r := &ReleaseReconciler{Client: e.c, Scheme: e.scheme, Now: func() time.Time { return t0.Add(d) }}
		res, err := r.Reconcile(e.ctx, ctrl.Request{NamespacedName: e.rel})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := at(10 * time.Second); res.RequeueAfter == 0 || e.release().Status.Phase != ripplev1.PhaseRunning {
		t.Fatalf("within grace: requeue=%v phase=%s, want a recheck while Running", res.RequeueAfter, e.release().Status.Phase)
	}
	at(2 * time.Minute)
	if rel := e.release(); rel.Status.Phase != ripplev1.PhaseHalted {
		t.Fatalf("after grace: phase %s, want Halted", rel.Status.Phase)
	}
}
