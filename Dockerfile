# Один статический бинарник в образе без оболочки: меньше места на VPS и меньше того, что можно взломать.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/joblox ./cmd/joblox

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/joblox /usr/local/bin/joblox
COPY profiles /profiles
ENV JOBLOX_DB=/data/joblox.db \
    JOBLOX_ADDR=:8080 \
    JOBLOX_ADMIN_ADDR=:9180
VOLUME /data
EXPOSE 8080 9180
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/usr/local/bin/joblox", "health"]
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/joblox"]
CMD ["serve"]
