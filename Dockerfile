FROM golang:1.26.9-alpine AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

ARG VERSION=dev
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a \
    -ldflags "-X github.com/Prismer-AI/k8s4claw/internal/runtime.InitContainerImage=ghcr.io/prismer-ai/claw-init:${VERSION}" \
    -o operator ./cmd/operator/

FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/operator .

USER 65532:65532

ENTRYPOINT ["/operator"]
