FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/unraid-runner-manager ./cmd/unraid-runner-manager

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata bash curl iptables ip6tables
COPY --from=build /out/unraid-runner-manager /usr/local/bin/unraid-runner-manager
ENV LISTEN_ADDR=:8080 DATA_DIR=/config RUNNER_DATA_DIR=/runners
EXPOSE 8080
VOLUME ["/config"]
ENTRYPOINT ["/usr/local/bin/unraid-runner-manager"]
