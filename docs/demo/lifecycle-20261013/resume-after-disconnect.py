#!/usr/bin/env python3
"""Continue this recorded run after the Mac executor disconnected.
Requires the first four recorded results and the restored :0.0.2 fixture.
For a fresh run, use scenarios.py instead.
"""
import json
import scenarios as s

s.results.extend(json.loads((s.OUT / 'results.json').read_text()))
assert len(s.results) == 4
assert s.healthy('0.0.2')
print('Resuming after executor disconnect; previous evidence files are preserved.', flush=True)
s.remaining()
