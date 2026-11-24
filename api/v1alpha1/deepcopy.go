package v1alpha1

import "k8s.io/apimachinery/pkg/runtime"

// Hand-written in place of controller-gen output; keep in sync with types.go.

func (in *ModuleReleaseSpec) DeepCopyInto(out *ModuleReleaseSpec) {
	*out = *in
	if in.Waves != nil {
		out.Waves = make([]Wave, len(in.Waves))
		for i := range in.Waves {
			out.Waves[i] = Wave{Name: in.Waves[i].Name, Consumers: append([]string(nil), in.Waves[i].Consumers...)}
		}
	}
}

func (in *ModuleReleaseStatus) DeepCopyInto(out *ModuleReleaseStatus) {
	*out = *in
	if in.Results != nil {
		out.Results = append([]CheckResult(nil), in.Results...) // CheckResult has only value fields
	}
}

func (in *ModuleRelease) DeepCopyInto(out *ModuleRelease) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *ModuleRelease) DeepCopy() *ModuleRelease {
	if in == nil {
		return nil
	}
	out := new(ModuleRelease)
	in.DeepCopyInto(out)
	return out
}

func (in *ModuleRelease) DeepCopyObject() runtime.Object { return in.DeepCopy() }

func (in *ModuleReleaseList) DeepCopyInto(out *ModuleReleaseList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]ModuleRelease, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *ModuleReleaseList) DeepCopy() *ModuleReleaseList {
	if in == nil {
		return nil
	}
	out := new(ModuleReleaseList)
	in.DeepCopyInto(out)
	return out
}

func (in *ModuleReleaseList) DeepCopyObject() runtime.Object { return in.DeepCopy() }
