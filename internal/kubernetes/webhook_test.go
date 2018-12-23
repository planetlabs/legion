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
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-test/deep"
	admission "k8s.io/api/admission/v1beta1"
)

type predictableReviewer struct {
	rsp *admission.AdmissionResponse
}

func (r *predictableReviewer) Review(_ *admission.AdmissionRequest) *admission.AdmissionResponse {
	return r.rsp
}

func TestAdmissionControlWebhook(t *testing.T) {
	cases := []struct {
		name string
		r    Reviewer
		body []byte
		want []byte
	}{
		{
			name: "EmptyRequestBody",
			r:    &predictableReviewer{&admission.AdmissionResponse{Allowed: true}},
			body: []byte{},
			want: []byte("cannot parse empty request body\n"),
		},
		{
			name: "UnexpectedRequestBody",
			r:    &predictableReviewer{&admission.AdmissionResponse{Allowed: true}},
			body: []byte("imastring!"),
			want: []byte("cannot decode request body as admission review: couldn't get version/kind; json parse error: invalid character 'i' looking for beginning of value\n"),
		},
		{
			name: "MissingAdmissionRequest",
			r:    &predictableReviewer{&admission.AdmissionResponse{Allowed: true}},
			body: func() []byte {
				b := &bytes.Buffer{}
				serializer.Encode(&admission.AdmissionReview{}, b)
				return b.Bytes()
			}(),
			want: []byte("admission review must contain a request\n"),
		},
		{
			name: "PodAdmitted",
			r:    &predictableReviewer{&admission.AdmissionResponse{Allowed: true}},
			body: func() []byte {
				b := &bytes.Buffer{}
				serializer.Encode(&admission.AdmissionReview{
					Request: &admission.AdmissionRequest{},
				}, b)
				return b.Bytes()
			}(),
			want: []byte("{\"response\":{\"uid\":\"\",\"allowed\":true}}\n"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(AdmissionReviewWebhook(tc.r)))
			defer ts.Close()

			rsp, err := http.Post(ts.URL, "application/json", bytes.NewReader(tc.body))
			if err != nil {
				t.Fatalf("http.Post(): %v", err)
			}
			defer rsp.Body.Close()

			got, err := ioutil.ReadAll(rsp.Body)
			if err != nil {
				t.Fatalf("ioutil.ReadAll(): %v", err)
			}

			if diff := deep.Equal(got, tc.want); diff != nil {
				t.Errorf("got != want:\nbytes:\n  %v\nstringified:\n  got:  %s\n  want: %s\n", diff, got, tc.want)
			}
		})
	}
}
