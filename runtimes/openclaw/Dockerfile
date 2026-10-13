FROM golang:1.26.9-alpine AS builder

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -o /claw-entrypoint .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /claw-entrypoint /usr/bin/claw-entrypoint
USER 65532:65532
EXPOSE 18900
ENTRYPOINT ["/usr/bin/claw-entrypoint"]
