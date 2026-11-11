# bouncewarden — all-in-one monitoring console for PgBouncer.
# Multi-stage: compiles a static Go binary, no toolchain in the runtime image.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/bouncewarden ./cmd/bouncewarden

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 bw && mkdir -p /data && chown bw /data
USER bw
VOLUME /data
COPY --from=build /out/bouncewarden /usr/local/bin/bouncewarden
EXPOSE 8080
ENTRYPOINT ["bouncewarden"]
CMD ["serve", "--config", "/data/config.yaml"]
