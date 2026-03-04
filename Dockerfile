FROM golang:1.23-alpine AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -o operator ./cmd/operator/

FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/operator .

USER 65532:65532

ENTRYPOINT ["/operator"]
