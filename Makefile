IMAGE_NAME=qubitproducts/prometheus_gke_sd
IMAGE_VERSION=$(shell git rev-parse HEAD)

bootstrap:
	glide install

build:
	go build .

docker_build:
	docker run --rm -v "$$PWD":/go/src/github.com/QubitGroup/prometheus_gke_sd \
	  -e GOPATH=/go \
	  -w /go/src/github.com/QubitGroup/prometheus_gke_sd \
	  golang:1.7 make build

docker_image_build: docker_build
	docker build -t $(IMAGE_NAME):$(IMAGE_VERSION) .

release: docker_image_build
	docker push $(IMAGE_NAME):$(IMAGE_VERSION)
	docker tag $(IMAGE_NAME):$(IMAGE_VERSION) $(IMAGE_NAME):latest
	docker push $(IMAGE_NAME):latest
