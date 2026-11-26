#!/bin/sh
set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
docker buildx build \
  --file "$root_dir/scripts/proto.Dockerfile" \
  --output "type=local,dest=$root_dir/internal/proto/workadventure/v1" \
  "$root_dir/proto"
