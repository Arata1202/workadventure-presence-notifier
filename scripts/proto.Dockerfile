FROM scratch AS compiler-amd64
ADD --checksum=sha256:96553041f1a91ea0efee963cb16f462f5985b4d65365f3907414c360044d8065 https://github.com/protocolbuffers/protobuf/releases/download/v31.1/protoc-31.1-linux-x86_64.zip /protoc.zip

FROM scratch AS compiler-arm64
ADD --checksum=sha256:6c554de11cea04c56ebf8e45b54434019b1cd85223d4bbd25c282425e306ecc2 https://github.com/protocolbuffers/protobuf/releases/download/v31.1/protoc-31.1-linux-aarch_64.zip /protoc.zip

FROM compiler-${TARGETARCH} AS compiler

FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS generator
COPY --from=compiler /protoc.zip /tmp/protoc.zip
RUN unzip -q /tmp/protoc.zip -d /opt/protoc && \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6 && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
WORKDIR /proto
COPY workadventure.proto .
RUN mkdir /out && /opt/protoc/bin/protoc -I . -I /opt/protoc/include \
    --go_out=paths=source_relative:/out --go-grpc_out=paths=source_relative:/out workadventure.proto

FROM scratch
COPY --from=generator /out/ /
