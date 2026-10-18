.PHONY: build test generate docker-build

build:
	go build -o bin/workadventure-presence-notifier ./cmd/workadventure-presence-notifier

test:
	go test ./...

generate:
	./scripts/generate-proto.sh

docker-build:
	docker build -t workadventure-presence-notifier:dev .
