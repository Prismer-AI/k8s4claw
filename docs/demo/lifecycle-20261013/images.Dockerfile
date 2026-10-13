# Test packaging only: real binaries from the tested commit, no mock runtime.
# scratch avoids changing external runtime-image tags and paid/API traffic.
FROM scratch AS operator-stock
COPY operator /operator
USER 65532:65532
ENTRYPOINT ["/operator"]
FROM scratch AS operator-diagnostic
COPY operator-diagnostic /operator
USER 65532:65532
ENTRYPOINT ["/operator"]
FROM scratch AS init-stock
COPY claw-init /claw-init
USER 65532:65532
ENTRYPOINT ["/claw-init"]
FROM init-stock AS init-diagnostic
# Explicit test-only workaround for the stock init contract failure.
ENV CONFIG_SOURCE=/etc/claw CONFIG_DEST=/tmp/config WORKSPACE_PATH=/tmp/workspace
FROM scratch AS ipcbus
COPY claw-ipcbus /claw-ipcbus
USER 65532:65532
ENTRYPOINT ["/claw-ipcbus"]
FROM scratch AS runtime
COPY claw-entrypoint /usr/bin/claw-entrypoint
USER 65532:65532
ENTRYPOINT ["/usr/bin/claw-entrypoint"]
FROM scratch AS bad-runtime
# The operator command /usr/bin/claw-entrypoint intentionally does not exist.
COPY claw-entrypoint /deliberately-wrong-path
USER 65532:65532
