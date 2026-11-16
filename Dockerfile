FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build

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
    ./cmd/workadventure-presence-notifier
RUN mkdir -p /out/state

FROM scratch

ARG VERSION=dev
LABEL org.opencontainers.image.title="workadventure-presence-notifier" \
      org.opencontainers.image.description="Maintains an event-driven Slack status for WorkAdventure presence." \
      org.opencontainers.image.source="https://github.com/Arata1202/workadventure-presence-notifier" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/workadventure-presence-notifier /workadventure-presence-notifier
COPY --from=build --chown=65532:65532 /out/state /var/lib/workadventure-presence-notifier

USER 65532:65532
ENTRYPOINT ["/workadventure-presence-notifier"]
