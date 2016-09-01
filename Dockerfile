FROM ubuntu

COPY	./prometheus_gke_sd	/usr/bin/prometheus_gke_sd

ENTRYPOINT	["/usr/bin/prometheus_gke_sd"]
