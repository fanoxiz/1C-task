FROM golang:1.26.8-alpine3.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/mobius ./cmd/mobius \
    && CGO_ENABLED=0 go build -trimpath -o /out/loki ./cmd/loki

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/ /app/
USER 65532:65532
CMD ["/app/mobius"]
