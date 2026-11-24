package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	ripplev1 "github.com/manishaacharyahere/ripple/api/v1alpha1"
)

const (
	LabelRelease    = "ripple.dev/release"
	LabelWave       = "ripple.dev/wave"
	LabelRunner     = "app.kubernetes.io/name" // matched by the runner NetworkPolicy
	RunnerLabelVal  = "ripple-runner"
	AnnConsumer     = "ripple.dev/consumer"
	runnerContainer = "runner"
)

// jobName is deterministic in (release, spec, wave, consumer): a crashed or
// stale reconciler recreating "the same" Job gets AlreadyExists instead of a
// duplicate run, and changing the spec yields a fresh set of Jobs.
func jobName(rel *ripplev1.ModuleRelease, wave, consumer string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		rel.UID, rel.Spec.Repo, rel.Spec.From, rel.Spec.To, wave, consumer)))
	return fmt.Sprintf("%s-%s-%s", trunc(rel.Name, 20), trunc(wave, 20), hex.EncodeToString(h[:])[:10])
}

func trunc(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	for len(s) > 0 && s[len(s)-1] == '-' {
		s = s[:len(s)-1] // names must not end in '-'
	}
	return s
}

func buildJob(rel *ripplev1.ModuleRelease, wave, consumer string) *batchv1.Job {
	labels := map[string]string{LabelRelease: rel.Name, LabelWave: wave, LabelRunner: RunnerLabelVal}
	image := rel.Spec.RunnerImage
	if image == "" {
		image = "ripple-runner:dev"
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        jobName(rel, wave, consumer),
			Namespace:   rel.Namespace,
			Labels:      labels,
			Annotations: map[string]string{AnnConsumer: consumer},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          ptr.To[int32](0), // a failed plan is a result, not something to retry
			ActiveDeadlineSeconds: ptr.To[int64](1800),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName:           "ripple-runner",
					AutomountServiceAccountToken: ptr.To(false),
					RestartPolicy:                corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					InitContainers: []corev1.Container{{
						Name:          "localstack",
						Image:         "localstack/localstack:4.4",
						RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways), // native sidecar
						Env:           []corev1.EnvVar{{Name: "SERVICES", Value: "s3,iam,sts,kms"}},
						StartupProbe: &corev1.Probe{
							ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/_localstack/health", Port: intstr.FromInt32(4566)}},
							PeriodSeconds:    3,
							FailureThreshold: 60,
						},
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false)},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
						},
					}},
					Containers: []corev1.Container{{
						Name:            runnerContainer,
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args: []string{"upgrade-plan",
							"--repo=" + rel.Spec.Repo, "--example=" + consumer,
							"--endpoint=http://localhost:4566", "--workdir=/work/run",
							"--result-file=/dev/termination-log",
							rel.Spec.From, rel.Spec.To},
						TerminationMessagePolicy: corev1.TerminationMessageReadFile,
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             ptr.To(true),
							RunAsUser:                ptr.To[int64](10001),
							ReadOnlyRootFilesystem:   ptr.To(true),
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "work", MountPath: "/work"}, {Name: "tmp", MountPath: "/tmp"}},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse("2Gi"))}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse("512Mi"))}}},
					},
				},
			},
		},
	}
}
