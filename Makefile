# Every target runs in a pinned container: Go, Terraform and OpenTofu are not
# needed on the build machine, only Docker. Terraform and OpenTofu are test
# tools only; neither is shipped with the provider.

GO_IMAGE   := golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0
TOFU_IMAGE := ghcr.io/opentofu/opentofu:1.13.2-minimal@sha256:622dc6bb98b105c18f53cf419ab64a4f3b88bd9f8c16cc978cf89e4202f4613f
TERRAFORM_IMAGE := hashicorp/terraform:1.17.1@sha256:a5d5a5a892c98f60fcbfa81eac2a40fc19e7b3fa7719bccae10ff6111d22524a
TESTACC_IMAGE := terraform-provider-ignitify-testacc:local
GORELEASER_IMAGE := goreleaser/goreleaser:v2.19.0@sha256:b62e651c2fd9e7b1c94133a4e5d131187649e45aeeca137fbf4940f0f654ad08
GITLEAKS_IMAGE   := ghcr.io/gitleaks/gitleaks:v8.30.1@sha256:c00b6bd0aeb3071cbcb79009cb16a60dd9e0a7c60e2be9ab65d25e6bc8abbb7f

# A named volume keeps the Go module and build caches between runs.
CACHE_VOLUME := terraform-provider-ignitify-gocache

BINARY  := terraform-provider-ignitify
VERSION ?= 0.2.0-dev

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

.PHONY: build test test-tofu test-terraform lint tidy testacc testacc-image check-cases docs docs-check notices notices-check release-check secret-scan clean

build:
	$(DOCKER_RUN) -e GOOS=$(GOOS) -e GOARCH=$(GOARCH) $(GO_IMAGE) \
		go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) .

test:
	$(DOCKER_RUN) $(GO_IMAGE) go test -count=1 ./...

lint:
	$(DOCKER_RUN) $(GO_IMAGE) sh -c 'go vet ./... && unformatted=$$(gofmt -l .) && if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi && echo "lint: ok"'

tidy:
	$(DOCKER_RUN) $(GO_IMAGE) go mod tidy

# The test image: the pinned Go image plus the OpenTofu and Terraform binaries.
testacc-image:
	docker build -q -f testacc.Dockerfile \
		--build-arg GO_IMAGE=$(GO_IMAGE) --build-arg TOFU_IMAGE=$(TOFU_IMAGE) --build-arg TERRAFORM_IMAGE=$(TERRAFORM_IMAGE) \
		-t $(TESTACC_IMAGE) .

# Settings for the testing library to run OpenTofu as the binary.
TOFU_ENV = -e TF_ACC_TERRAFORM_PATH=/usr/local/bin/tofu -e TF_ACC_PROVIDER_HOST=registry.opentofu.org

# Real OpenTofu plans and applies against the in-memory fake API. No network.
test-tofu: testacc-image
	$(DOCKER_RUN) $(TOFU_ENV) $(TESTACC_IMAGE) go test -count=1 -run '^TestTofu' -v ./internal/provider/

# Settings for the testing library to run Terraform as the binary.
TERRAFORM_ENV = -e TF_ACC_TERRAFORM_PATH=/usr/local/bin/terraform -e TF_ACC_PROVIDER_HOST=registry.terraform.io

# The same plans and applies as test-tofu, run by Terraform. No network.
test-terraform: testacc-image
	$(DOCKER_RUN) $(TERRAFORM_ENV) $(TESTACC_IMAGE) go test -count=1 -run '^TestTofu' -v ./internal/provider/

# Acceptance tests create real objects. They need IGNITIFY_API_URL and
# IGNITIFY_API_KEY (an owner key) in the environment. IGNITIFY_ACC_QUOTA_API_KEY,
# when set, is a key of a devtrial tenant for the schedule quota test.
# TESTACC_NETWORK, when set, is the test container's docker network (for
# example container:<name> to share a local forwarder's network); empty keeps
# the default network. TESTACC_CLI is tofu (the default) or terraform.
# TESTACC_RUN selects the tests, for example ^TestAccSchedule.
TESTACC_NETWORK ?=
TESTACC_CLI ?= tofu
TESTACC_RUN ?= ^TestAcc
TESTACC_ENV = $(if $(filter terraform,$(TESTACC_CLI)),$(TERRAFORM_ENV),$(TOFU_ENV))

testacc: testacc-image
	@test -n "$$IGNITIFY_API_URL" || (echo "IGNITIFY_API_URL is not set" && exit 1)
	@test -n "$$IGNITIFY_API_KEY" || (echo "IGNITIFY_API_KEY is not set" && exit 1)
	$(DOCKER_RUN) $(if $(TESTACC_NETWORK),--network $(TESTACC_NETWORK)) $(TESTACC_ENV) -e TF_ACC=1 \
		-e IGNITIFY_API_URL -e IGNITIFY_API_KEY -e IGNITIFY_ACC_QUOTA_API_KEY \
		$(TESTACC_IMAGE) go test -count=1 -v -timeout 30m -run '$(TESTACC_RUN)' ./internal/provider/...

# The shared selection cases are a copy of the platform's file and must equal
# it byte for byte. PLATFORM_DIR is a checkout of the platform source; without
# it the check is skipped.
CASES_FILE     := internal/source/testdata/cases.json
PLATFORM_CASES := contracts/source-selection/golden/cases.json

