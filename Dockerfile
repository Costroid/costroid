# syntax=docker/dockerfile:1
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Costroid Authors

# ---- prep: create /data owned by the distroless nonroot uid (65532) ----
# The runtime stage is distroless (no shell), so it cannot RUN mkdir/chown.
# Pinned to the build platform so this trivial step never runs under emulation.
FROM --platform=$BUILDPLATFORM busybox:1.37.0 AS prep
RUN mkdir -p /data

# ---- runtime ----
# costroid is cgo/DuckDB: glibc + libstdc++ DYNAMIC. cc-debian12 supplies glibc,
# libstdc++, libgcc, ca-certificates and tzdata; :nonroot gives uid 65532.
FROM gcr.io/distroless/cc-debian12:nonroot

# buildx sets TARGETARCH (amd64 | arm64) per requested --platform; the values
# match the release artifact suffixes costroid-linux-amd64 / costroid-linux-arm64.
# Copying a prebuilt native binary (no compile, no RUN) is what lets one
# `buildx --platform linux/amd64,linux/arm64` build both images with NO emulation.
# --chmod=0755 restores the exec bit that actions/upload-artifact strips.
ARG TARGETARCH
COPY --chmod=0755 costroid-linux-${TARGETARCH} /costroid

# Writable store for `costroid serve`. The default CMD (demo) does NOT use it;
# demo writes an ephemeral store under /tmp (distroless ships /tmp at mode 1777).
# /data is pre-owned by 65532 so a Docker named/anonymous volume inherits that
# ownership; a Kubernetes PVC/emptyDir instead needs securityContext.fsGroup 65532.
COPY --from=prep --chown=65532:65532 --chmod=0700 /data /data
VOLUME ["/data"]

# The listen default is loopback-only; bind all interfaces inside the container.
# COSTROID_ADDR applies to every subcommand (demo and serve). Do NOT also put
# --addr in CMD: the flag outranks the env, which would silently defeat an
# operator's `-e COSTROID_ADDR=...` override.
ENV COSTROID_ADDR=0.0.0.0:8080 \
    COSTROID_DATA_DIR=/data

LABEL org.opencontainers.image.source="https://github.com/Costroid/costroid" \
      org.opencontainers.image.description="Costroid: single-binary FOCUS cost and usage dashboard" \
      org.opencontainers.image.licenses="Apache-2.0"

EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/costroid"]
CMD ["demo"]
