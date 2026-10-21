FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/workadventure-presence-notifier \
    ./cmd/workadventure-presence-notifier && \
    mkdir -p /out/state

FROM scratch

ARG VERSION=dev
LABEL org.opencontainers.image.title="workadventure-presence-notifier" \
      org.opencontainers.image.description="Maintains an event-driven WorkAdventure presence status in a configured destination." \
      org.opencontainers.image.source="https://github.com/Arata1202/workadventure-presence-notifier" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/workadventure-presence-notifier /workadventure-presence-notifier
COPY --from=build --chown=65532:65532 /out/state /var/lib/workadventure-presence-notifier

USER 65532:65532
ENTRYPOINT ["/workadventure-presence-notifier"]
