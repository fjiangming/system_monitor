FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG BUILD_REVISION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out/agents && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.buildRevision=$BUILD_REVISION" -o /out/agents/system-monitor-linux-amd64 . && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.buildRevision=$BUILD_REVISION" -o /out/agents/system-monitor-linux-arm64 . && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.buildRevision=$BUILD_REVISION" -o /out/system-monitor .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -g 10001 monitor && adduser -D -u 10001 -G monitor monitor && mkdir -p /var/lib/system-monitor && chown monitor:monitor /var/lib/system-monitor
COPY --from=build /out/system-monitor /usr/local/bin/system-monitor
COPY deploy/ /usr/local/share/system-monitor/deploy/
COPY --from=build /out/agents/ /usr/local/share/system-monitor/agents/
USER monitor
WORKDIR /var/lib/system-monitor
VOLUME ["/var/lib/system-monitor"]
EXPOSE 18080
ENTRYPOINT ["system-monitor"]
CMD ["serve"]
