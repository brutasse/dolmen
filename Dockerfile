# Self-contained static binary: all templates and assets are embedded via
# go:embed, and CGO is off, so the final image needs nothing but the binary
# plus the CA certs distroless ships (OIDC and S3 are HTTPS).
FROM golang:1.26.1 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /bin/dolmen .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /bin/dolmen /dolmen
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/dolmen"]
# Mount the config file: docker run -v ./config.yaml:/config/dolmen.yaml:ro ...
CMD ["-config", "/config/dolmen.yaml"]
