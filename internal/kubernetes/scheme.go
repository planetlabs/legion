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
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	runtimeserializer "k8s.io/apimachinery/pkg/runtime/serializer"
)

// GroupName is the group name used in this package.
const GroupName = "legion.planet.com"

// Scheme registration utilities.
var (
	SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: runtime.APIVersionInternal}
	SchemeBuilder      = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PodMutation{})
		return nil
	})
	AddToScheme = SchemeBuilder.AddToScheme
)

// DecodePodMutation decodes a PodMutation from the provided bytes. It uses
// k8s.io/apimachinery's UniversalDecoder in order to decode bytes encoded in
// any format supported by Kubernetes (i.e. YAML, JSON, etc).
func DecodePodMutation(data []byte) (PodMutation, error) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		return PodMutation{}, errors.Wrap(err, "cannot register configuration scheme")
	}
	codecs := runtimeserializer.NewCodecFactory(scheme)

	var pm PodMutation
	if _, _, err := codecs.UniversalDecoder().Decode(data, nil, &pm); err != nil {
		return PodMutation{}, errors.Wrap(err, "cannot decode PodMutation")
	}
	return pm, nil
}
