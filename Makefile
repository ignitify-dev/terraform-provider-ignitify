# Every target runs in a pinned container: Go, Terraform and OpenTofu are not
# needed on the build machine, only Docker.

GO_IMAGE   := golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0
TOFU_IMAGE := ghcr.io/opentofu/opentofu:1.13.2-minimal@sha256:622dc6bb98b105c18f53cf419ab64a4f3b88bd9f8c16cc978cf89e4202f4613f
TESTACC_IMAGE := terraform-provider-ignitify-testacc:local

# A named volume keeps the Go module and build caches between runs.
CACHE_VOLUME := terraform-provider-ignitify-gocache

BINARY  := terraform-provider-ignitify
VERSION ?= 0.1.0-dev

# The binary is built for the machine that runs Terraform, not for the container.
HOST_OS   := $(shell uname -s | tr '[:upper:]' '[:lower:]')
HOST_ARCH := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
GOOS   ?= $(HOST_OS)
GOARCH ?= $(HOST_ARCH)

DOCKER_RUN = docker run --rm \
	-v "$(CURDIR)":/src -w /src \
	-v $(CACHE_VOLUME):/cache \
	-e GOMODCACHE=/cache/mod -e GOCACHE=/cache/build \
	-e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0

.PHONY: build test test-tofu lint tidy testacc testacc-image clean

build:
	$(DOCKER_RUN) -e GOOS=$(GOOS) -e GOARCH=$(GOARCH) $(GO_IMAGE) \
		go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) .

test:
	$(DOCKER_RUN) $(GO_IMAGE) go test -count=1 ./...

lint:
	$(DOCKER_RUN) $(GO_IMAGE) sh -c 'go vet ./... && unformatted=$$(gofmt -l .) && if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi && echo "lint: ok"'

tidy:
	$(DOCKER_RUN) $(GO_IMAGE) go mod tidy

# The test image: the pinned Go image plus the OpenTofu binary.
testacc-image:
	docker build -q -f testacc.Dockerfile \
		--build-arg GO_IMAGE=$(GO_IMAGE) --build-arg TOFU_IMAGE=$(TOFU_IMAGE) \
		-t $(TESTACC_IMAGE) .

# Settings for the testing library to run OpenTofu as the binary.
TOFU_ENV = -e TF_ACC_TERRAFORM_PATH=/usr/local/bin/tofu -e TF_ACC_PROVIDER_HOST=registry.opentofu.org

# Real OpenTofu plans and applies against the in-memory fake API. No network.
test-tofu: testacc-image
	$(DOCKER_RUN) $(TOFU_ENV) $(TESTACC_IMAGE) go test -count=1 -run '^TestTofu' -v ./internal/provider/

# Acceptance tests create real objects. They need IGNITIFY_API_URL and
# IGNITIFY_API_KEY (an owner key) in the environment, and run OpenTofu.
testacc: testacc-image
	@test -n "$$IGNITIFY_API_URL" || (echo "IGNITIFY_API_URL is not set" && exit 1)
	@test -n "$$IGNITIFY_API_KEY" || (echo "IGNITIFY_API_KEY is not set" && exit 1)
	$(DOCKER_RUN) $(TOFU_ENV) -e TF_ACC=1 \
		-e IGNITIFY_API_URL -e IGNITIFY_API_KEY \
		$(TESTACC_IMAGE) go test -count=1 -v -timeout 30m -run '^TestAcc' ./internal/provider/...

clean:
	rm -rf bin
