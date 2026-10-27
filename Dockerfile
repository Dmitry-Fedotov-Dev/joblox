# Один статический бинарник в образе без оболочки: меньше места на VPS и меньше того, что можно взломать.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/joblox ./cmd/joblox     && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/joblox /usr/local/bin/joblox
# /data belongs to the image's nonroot user: a new named volume takes this owner, otherwise SQLite cannot write
COPY --from=build --chown=65532:65532 /out/data /data
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
