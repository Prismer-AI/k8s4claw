#!/usr/bin/env python3
"""Record a real child PTY to asciicast v2 and a transcript (no desktop capture).

Usage: python3 record.py output-prefix command [args...]
Only child output is recorded; shell history, desktop and input are not captured.
"""
import codecs
import errno
import fcntl
import json
import os
import pty
import struct
import subprocess
import sys
import termios
import time

prefix, *command = sys.argv[1:]
master, slave = pty.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 36, 120, 0, 0))
start = time.monotonic()
env = dict(os.environ, TERM="dumb", NO_COLOR="1")
proc = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=slave,
                        stderr=slave, env=env, close_fds=True)
os.close(slave)
decoder = codecs.getincrementaldecoder("utf-8")("replace")
with open(prefix + ".cast", "w") as cast, open(prefix + ".txt", "w") as log:
    cast.write(json.dumps({"version": 2, "width": 120, "height": 36,
                          "timestamp": int(time.time()),
                          "title": "k8s4claw real isolated-kind test terminal",
                          "env": {"TERM": "dumb"}}) + "\n")
    while True:
        try:
            data = os.read(master, 65536)
        except OSError as e:
            if e.errno == errno.EIO:
                break
            raise
        if not data:
            break
        output = decoder.decode(data)
        cast.write(json.dumps([round(time.monotonic() - start, 6), "o", output]) + "\n")
        cast.flush()
        log.write(output)
        log.flush()
        sys.stdout.write(output)
        sys.stdout.flush()
    rc = proc.wait()
    end = "\r\n[recorder] child exit code: %d\r\n" % rc
    cast.write(json.dumps([round(time.monotonic() - start, 6), "o", end]) + "\n")
    log.write(end)
os.close(master)
sys.exit(rc)
