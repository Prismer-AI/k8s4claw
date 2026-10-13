#!/usr/bin/env python3
"""Redact host path prefix in completed captured evidence, preserving PTY timing.
Run only after all recordings have stopped. Raw backups remain outside Git.
"""
from pathlib import Path
import json
import re
import shutil
import subprocess
root=Path(subprocess.check_output(['git','rev-parse','--show-toplevel'],text=True).strip())
task=root.parent
out=root/'docs/demo/lifecycle-fixed-20261013'
backup=task/'.state/private-fixed-recordings'
backup.mkdir(parents=True,exist_ok=True)
count=0
for p in out.iterdir():
    if p.suffix not in ['.cast','.txt','.json'] or p.name.endswith('.go.txt'):
        continue
    if not (backup/p.name).exists(): shutil.copy2(p,backup/p.name)
    text=p.read_text().replace(str(task),'$TASK')
    p.write_text(text)
    if p.suffix=='.json': json.loads(text)
    if p.suffix=='.cast':
        for line in text.splitlines(): json.loads(line)
    patterns=[r'/Users/[^/]+/',r'-----BEGIN .*PRIVATE KEY-----',r'gh[pousr]_[A-Za-z0-9]{20,}',r'sk-[A-Za-z0-9]{24,}',r'Bearer [A-Za-z0-9._-]{20,}',r'client-key-data:',r'client-certificate-data:']
    for pattern in patterns:
        if re.search(pattern,text): raise SystemExit('REVIEW REQUIRED: '+p.name+' '+pattern)
    count+=1
print('Sanitized and scanned',count,'evidence files; only task path prefixes replaced.')
