#!/bin/sh
set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
user_id=$(id -u)
group_id=$(id -g)

docker run --rm \
  -v "$root_dir:/src" \
  -w /src \
  golang:1.25-alpine \
  sh -ec '
    apk add --no-cache protobuf protobuf-dev >/dev/null
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
    protoc -I proto -I /usr/include \
      --go_out=. \
      --go_opt=module=github.com/Arata1202/workadventure-presence-notifier \
      --go-grpc_out=. \
      --go-grpc_opt=module=github.com/Arata1202/workadventure-presence-notifier \
      proto/workadventure.proto
  '

docker run --rm \
  -v "$root_dir:/src" \
  alpine:3.22 \
  chown -R "$user_id:$group_id" /src/internal/proto/workadventure/v1
