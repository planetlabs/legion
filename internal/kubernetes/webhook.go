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
	"io/ioutil"
	"net/http"

	"github.com/pkg/errors"
	admission "k8s.io/api/admission/v1beta1"
)

// A Reviewer reviews admission requests.
type Reviewer interface {
	Review(*admission.AdmissionRequest) *admission.AdmissionResponse
}

// AdmissionReviewWebhook returns a new admission review webhook. Admission
// requests are reviewed by the supplied Reviewer.
func AdmissionReviewWebhook(r Reviewer) http.HandlerFunc {
	return func(w http.ResponseWriter, rq *http.Request) {
		b, err := ioutil.ReadAll(rq.Body)
		if err != nil {
			http.Error(w, errors.Wrap(err, "cannot read request body").Error(), http.StatusBadRequest)
			return
		}
		if len(b) == 0 {
			http.Error(w, "cannot parse empty request body", http.StatusBadRequest)
			return
		}
		ar := &admission.AdmissionReview{}
		if _, _, err := serializer.Decode(b, nil, ar); err != nil {
			http.Error(w, errors.Wrap(err, "cannot decode request body as admission review").Error(), http.StatusBadRequest)
			return
		}
		if ar.Request == nil {
			http.Error(w, "admission review must contain a request", http.StatusBadRequest)
			return
		}
		serializer.Encode(&admission.AdmissionReview{Response: r.Review(ar.Request)}, w) // nolint:gosec,errcheck
	}
}
