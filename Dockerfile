FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/system-monitor .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -g 10001 monitor && adduser -D -u 10001 -G monitor monitor && mkdir -p /var/lib/system-monitor && chown monitor:monitor /var/lib/system-monitor
COPY --from=build /out/system-monitor /usr/local/bin/system-monitor
USER monitor
WORKDIR /var/lib/system-monitor
VOLUME ["/var/lib/system-monitor"]
EXPOSE 18080
ENTRYPOINT ["system-monitor"]
CMD ["serve"]
