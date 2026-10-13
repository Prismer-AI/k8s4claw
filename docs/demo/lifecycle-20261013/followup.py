#!/usr/bin/env python3
import json
import scenarios as s

print('Read-only follow-up after resumed run finished; check durable rollback accounting.', flush=True)
view = s.snapshot('11-recovery-followup')
print(json.dumps(view['claw_status'].get('autoUpdate'), indent=2), flush=True)
s.k('logs', 'deployment/operator', '--tail=35')
s.k('get', 'pod', 'ipc-contract', '-o', 'jsonpath={.status.phase}')
print()