check-cases:
	@if [ -z "$(PLATFORM_DIR)" ] || [ ! -f "$(PLATFORM_DIR)/$(PLATFORM_CASES)" ]; then \
		echo "check-cases: skipped: set PLATFORM_DIR to the platform source checkout that holds $(PLATFORM_CASES)"; \
	else \
		docker run --rm -v "$(CURDIR)":/src:ro -v "$(abspath $(PLATFORM_DIR))":/platform:ro -w /src $(GO_IMAGE) sh -c '\
			here=$$(sha256sum $(CASES_FILE) | cut -d" " -f1); \
			there=$$(sha256sum /platform/$(PLATFORM_CASES) | cut -d" " -f1); \
			echo "provider copy: $$here"; echo "platform file: $$there"; \
			if [ "$$here" != "$$there" ]; then echo "check-cases: FAILED: copy the platform file unchanged"; exit 1; fi; \
			echo "check-cases: ok"'; \
	fi

# Registry docs. tfplugindocs (pinned in tools/go.mod) reads the provider
# schema through the Terraform binary of the test image and renders docs/ from
# the schema and examples/{provider,resources,data-sources}.
TOOL_BIN := /cache/tools
TFPLUGINDOCS = go build -C tools -o $(TOOL_BIN)/tfplugindocs github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs
DOCS_ENV = -e CHECKPOINT_DISABLE=1 -e PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin

docs: testacc-image
	$(DOCKER_RUN) $(DOCS_ENV) $(TESTACC_IMAGE) sh -c '$(TFPLUGINDOCS) && \
		$(TOOL_BIN)/tfplugindocs generate --provider-name ignitify && \
		$(TOOL_BIN)/tfplugindocs validate --provider-name ignitify'

# Generates the docs in a copy of the tree and compares them with docs/.
docs-check: testacc-image
	$(DOCKER_RUN) $(DOCS_ENV) $(TESTACC_IMAGE) sh -c '$(TFPLUGINDOCS) && \
		rm -rf /tmp/docs-check && mkdir /tmp/docs-check && \
		tar -C /src --exclude=./.git --exclude=./bin --exclude=./dist -cf - . | tar -C /tmp/docs-check -xf - && \
		cd /tmp/docs-check && rm -rf docs && \
		$(TOOL_BIN)/tfplugindocs generate --provider-name ignitify >/dev/null && \
		$(TOOL_BIN)/tfplugindocs validate --provider-name ignitify && \
		if diff -r /src/docs /tmp/docs-check/docs; then echo "docs-check: ok"; else echo "docs-check: FAILED: run make docs"; exit 1; fi'

# Third-party notices: the licence of every module linked into the binary, by
# go-licenses (pinned in tools/go.mod). The check fails on any licence outside
# the allowed list. Release zips carry the file.
NOTICES_FILE := THIRD_PARTY_NOTICES
ALLOWED_LICENSES := MPL-2.0,MIT,BSD-2-Clause,BSD-3-Clause,Apache-2.0
GO_LICENSES = go build -C tools -o $(TOOL_BIN)/go-licenses github.com/google/go-licenses/v2

NOTICES = sh tools/notices.sh

notices:
	$(DOCKER_RUN) $(GO_IMAGE) sh -c '$(GO_LICENSES) && \
		$(NOTICES) $(NOTICES_FILE).tmp $(ALLOWED_LICENSES) $(TOOL_BIN)/go-licenses && \
		mv $(NOTICES_FILE).tmp $(NOTICES_FILE) && echo "notices: ok"'

notices-check:
	$(DOCKER_RUN) $(GO_IMAGE) sh -c '$(GO_LICENSES) && \
		$(NOTICES) /tmp/notices $(ALLOWED_LICENSES) $(TOOL_BIN)/go-licenses && \
		if cmp -s /tmp/notices $(NOTICES_FILE); then echo "notices-check: ok"; else echo "notices-check: FAILED: run make notices"; exit 1; fi'

# git refuses a repo owned by another user; the containers run as root.
GIT_SAFE = -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src

# goreleaser check and a local snapshot build into dist/: nothing is published
# and nothing is signed.
release-check:
	$(DOCKER_RUN) $(GIT_SAFE) -e GOTOOLCHAIN=local --entrypoint sh $(GORELEASER_IMAGE) -c '\
		goreleaser check && goreleaser release --snapshot --clean --skip=publish,sign'

# Secret scan with the default rules: every commit reachable from any ref,
# then the files not yet committed (the build output is left out).
secret-scan:
	docker run --rm -v "$(CURDIR)":/src:ro $(GIT_SAFE) $(GITLEAKS_IMAGE) \
		git --log-opts=--all --redact --verbose /src
	docker run --rm -v "$(CURDIR)":/src:ro --entrypoint sh $(GITLEAKS_IMAGE) -c '\
		mkdir /tmp/tree && tar -C /src --exclude=./.git --exclude=./bin --exclude=./dist -cf - . | tar -C /tmp/tree -xf - && \
		gitleaks dir --redact --verbose /tmp/tree'

clean:
	rm -rf bin dist
