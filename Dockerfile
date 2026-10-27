# Один статический бинарник в образе без оболочки: меньше места на VPS и меньше того, что можно взломать.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/openline ./cmd/openline

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/openline /usr/local/bin/openline
COPY profiles /profiles
ENV OPENLINE_DB=/data/openline.db \
    OPENLINE_ADDR=:8080 \
    OPENLINE_ADMIN_ADDR=:9180
VOLUME /data
EXPOSE 8080 9180
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/usr/local/bin/openline", "health"]
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/openline"]
CMD ["serve"]
