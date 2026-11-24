package v1alpha1

import "testing"

// Guards the hand-written deepcopy: mutating a copy must never touch the original.
func TestDeepCopyIsolates(t *testing.T) {
	orig := &ModuleRelease{
		Spec:   ModuleReleaseSpec{Waves: []Wave{{Name: "dev", Consumers: []string{"examples/a"}}}},
		Status: ModuleReleaseStatus{Results: []CheckResult{{Status: "clean"}}},
	}
	orig.Labels = map[string]string{"k": "v"}
	cp := orig.DeepCopy()
	cp.Spec.Waves[0].Consumers[0] = "x"
	cp.Spec.Waves[0].Name = "x"
	cp.Status.Results[0].Status = "x"
	cp.Labels["k"] = "x"
	if orig.Spec.Waves[0].Consumers[0] != "examples/a" || orig.Spec.Waves[0].Name != "dev" ||
		orig.Status.Results[0].Status != "clean" || orig.Labels["k"] != "v" {
		t.Fatalf("copy aliases original: %+v", orig)
	}
}
