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
	"flag"
	"net/http"
	"os"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	"github.com/julienschmidt/httprouter"
	"github.com/pkg/errors"

	"go.opencensus.io/exporter/prometheus"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"go.uber.org/zap"
	"gopkg.in/alecthomas/kingpin.v2"

	"code.earth.planet.com/product/legion/internal/kubernetes"
)

const component = "legion"

func main() {
	var (
		app = kingpin.New(filepath.Base(os.Args[0]), "Serves an admission webhook that mutates pods according to the provided config.").DefaultEnvars()

		debug          = app.Flag("debug", "Run with debug logging.").Short('d').Bool()
		certFile       = app.Flag("cert", "File containing a PEM encoded certificate to be presented by the webhook listen address.").ExistingFile()
		keyFile        = app.Flag("key", "File containing a PEM encoded key to be presented by the webhook listen address.").ExistingFile()
		listenWebhook  = app.Flag("listen-webhook", "Address at which to expose /webhook via HTTPS.").Default(":10002").String()
		listenInsecure = app.Flag("listen-insecure", "Address at which to expose /metrics and /healthz via HTTP.").Default(":10003").String()

		// config = app.Flag("config", "JSON or YAML pod mutation config.").ExistingFile()
	)
	kingpin.MustParse(app.Parse(os.Args[1:]))
	glogWorkaround()

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

	g := &errgroup.Group{}
	g.Go(func() error {
		rt := httprouter.New()
		rt.Handler(http.MethodGet, "/metrics", metrics)
		rt.HandlerFunc(http.MethodGet, "/healthz", func(_ http.ResponseWriter, _ *http.Request) {})
		return errors.Wrap(http.ListenAndServe(*listenInsecure, rt), "cannot serve insecure requests")
	})

	g.Go(func() error {
		// TODO(negz): Load this from *config. Consider making it a 'real'
		// Kubernetes compatible object so we can use the universal decoder.
		p := kubernetes.PodMutation{}
		// TODO(negz): Figure out how to handle/configure ignore funcs?
		r := kubernetes.NewPodMutator(p, kubernetes.WithLogger(log))
		rt := httprouter.New()
		// TODO(negz): Confirm webhooks are POST-ed
		rt.HandlerFunc(http.MethodPost, "/webhook", kubernetes.AdmissionReviewWebhook(r))
		return errors.Wrap(http.ListenAndServeTLS(*listenWebhook, *certFile, *keyFile, rt), "cannot serve insecure requests")
	})

	kingpin.FatalIfError(g.Wait(), "cannot serve HTTP requests")
}

// Many Kubernetes client things depend on glog. glog gets sad when flag.Parse()
// is not called before it tries to emit a log line. flag.Parse() fights with
// kingpin.
func glogWorkaround() {
	os.Args = []string{os.Args[0], "-logtostderr=true", "-v=0", "-vmodule="}
	flag.Parse()
}
