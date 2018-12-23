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
	"testing"

	"github.com/go-test/deep"
	"github.com/pkg/errors"
	admission "k8s.io/api/admission/v1beta1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	coolPatch = []byte("coolpatch")
	coolPod   = core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:        "coolpod",
			Namespace:   "coolnamespace",
			Labels:      map[string]string{"cool": "true"},
			Annotations: map[string]string{"cool": "true"},
		},
		Spec: core.PodSpec{
			DNSPolicy: core.DNSClusterFirst,
			Containers: []core.Container{{
				Name:    "coolcontainer",
				Image:   "coolimage:coolest",
				Command: []string{"/cool"},
				Args:    []string{"-very"},
			}},
		},
	}
)

func TestPatch(t *testing.T) {
	cases := []struct {
		name string
		pod  core.Pod
		spec PodInjection
		want []byte
	}{
		{
			name: "NoOp",
			pod:  coolPod,
			spec: PodInjection{},
			want: []byte("[]"),
		},
		{
			name: "AddAnnotation",
			pod:  coolPod,
			spec: PodInjection{
				ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{"supercool": "alsotrue"}},
			},
			want: []byte("[{\"op\":\"add\",\"path\":\"/metadata/annotations/supercool\",\"value\":\"alsotrue\"}]"),
		},
		{
			name: "AddContainer",
			pod:  coolPod,
			spec: PodInjection{
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "coolercontainer",
						Image: "extracool:somehowmorecool",
					}},
				},
				Strategy: InjectionStrategy{Append: true},
			},
			want: []byte("[{\"op\":\"add\",\"path\":\"/spec/containers/1\",\"value\":{\"image\":\"extracool:somehowmorecool\",\"name\":\"coolercontainer\",\"resources\":{}}}]"),
		},
		{
			name: "OverrideNameservers",
			pod:  coolPod,
			spec: PodInjection{
				Spec: core.PodSpec{
					DNSPolicy: core.DNSNone,
					DNSConfig: &core.PodDNSConfig{Nameservers: []string{"127.0.0.1"}},
				},
				Strategy: InjectionStrategy{Overwrite: true},
			},
			want: []byte("[{\"op\":\"replace\",\"path\":\"/spec/dnsPolicy\",\"value\":\"None\"},{\"op\":\"add\",\"path\":\"/spec/dnsConfig\",\"value\":{\"nameservers\":[\"127.0.0.1\"]}}]"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := tc.spec.Patch(tc.pod)
			if diff := deep.Equal(got, tc.want); diff != nil {
				t.Errorf("got != want:\nbytes:\n  %v\nstringified:\n  got:  %s\n  want: %s\n", diff, got, tc.want)
			}
		})
	}
}

type predictablePatcher struct {
	patch []byte
	err   error
}

func (p *predictablePatcher) Patch(_ core.Pod) ([]byte, error) {
	return p.patch, p.err
}

func TestReview(t *testing.T) {
	cases := []struct {
		name    string
		patcher Patcher
		options []PodInjectorOption
		ar      *admission.AdmissionRequest
		want    *admission.AdmissionResponse
	}{
		{
			name:    "ResourceIsNotAPod",
			patcher: &predictablePatcher{},
			ar:      &admission.AdmissionRequest{},
			want: &admission.AdmissionResponse{
				Result: &meta.Status{
					Status:  meta.StatusFailure,
					Reason:  meta.StatusReasonInvalid,
					Message: "not reviewing unexpected non-pod resource",
				},
			},
		},
		{
			name:    "ObjectIsNotAPod",
			patcher: &predictablePatcher{},
			ar: &admission.AdmissionRequest{
				Resource: resourcePod,
				Object:   runtime.RawExtension{Raw: []byte{}},
			},
			want: &admission.AdmissionResponse{
				Result: &meta.Status{
					Status:  meta.StatusFailure,
					Reason:  meta.StatusReasonInvalid,
					Message: "cannot decode object as a pod: couldn't get version/kind; json parse error: unexpected end of JSON input",
				},
			},
		},
		{
			name:    "PodWithHostNetworkIsIgnored",
			patcher: &predictablePatcher{patch: coolPatch},
			options: []PodInjectorOption{WithIgnoreFuncs(IgnorePodsInHostNetwork())},
			ar: &admission.AdmissionRequest{
				Resource: resourcePod,
				Object: runtime.RawExtension{Raw: func() []byte {
					b := &bytes.Buffer{}
					p := &core.Pod{Spec: core.PodSpec{HostNetwork: true}}
					serializer.Encode(p, b)
					return b.Bytes()
				}()},
			},
			want: &admission.AdmissionResponse{Allowed: true},
		},
		{
			name:    "PodWithAnnotationIsIgnored",
			patcher: &predictablePatcher{patch: coolPatch},
			options: []PodInjectorOption{WithIgnoreFuncs(IgnorePodsWithAnnotation("cool", "nope"))},
			ar: &admission.AdmissionRequest{
				Resource: resourcePod,
				Object: runtime.RawExtension{Raw: func() []byte {
					b := &bytes.Buffer{}
					p := &core.Pod{}
					p.SetAnnotations(map[string]string{"cool": "nope"})
					serializer.Encode(p, b)
					return b.Bytes()
				}()},
			},
			want: &admission.AdmissionResponse{Allowed: true},
		},
		{
			name:    "PatchError",
			patcher: &predictablePatcher{err: errors.New("boom")},
			ar: &admission.AdmissionRequest{
				Resource: resourcePod,
				Object: runtime.RawExtension{Raw: func() []byte {
					b := &bytes.Buffer{}
					serializer.Encode(&coolPod, b)
					return b.Bytes()
				}()},
			},
			want: &admission.AdmissionResponse{
				Result: &meta.Status{
					Status:  meta.StatusFailure,
					Reason:  meta.StatusReasonInternalError,
					Message: "cannot patch pod: boom",
				},
			},
		},
		{
			name:    "PatchSuccessful",
			patcher: &predictablePatcher{patch: coolPatch},
			ar: &admission.AdmissionRequest{
				Resource: resourcePod,
				Object: runtime.RawExtension{Raw: func() []byte {
					b := &bytes.Buffer{}
					serializer.Encode(&coolPod, b)
					return b.Bytes()
				}()},
			},
			want: &admission.AdmissionResponse{
				Allowed:   true,
				Patch:     coolPatch,
				PatchType: &jsonPatch,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := NewPodInjector(tc.patcher, tc.options...)
			if diff := deep.Equal(i.Review(tc.ar), tc.want); diff != nil {
				t.Errorf("got != want:\n%v\n", diff)
			}
		})
	}
}