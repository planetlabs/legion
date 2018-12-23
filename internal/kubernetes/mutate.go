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
	"context"
	"encoding/json"

	"github.com/appscode/jsonpatch"
	"github.com/imdario/mergo"
	"github.com/pkg/errors"
	"go.opencensus.io/stats"
	"go.opencensus.io/tag"
	"go.uber.org/zap"
	admission "k8s.io/api/admission/v1beta1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimejson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/client-go/kubernetes/scheme"
)

// Annotation values controlling injection.
const (
	MutationDone     = "mutated"
	MutationDisabled = "disabled"
)

var (
	jsonPatch   = admission.PatchTypeJSONPatch
	resourcePod = meta.GroupVersionResource{Version: "v1", Resource: "pods"}
	serializer  = runtimejson.NewSerializer(runtimejson.DefaultMetaFactory, scheme.Scheme, scheme.Scheme, false)
)

const (
	tagResultMutated = "mutated"
	tagResultIgnored = "ignored"
	tagResultError   = "error"
)

// Opencensus measurements.
var (
	MeasurePodsReviewed = stats.Int64("patch/pods_reviewed", "Number of pods reviewed.", stats.UnitDimensionless)

	TagKind, _      = tag.NewKey("kind")
	TagNamespace, _ = tag.NewKey("namespace")
	TagName, _      = tag.NewKey("name")
	TagResult, _    = tag.NewKey("result")
)

// A Patcher generates an RFC6902 JSON patch for the supplied pod.
type Patcher interface {
	Patch(core.Pod) ([]byte, error)
}

// PodMutation specifies how a pod will be mutated.
type PodMutation struct {
	meta.ObjectMeta `json:"metadata,omitempty"`
	Spec            core.PodSpec     `json:"spec,omitempty"`
	Strategy        MutationStrategy `json:"strategy,omitempty"`
}

// MutationStrategy determines how pod configuration will be injected.
type MutationStrategy struct {
	// Overwrite keys that are already set in the original pod.
	Overwrite bool `json:"overwrite,omitempty"`

	// Append to, rather than replacing, arrays in the original pod.
	Append bool `json:"append,omitempty"`
}

// Patch generates an RFC 6902 JSON patch for the supplied pod.
func (m PodMutation) Patch(original core.Pod) ([]byte, error) {
	var injected core.Pod
	original.DeepCopyInto(&injected)

	mo := []func(*mergo.Config){}
	if m.Strategy.Overwrite {
		mo = append(mo, mergo.WithOverride)
	}
	if m.Strategy.Append {
		mo = append(mo, mergo.WithAppendSlice)
	}
	if err := mergo.Merge(&injected.ObjectMeta, m.ObjectMeta, mo...); err != nil {
		return nil, errors.Wrap(err, "cannot inject pod metadata")
	}
	if err := mergo.Merge(&injected.Spec, m.Spec, mo...); err != nil {
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
	// TODO(negz): Sort patch before marshalling it.
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

// PodMutator is a Reviewer that mutates pods.
type PodMutator struct {
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

// A PodMutatorOption configures an PodMutator.
type PodMutatorOption func(d *PodMutator)

// WithLogger configures a PodMutator to use the supplied logger.
func WithLogger(l *zap.Logger) PodMutatorOption {
	return func(m *PodMutator) {
		m.l = l
	}
}

// WithIgnoreFuncs configs a PodMutator with the supplied ignore functions.
func WithIgnoreFuncs(fn ...IgnoreFunc) PodMutatorOption {
	return func(m *PodMutator) {
		m.ignore = fn
	}
}

// NewPodMutator returns a new NewPodMutator with the supplied options.
func NewPodMutator(p Patcher, mo ...PodMutatorOption) *PodMutator {
	m := &PodMutator{l: zap.NewNop(), p: p}
	for _, o := range mo {
		o(m)
	}
	return m
}

// Review approves and patches pod admission requests.
func (m *PodMutator) Review(ar *admission.AdmissionRequest) *admission.AdmissionResponse {
	log := m.l.With(
		zap.String("kind", ar.Kind.String()),
		zap.String("namespace", ar.Namespace),
		zap.String("name", ar.Name))

	tags, _ := tag.New(context.Background(), // nolint:gosec
		tag.Upsert(TagKind, ar.Kind.String()),
		tag.Upsert(TagNamespace, ar.Namespace),
		tag.Upsert(TagName, ar.Name))

	if ar.Resource != resourcePod {
		e := "cannot review non-pod resource"
		log.Info(e, zap.String("expected", resourcePod.String()), zap.String("observed", ar.Resource.String()))
		tags, _ = tag.New(tags, tag.Upsert(TagResult, tagResultError)) // nolint:gosec
		stats.Record(tags, MeasurePodsReviewed.M(1))
		return admissionError(errors.New(e), meta.StatusReasonInvalid)
	}

	var pod core.Pod
	if _, _, err := serializer.Decode(ar.Object.Raw, nil, &pod); err != nil {
		e := "cannot decode object as a pod"
		log.Info(e, zap.Error(err))
		tags, _ = tag.New(tags, tag.Upsert(TagResult, tagResultError)) // nolint:gosec
		stats.Record(tags, MeasurePodsReviewed.M(1))
		return admissionError(errors.Wrap(err, e), meta.StatusReasonInvalid)
	}

	for _, ignore := range m.ignore {
		if ignore(pod) {
			log.Info("not mutating ignored pod")
			tags, _ = tag.New(tags, tag.Upsert(TagResult, tagResultIgnored)) // nolint:gosec
			stats.Record(tags, MeasurePodsReviewed.M(1))
			return &admission.AdmissionResponse{Allowed: true}
		}
	}

	patch, err := m.p.Patch(pod)
	if err != nil {
		e := "cannot patch pod"
		log.Info(e, zap.Error(err))
		tags, _ = tag.New(tags, tag.Upsert(TagResult, tagResultError)) // nolint:gosec
		stats.Record(tags, MeasurePodsReviewed.M(1))
		return admissionError(errors.Wrap(err, e), meta.StatusReasonInternalError)
	}

	log.Debug("mutated pod")
	tags, _ = tag.New(tags, tag.Upsert(TagResult, tagResultMutated)) // nolint:gosec
	stats.Record(tags, MeasurePodsReviewed.M(1))
	return &admission.AdmissionResponse{
		UID:       ar.UID,
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
