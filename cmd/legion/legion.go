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

package main

import (
	"context"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/pkg/errors"
	"go.opencensus.io/exporter/prometheus"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"gopkg.in/alecthomas/kingpin.v2"

	"code.earth.planet.com/product/legion/internal/kubernetes"
)

const component = "legion"

func main() {
	var (
		app = kingpin.New(filepath.Base(os.Args[0]), "Serves an admission webhook that mutates pods according to the provided config.").DefaultEnvars()

		debug          = app.Flag("debug", "Run with debug logging.").Short('d').Bool()
		certFile       = app.Flag("cert", "File containing a PEM encoded certificate to be presented by the webhook listen address.").Default("cert.pem").ExistingFile()
		keyFile        = app.Flag("key", "File containing a PEM encoded key to be presented by the webhook listen address.").Default("key.pem").ExistingFile()
		listenWebhook  = app.Flag("listen-webhook", "Address at which to expose /webhook via HTTPS.").Default(":10002").String()
		listenInsecure = app.Flag("listen-insecure", "Address at which to expose /metrics and /healthz via HTTP.").Default(":10003").String()

		// TODO(negz) Move these settings into kubernetes.PodMutation? Currently
		// these settings configure _which_ pods are mutated, while PodMutation
		ignorePodsWithHostNetwork    = app.Flag("ignore-pods-with-host-network", "Do not mutate pods running in the host network namespace.").Bool()
		ignorePodsWithAnnotations    = app.Flag("ignore-pods-with-annotation", "Do not mutate pods with the specified annotations.").PlaceHolder("KEY=VALUE").StringMap()
		ignorePodsWithoutAnnotations = app.Flag("ignore-pods-without-annotation", "Do not mutate pods without the specified annotations").PlaceHolder("KEY=VALUE").StringMap()

		config = app.Arg("config-file", "A PodMutation encoded as YAML or JSON.").ExistingFile()
	)
	kingpin.MustParse(app.Parse(os.Args[1:]))

	var (
		podsReviewed = &view.View{
			Name:        "pods_reviewed_total",
			Measure:     kubernetes.MeasurePodsReviewed,
			Description: "Number of namespaces processed.",
			Aggregation: view.Count(),
			TagKeys:     []tag.Key{kubernetes.TagKind, kubernetes.TagNamespace, kubernetes.TagResult},
		}
	)
	kingpin.FatalIfError(view.Register(podsReviewed), "cannot create metrics")
	metrics, err := prometheus.NewExporter(prometheus.Options{Namespace: component})
	kingpin.FatalIfError(err, "cannot export metrics")
	view.RegisterExporter(metrics)

	log, err := zap.NewProduction()
	if *debug {
		log, err = zap.NewDevelopment()
	}
	kingpin.FatalIfError(err, "cannot create log")
	defer log.Sync() // nolint:errcheck,gosec

	g, ctx := errgroup.WithContext(context.Background())
	g.Go(func() error {
		rt := httprouter.New()
		rt.Handler(http.MethodGet, "/metrics", metrics)
		rt.HandlerFunc(http.MethodGet, "/healthz", func(_ http.ResponseWriter, _ *http.Request) {})

		log.Debug("listening for insecure requests", zap.String("listen", *listenInsecure))
		s := http.Server{Addr: *listenInsecure, Handler: rt}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s.Shutdown(sctx) // nolint:errcheck,gosec
		}()
		return errors.Wrap(s.ListenAndServe(), "cannot serve insecure requests")
	})

	g.Go(func() error {
		data, err := ioutil.ReadFile(*config)
		if err != nil {
			return errors.Wrap(err, "cannot read configuration file")
		}
		p, err := kubernetes.DecodePodMutation(data)
		if err != nil {
			return errors.Wrap(err, "cannot decode configuration file")
		}

		i := []kubernetes.IgnoreFunc{}
		if *ignorePodsWithHostNetwork {
			i = append(i, kubernetes.IgnorePodsInHostNetwork())
		}
		for k, v := range *ignorePodsWithAnnotations {
			i = append(i, kubernetes.IgnorePodsWithAnnotation(k, v))
		}
		for k, v := range *ignorePodsWithoutAnnotations {
			i = append(i, kubernetes.IgnorePodsWithoutAnnotation(k, v))
		}

		r := kubernetes.NewPodMutator(p, kubernetes.WithLogger(log), kubernetes.WithIgnoreFuncs(i...))
		rt := httprouter.New()
		rt.HandlerFunc(http.MethodPost, "/webhook", kubernetes.AdmissionReviewWebhook(r))

		log.Debug("listening for webhook requests", zap.String("listen", *listenWebhook))
		s := http.Server{Addr: *listenWebhook, Handler: rt}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s.Shutdown(sctx) // nolint:errcheck,gosec
		}()
		return errors.Wrap(s.ListenAndServeTLS(*certFile, *keyFile), "cannot serve webhook requests")
	})

	kingpin.FatalIfError(g.Wait(), "cannot serve HTTP requests")
}
