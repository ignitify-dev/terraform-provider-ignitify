# The test image: the pinned Go image plus the OpenTofu binary, copied from the
# pinned minimal OpenTofu image (the way the OpenTofu docs advise; the full
# image refuses to be a build stage).
# The Makefile passes both pinned image references.
ARG GO_IMAGE
ARG TOFU_IMAGE

FROM ${TOFU_IMAGE} AS tofu

FROM ${GO_IMAGE}
COPY --from=tofu /usr/local/bin/tofu /usr/local/bin/tofu
