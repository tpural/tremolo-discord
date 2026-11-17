FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /tremolo-discord ./cmd/tremolo-discord

FROM gcr.io/distroless/static:nonroot
COPY --from=build /tremolo-discord /tremolo-discord
ENTRYPOINT ["/tremolo-discord"]
