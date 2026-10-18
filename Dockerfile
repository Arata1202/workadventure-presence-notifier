FROM golang:1.25-alpine AS build

ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/workadventure-presence-notifier \
    ./cmd/workadventure-presence-notifier

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/workadventure-presence-notifier /workadventure-presence-notifier

USER 65532:65532
ENTRYPOINT ["/workadventure-presence-notifier"]
