FROM golang:1.25-alpine AS builder
WORKDIR /workspace
COPY go.mod ./
COPY main.go .
RUN CGO_ENABLED=0 go build -o /mock-runtime .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /mock-runtime /mock-runtime
# Symlinks so the same binary works as any entrypoint
COPY --from=builder /mock-runtime /claw-init
COPY --from=builder /mock-runtime /usr/bin/claw-entrypoint
COPY --from=builder /mock-runtime /claw-ipcbus
COPY --from=builder /mock-runtime /claw-channel-slack
COPY --from=builder /mock-runtime /claw-channel-discord
COPY --from=builder /mock-runtime /claw-channel-webhook
USER 65532:65532
ENTRYPOINT ["/mock-runtime"]
