# prometheus_gke_sd

This tool allows Prometheus to automatically discover kubernetes clusters running on Google Container Engine

## Building

``` go
make bootstrap build
```

We use [Glide](https://github.com/Masterminds/glide) for dep management

## Running

prometheus_gke_sd requires a config file, which should contain the following values:

``` go

$ cat ./default.yml
prometheus_config: "/etc/prometheus/prometheus.yml" # Location of main prometheus config file
certificate_store: "/etc/prometheus/credentials"    # Where should we store certificates for accessing the kube cluster?
prometheus_endpoint: "http://localhost:9090"        # Where is prometheus listening on? ( we'll use this for reloading )
gcp_project: ""                                     # What GCP project should we discover clusters in? ## REQUIRED
poll_time: 30                                       # How often should we check ?

$ ./prometheus_gke_sd -config ./default.yml

```
