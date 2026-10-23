# Build from the repository root:
#   docker build -f cmd/tapoforwarder/Dockerfile -t tapoforwarder .
# The build stage runs on the host platform and cross-compiles, so multi-arch
# builds don't need emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
	go build -trimpath -ldflags="-s -w" -o /tapoforwarder ./cmd/tapoforwarder

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /tapoforwarder /tapoforwarder
ENV TAPO_CONFIG=/data/tapoforwarder.json
EXPOSE 9105
ENTRYPOINT ["/tapoforwarder"]
