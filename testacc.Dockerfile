# The test image: the pinned Go image plus the OpenTofu and Terraform
# binaries, copied from their pinned images (the way the OpenTofu docs advise
# for OpenTofu; the full image refuses to be a build stage). Both binaries are
# test tools only.
# The Makefile passes the pinned image references.
ARG GO_IMAGE
ARG TOFU_IMAGE
ARG TERRAFORM_IMAGE

FROM ${TOFU_IMAGE} AS tofu

FROM ${TERRAFORM_IMAGE} AS terraform

FROM ${GO_IMAGE}
COPY --from=tofu /usr/local/bin/tofu /usr/local/bin/tofu
COPY --from=terraform /bin/terraform /usr/local/bin/terraform
