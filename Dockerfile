# Self-contained static binary: all templates and assets are embedded via
# go:embed, and CGO is off, so the final image needs nothing but the binary
# plus the CA certs distroless ships (OIDC and S3 are HTTPS). The build
# stage runs on the builder's own platform and cross-compiles via GOARCH
# (TARGETARCH), so multi-arch images need no emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.1 AS build

ARG TARGETARCH
ARG VERSION
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOARCH="${TARGETARCH}" go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o /bin/dolmen .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /bin/dolmen /dolmen
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/dolmen"]
# Mount the config file: docker run -v ./config.yaml:/config/dolmen.yaml:ro ...
CMD ["-config", "/config/dolmen.yaml"]
