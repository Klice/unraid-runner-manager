FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/unraid-runner-manager ./cmd/unraid-runner-manager

FROM gcr.io/distroless/static-debian12:latest
COPY --from=build /out/unraid-runner-manager /unraid-runner-manager
ENV LISTEN_ADDR=:8080 DATA_DIR=/config RUNNER_DATA_DIR=/runners
EXPOSE 8080
VOLUME ["/config"]
ENTRYPOINT ["/unraid-runner-manager"]
