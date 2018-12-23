/*
Copyright 2018 Planet Labs Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
implied. See the License for the specific language governing permissions
and limitations under the License.
*/

package kubernetes

import (
	"bytes"
	"encoding/json"

	"github.com/imdario/mergo"

	"github.com/appscode/jsonpatch"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	admission "k8s.io/api/admission/v1beta1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimejson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/client-go/kubernetes/scheme"
)

// Annotation values controlling injection.
const (
	InjectionStatusDone = "injected"
	InjectionDisabled   = "disable"
)

var (
	jsonPatch   = admission.PatchTypeJSONPatch
	resourcePod = meta.GroupVersionResource{Version: "v1", Resource: "pods"}
	serializer  = runtimejson.NewSerializer(runtimejson.DefaultMetaFactory, scheme.Scheme, scheme.Scheme, false)
)

// A Patcher generates an RFC6902 JSON patch for the supplied pod.
type Patcher interface {
	Patch(core.Pod) ([]byte, error)
}

// PodInjection specifies what will by injected into a pod.
type PodInjection struct {
	meta.ObjectMeta `json:"metadata,omitempty"`
	Spec            core.PodSpec      `json:"spec,omitempty"`
	Strategy        InjectionStrategy `json:"strategy,omitempty"`
}

// InjectionStrategy determines how pod configuration will be injected.
type InjectionStrategy struct {
	// Overwrite keys that are already set in the original pod.
	Overwrite bool `json:"overwrite,omitempty"`

	// Append to, rather than replacing, arrays in the original pod.
	Append bool `json:"append,omitempty"`
}

// Patch generates an RFC 6902 JSON patch for the supplied pod.
func (s PodInjection) Patch(original core.Pod) ([]byte, error) {
	var injected core.Pod
	original.DeepCopyInto(&injected)

	mo := []func(*mergo.Config){}
	if s.Strategy.Overwrite {
		mo = append(mo, mergo.WithOverride)
	}
	if s.Strategy.Append {
		mo = append(mo, mergo.WithAppendSlice)
	}
	if err := mergo.Merge(&injected.ObjectMeta, s.ObjectMeta, mo...); err != nil {
		return nil, errors.Wrap(err, "cannot inject pod metadata")
	}
	if err := mergo.Merge(&injected.Spec, s.Spec, mo...); err != nil {
		return nil, errors.Wrap(err, "cannot inject pod spec")
	}

	ob := &bytes.Buffer{}
	if err := serializer.Encode(&original, ob); err != nil {
		return nil, errors.Wrap(err, "cannot encode original pod as JSON")
	}
	pb := &bytes.Buffer{}
	if err := serializer.Encode(&injected, pb); err != nil {
		return nil, errors.Wrap(err, "cannot encode patched pod as JSON")
	}
	patch, err := jsonpatch.CreatePatch(ob.Bytes(), pb.Bytes())
	if err != nil {
		return nil, errors.Wrap(err, "cannot create patch")
	}
	b, err := json.Marshal(patch)
	if err != nil {
		return nil, errors.Wrap(err, "cannot encode patch as JSON")
	}
	return b, nil
}

// A Reviewer reviews admission requests.
type Reviewer interface {
	Review(*admission.AdmissionReview) *admission.AdmissionResponse
}

// PodInjector is a Reviewer that approves and patches pod admission requests.
type PodInjector struct {
	l      *zap.Logger
	p      Patcher
	ignore []IgnoreFunc
}

// IgnoreFunc returns true if a pod should be allowed without injection.
type IgnoreFunc func(core.Pod) bool

// IgnorePodsInHostNetwork returns a function that ignores pods in the host
// network namespace.
func IgnorePodsInHostNetwork() IgnoreFunc {
	return func(p core.Pod) bool {
		return p.Spec.HostNetwork
	}
}

// IgnorePodsWithAnnotation returns a function that ignores pods with the
// supplied annotation.
func IgnorePodsWithAnnotation(k, v string) IgnoreFunc {
	return func(p core.Pod) bool {
		return p.GetAnnotations()[k] == v
	}
}

// A PodInjectorOption configures an PodInjector.
type PodInjectorOption func(d *PodInjector)

// WithLogger configures a PodInjector to use the supplied logger.
func WithLogger(l *zap.Logger) PodInjectorOption {
	return func(i *PodInjector) {
		i.l = l
	}
}

// WithIgnoreFuncs configs a PodInjector with the supplied ignore functions.
func WithIgnoreFuncs(fn ...IgnoreFunc) PodInjectorOption {
	return func(i *PodInjector) {
		i.ignore = fn
	}
}

// NewPodInjector returns a new NewPodInjector with the supplied options.
func NewPodInjector(p Patcher, io ...PodInjectorOption) *PodInjector {
	i := &PodInjector{l: zap.NewNop(), p: p}
	for _, o := range io {
		o(i)
	}
	return i
}

// Review approves and patches pod admission requests.
func (i *PodInjector) Review(ar *admission.AdmissionRequest) *admission.AdmissionResponse {
	log := i.l.With(
		zap.String("kind", ar.Kind.String()),
		zap.String("namespace", ar.Namespace),
		zap.String("name", ar.Name))

	if ar.Resource != resourcePod {
		e := "not reviewing unexpected non-pod resource"
		log.Info(e, zap.String("expected", resourcePod.String()), zap.String("observed", ar.Resource.String()))
		return admissionError(errors.New(e), meta.StatusReasonInvalid)
	}

	var pod core.Pod
	if _, _, err := serializer.Decode(ar.Object.Raw, nil, &pod); err != nil {
		e := "cannot decode object as a pod"
		log.Info(e, zap.Error(err))
		return admissionError(errors.Wrap(err, e), meta.StatusReasonInvalid)
	}

	for _, ignore := range i.ignore {
		if ignore(pod) {
			log.Info("not mutating ignored pod")
			return &admission.AdmissionResponse{Allowed: true}
		}
	}

	patch, err := i.p.Patch(pod)
	if err != nil {
		e := "cannot patch pod"
		log.Info(e, zap.Error(err))
		return admissionError(errors.Wrap(err, e), meta.StatusReasonInternalError)
	}

	return &admission.AdmissionResponse{
		Allowed:   true,
		Patch:     patch,
		PatchType: &jsonPatch,
	}
}

func admissionError(err error, reason meta.StatusReason) *admission.AdmissionResponse {
	return &admission.AdmissionResponse{
		Result: &meta.Status{
			Status:  meta.StatusFailure,
			Reason:  reason,
			Message: err.Error(),
		},
	}
}
