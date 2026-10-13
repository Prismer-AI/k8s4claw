# Real source binaries in scratch test packaging, not released images.
FROM scratch AS operator
COPY operator /operator
USER 65532:65532
ENTRYPOINT ["/operator"]
FROM scratch AS init
COPY claw-init /claw-init
USER 65532:65532
ENTRYPOINT ["/claw-init"]
FROM scratch AS ipcbus
COPY claw-ipcbus /claw-ipcbus
USER 65532:65532
ENTRYPOINT ["/claw-ipcbus"]
FROM scratch AS channel
COPY probe /probe
USER 65532:65532
ENTRYPOINT ["/probe", "hold"]
FROM scratch AS runtime
COPY claw-entrypoint /usr/bin/claw-entrypoint
COPY probe /probe
USER 65532:65532
ENTRYPOINT ["/usr/bin/claw-entrypoint"]
FROM scratch AS bad-runtime
# Deliberately invalid executable location: real container start failure.
COPY claw-entrypoint /deliberately-wrong-path
USER 65532:65532
