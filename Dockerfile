FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build

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

FROM scratch

ARG VERSION=dev
LABEL org.opencontainers.image.title="workadventure-presence-notifier" \
      org.opencontainers.image.description="Periodically sends the current WorkAdventure user list to external notification services." \
      org.opencontainers.image.source="https://github.com/Arata1202/workadventure-presence-notifier" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/workadventure-presence-notifier /workadventure-presence-notifier

USER 65532:65532
ENTRYPOINT ["/workadventure-presence-notifier"]
