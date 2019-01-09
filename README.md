# Legion
Legion serves a Kubernetes mutating admission webhook that mutates pods. Pods
are mutated according to a `PodMutation`, which configures how pod fields are
set, altered, or appended during mutation.

## The `PodMutation` file.
A `PodMutation` is a configuration file following Kubernetes best practices
similar to the [Kubelet config file](https://kubernetes.io/docs/tasks/administer-cluster/kubelet-config-file/).
It is not a Custom Resource Definition in that it is read from disk, rather than
the Kubernetes API.

The following `PodMutation` configures Legion to inject an `nginx` container and
set the `example.planet.com/injected: true` annotation:

```yaml
---
apiVersion: legion.planet.com/v1alpha1
kind: PodMutation
# This metadata pertains to the PodMutation itself, not mutated pods.
metadata:
  name: example
  labels:
    mutation: example
spec:
  # The mutation strategy configures how pods are mutated.
  strategy:
    # Overwrite fields that are already set on the pod being mutated. By default
    # Legion will only modify unset fields.
    overwrite: true
    # Append to, rather than overwriting, arrays on the pod being mutated.
    append: true
  # The mutation template is merged with the pod being mutated using [Mergo](https://github.com/imdario/mergo/)
  template:
    metadata:
      # It's good practice to have Legion set an annotation indicating a pod has
      # been mutated, and to ignore pods with said annotation using the
      # --ignore-pods-with-annotation flag
      annotations:
        example.planet.com/injected: 'true'
    spec:
      containers:
      - name: nginx
        image: nginx:1.7.9
        ports:
        - containerPort: 80
```

## Usage
Legion is automatically built and pushed to GCR on merge to master. It exposes
a simple health ping at `/healthz` and Prometheus metrics at `/metrics` on port
10003 by default. The webhook is served via HTTPS at port 10002 by default.

```bash
$ docker run us.gcr.io/planet-gcr/legion:0c530f14 /legion --help
usage: legion [<flags>] [<config-file>]

Serves an admission webhook that mutates pods according to the provided config.

Flags:
      --help                     Show context-sensitive help (also try
                                 --help-long and --help-man).
  -d, --debug                    Run with debug logging.
      --cert=cert.pem            File containing a PEM encoded certificate to be
                                 presented by the webhook listen address.
      --key=key.pem              File containing a PEM encoded key to be
                                 presented by the webhook listen address.
      --listen-webhook=":10002"  Address at which to expose /webhook via HTTPS.
      --listen-insecure=":10003"  
                                 Address at which to expose /metrics and
                                 /healthz via HTTP.
      --ignore-pods-with-host-network  
                                 Do not mutate pods running in the host network
                                 namespace.
      --ignore-pods-with-annotation=KEY=VALUE ...  
                                 Do not mutate pods with the specified
                                 annotations.
      --ignore-pods-without-annotation=KEY=VALUE ...  
                                 Do not mutate pods without the specified
                                 annotations

Args:
  [<config-file>]  A PodMutation encoded as YAML or JSON.
```