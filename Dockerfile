# One image for the service, the worker and the customer-side pull agent. It
# holds one static binary with its NOTICE and LICENSE, and runs as a user with
# no shell and no home.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=$VERSION" -o /out/costroid ./cmd/costroid

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/costroid /usr/local/bin/costroid
COPY NOTICE LICENSE /usr/share/doc/costroid/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/costroid"]
CMD ["serve"]
