FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN go mod tidy && go test ./... && CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -o /out/mail2bark ./cmd/mail2bark

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget && rm -rf /var/lib/apt/lists/* && useradd --system --uid 10001 --home /nonexistent mail2bark && mkdir -p /data /certs && chown -R mail2bark:mail2bark /data /certs
COPY --from=build /out/mail2bark /usr/local/bin/mail2bark
USER mail2bark
EXPOSE 8080 25 465 587 2525
VOLUME ["/data", "/certs"]
ENTRYPOINT ["/usr/local/bin/mail2bark"]
