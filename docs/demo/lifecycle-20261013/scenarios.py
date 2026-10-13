#!/usr/bin/env python3
"""Real-cluster characterization. All API calls require our private kind context.
No assertions use Claw's claimed success alone: inspect Pod UID/image/revision.
The init diagnostic image is a disclosed test fixture, not a product fix.
"""
import datetime
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(subprocess.check_output(['git', 'rev-parse', '--show-toplevel'], text=True).strip())
TASK = ROOT.parent
OUT = ROOT / 'docs/demo/lifecycle-20261013'
CLUSTER = 'k8s4claw-lifecycle-20261013'
NS = 'lifecycle-test'
IMAGE = 'ghcr.io/prismer-ai/k8s4claw-openclaw'
os.environ['KUBECONFIG'] = str(TASK / '.state/kind-lifecycle.config')
os.environ['DOCKER_CONTEXT'] = 'colima'
K = [str(TASK / '.tools/bin/kubectl'), '--context', 'kind-' + CLUSTER]
results = []

def command(args, data=None, check=True, quiet=False):
    if not quiet:
        print('$ ' + ' '.join(str(x) for x in args).replace(str(TASK), '$TASK'), flush=True)
    p = subprocess.run(args, input=None if data is None else json.dumps(data),
                       text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if not quiet or p.returncode:
        print(p.stdout, end='', flush=True)
    if check and p.returncode:
        raise RuntimeError('command exit %s' % p.returncode)
    return p.stdout

def k(*args, **kw):
    return command(K + ['-n', NS] + list(args), **kw)

def obj(kind, name):
    return json.loads(k('get', kind, name, '-o', 'json', quiet=True))

def apply(data):
    return k('apply', '-f', '-', data=data)

def patch(kind, name, data, status=False):
    args = ['patch', kind, name, '--type=merge', '-p', json.dumps(data)]
    if status:
        args += ['--subresource=status']
    k(*args)

def wait(predicate, timeout=120):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            if predicate():
                return True
        except (RuntimeError, KeyError, IndexError):
            pass
        time.sleep(2)
    return False

def result(name, status, detail):
    entry = dict(scenario=name, status=status, detail=detail)
    results.append(entry)
    (OUT / 'results.json').write_text(json.dumps(results, indent=2) + '\n')
    print('\nRESULT ' + json.dumps(entry) + '\n', flush=True)

def snapshot(label):
    claw = obj('claw', 'agent')
    sts = obj('sts', 'agent')
    pod = obj('pod', 'agent-0')
    view = {'label': label, 'utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'claw_annotations': claw['metadata'].get('annotations', {}),
            'claw_status': claw.get('status', {}),
            'sts_generation': sts['metadata']['generation'], 'sts_status': sts.get('status', {}),
            'sts_image': sts['spec']['template']['spec']['containers'][0]['image'],
            'pod_uid': pod['metadata']['uid'], 'pod_revision': pod['metadata']['labels'].get('controller-revision-hash'),
            'pod_images': [x['image'] for x in pod['spec']['containers']],
            'pod_status': pod.get('status', {})}
    view['claw_annotations'].pop('kubectl.kubernetes.io/last-applied-configuration', None)
    (OUT / (label + '.json')).write_text(json.dumps(view, indent=2) + '\n')
    print(json.dumps({key: view[key] for key in ['label', 'sts_generation', 'sts_status', 'sts_image', 'pod_uid', 'pod_revision', 'pod_images']}, indent=2), flush=True)
    k('get', 'pods')
    return view

def healthy(tag):
    sts, pod = obj('sts', 'agent'), obj('pod', 'agent-0')
    s = sts.get('status', {})
    return (s.get('observedGeneration', 0) >= sts['metadata']['generation'] and
            s.get('currentRevision') == s.get('updateRevision') and s.get('readyReplicas') == 1 and
            pod['spec']['containers'][0]['image'] == IMAGE + ':' + tag and
            pod['metadata']['labels'].get('controller-revision-hash') == s.get('updateRevision') and
            any(c['type'] == 'Ready' and c['status'] == 'True' for c in pod.get('status', {}).get('conditions', [])))

def prepare_health_check(tag, timeout='45s'):
    # Seed the real controller's persisted HealthCheck state. Registry discovery
    # is deliberately excluded; it is hardcoded to upstream image repositories.
    patch('claw', 'agent', {'spec': {'autoUpdate': {'enabled': False, 'schedule': '0 0 1 1 *', 'healthTimeout': timeout, 'maxRollbacks': 3}}})
    now = datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    patch('claw', 'agent', {'status': {'autoUpdate': {'currentVersion': '0.0.2', 'availableVersion': tag, 'lastCheck': now}}}, status=True)
    patch('claw', 'agent', {'metadata': {'annotations': {'claw.prismer.ai/target-image': IMAGE + ':' + tag,
          'claw.prismer.ai/update-phase': 'HealthCheck', 'claw.prismer.ai/update-started': now}}})

def enable_health_check():
    patch('claw', 'agent', {'spec': {'autoUpdate': {'enabled': True}}})

def phase_finished():
    return obj('claw', 'agent')['metadata'].get('annotations', {}).get('claw.prismer.ai/update-phase') is None

def main():
    print('REAL KIND TEST. Baseline stock source; subsequent init fixture is diagnostic only.', flush=True)
    command(['git', 'rev-parse', 'HEAD'])
    assert command(K + ['config', 'current-context'], quiet=True).strip() == 'kind-' + CLUSTER
    nodes = json.loads(command(K + ['get', 'nodes', '-o', 'json'], quiet=True))
    assert [n['metadata']['name'] for n in nodes['items']] == [CLUSTER + '-control-plane']
    tags = ['claw-operator-test:stock', 'claw-operator-test:diagnostic', 'claw-init-test:stock',
            'claw-init-test:diagnostic', 'claw-ipcbus-test:stock'] + [IMAGE + ':' + t for t in ['0.0.1', '0.0.2', '0.0.3', 'latest']]
    command([str(TASK / '.tools/bin/kind'), 'load', 'docker-image', '--name', CLUSTER] + tags)
    k('create', 'namespace', NS)
    command(K + ['apply', '-f', str(ROOT / 'config/crd/bases')])
    k('wait', '--for=condition=Established', 'crd/claws.claw.prismer.ai', '--timeout=30s')
    apply({'apiVersion': 'v1', 'kind': 'ServiceAccount', 'metadata': {'name': 'operator', 'namespace': NS}})
    apply({'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'ClusterRoleBinding',
           'metadata': {'name': 'lifecycle-test-operator'},
           'roleRef': {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'ClusterRole', 'name': 'cluster-admin'},
           'subjects': [{'kind': 'ServiceAccount', 'name': 'operator', 'namespace': NS}]})
    apply({'apiVersion': 'apps/v1', 'kind': 'Deployment', 'metadata': {'name': 'operator', 'namespace': NS},
           'spec': {'replicas': 1, 'selector': {'matchLabels': {'app': 'lifecycle-operator'}},
           'template': {'metadata': {'labels': {'app': 'lifecycle-operator'}}, 'spec': {'serviceAccountName': 'operator',
           'containers': [{'name': 'operator', 'image': 'claw-operator-test:stock', 'imagePullPolicy': 'Never',
                           'args': ['--disable-webhooks', '--leader-elect=false'],
                           'resources': {'requests': {'cpu': '100m', 'memory': '128Mi'}, 'limits': {'memory': '512Mi'}}}]}}}})
    k('rollout', 'status', 'deployment/operator', '--timeout=90s')
    apply({'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'test-only', 'namespace': NS},
           'stringData': {'ANTHROPIC_API_KEY': 'invalid-test-only-not-a-secret', 'ANTHROPIC_BASE_URL': 'http://127.0.0.1:9'}})
    apply({'apiVersion': 'claw.prismer.ai/v1alpha1', 'kind': 'Claw', 'metadata': {'name': 'agent', 'namespace': NS,
           'annotations': {'claw.prismer.ai/image-pull-policy': 'Never', 'claw.prismer.ai/target-image': IMAGE + ':0.0.1'}},
           'spec': {'runtime': 'openclaw', 'credentials': {'secretRef': {'name': 'test-only'}},
                    'resources': {'requests': {'cpu': '100m', 'memory': '128Mi'}, 'limits': {'cpu': '500m', 'memory': '512Mi'}}}})
    assert wait(lambda: obj('pod', 'agent-0').get('status', {}).get('initContainerStatuses', [{}])[0].get('restartCount', 0) > 0, 90)
    snapshot('03-stock-startup')
    logs = k('logs', 'agent-0', '-c', 'claw-init', check=False)
    result('stock real runtime startup', 'FAIL', logs.strip())

    print('DIAGNOSTIC ONLY: real init binary with CONFIG_SOURCE=/etc/claw, CONFIG_DEST=/tmp/config, WORKSPACE_PATH=/tmp/workspace.', flush=True)
    k('set', 'image', 'deployment/operator', 'operator=claw-operator-test:diagnostic')
    k('rollout', 'status', 'deployment/operator', '--timeout=90s')
    assert wait(lambda: obj('sts', 'agent')['spec']['template']['spec']['initContainers'][0]['image'] == 'claw-init-test:diagnostic')
    k('delete', 'pod', 'agent-0', '--wait=true')
    ok = wait(lambda: healthy('0.0.1'))
    snapshot('04-diagnostic-startup')
    logs = k('logs', 'agent-0', '-c', 'runtime', check=False)
    result('diagnostic real runtime startup', 'PASS' if ok and '"mock":false' in logs else 'FAIL',
           'Real OpenClaw process; init environment workaround; invalid placeholder key, no inference requests.')
    assert ok

    # A normal spec/annotation-driven rollout, checked against actual Pod revision.
    old_uid = obj('pod', 'agent-0')['metadata']['uid']
    patch('claw', 'agent', {'metadata': {'annotations': {'claw.prismer.ai/target-image': IMAGE + ':0.0.2'}}})
    ok = wait(lambda: healthy('0.0.2'))
    snapshot('05-normal-upgrade')
    result('diagnostic normal image rollout', 'PASS' if ok and obj('pod', 'agent-0')['metadata']['uid'] != old_uid else 'FAIL',
           '0.0.1 -> 0.0.2, same real-source binary under two tags; new Pod UID and converged revisions required. OCI discovery not tested.')

    prepare_health_check('0.0.3')
    assert wait(lambda: obj('pod', 'agent-0')['spec']['containers'][0]['image'].endswith(':0.0.3') and
                obj('sts', 'agent').get('status', {}).get('readyReplicas', 0) == 0)
    enable_health_check()
    snapshot('06-bad-version-active')
    completed = wait(phase_finished, 100)
    snapshot('07-bad-version-rollback')
    target = obj('sts', 'agent')['spec']['template']['spec']['containers'][0]['image']
    result('bad version returns to previous-good 0.0.2', 'PASS' if completed and target == IMAGE + ':0.0.2' and healthy('0.0.2') else 'FAIL',
           'After real failed container start, rollback StatefulSet target = ' + target + '; expected exact previous-good :0.0.2.')
    # Recover fixture explicitly, preserving the failed rollback evidence first.
    patch('claw', 'agent', {'spec': {'autoUpdate': {'enabled': False}}, 'metadata': {'annotations': {'claw.prismer.ai/target-image': IMAGE + ':0.0.2'}}})
    k('delete', 'pod', 'agent-0', '--wait=true')
    assert wait(lambda: healthy('0.0.2'))

    remaining()

def remaining():
    assert wait(lambda: healthy('0.0.2'))
    old_rollbacks = obj('claw', 'agent').get('status', {}).get('autoUpdate', {}).get('rollbackCount', 0)
    prepare_health_check('0.0.3', '75s')
    assert wait(lambda: obj('pod', 'agent-0')['spec']['containers'][0]['image'].endswith(':0.0.3') and obj('sts', 'agent').get('status', {}).get('readyReplicas', 0) == 0)
    enable_health_check()
    op = json.loads(k('get', 'pods', '-l', 'app=lifecycle-operator', '-o', 'json', quiet=True))['items'][0]
    before = op['metadata']['uid']
    snapshot('08-before-operator-interruption')
    assert not phase_finished(), 'Must interrupt while HealthCheck is persisted'
    print('Operator UID before interruption: ' + before, flush=True)
    k('delete', 'pod', op['metadata']['name'], '--grace-period=0', '--force', '--wait=false')
    assert wait(lambda: any(p['metadata']['uid'] != before and p.get('status', {}).get('phase') == 'Running' for p in json.loads(k('get', 'pods', '-l', 'app=lifecycle-operator', '-o', 'json', quiet=True))['items']))
    completed = wait(phase_finished, 120)
    snapshot('09-after-operator-recovery')
    new_op = json.loads(k('get', 'pods', '-l', 'app=lifecycle-operator', '-o', 'json', quiet=True))['items'][0]
    print('Operator UID after recovery: ' + new_op['metadata']['uid'], flush=True)
    au = obj('claw', 'agent').get('status', {}).get('autoUpdate', {})
    result('operator interruption resumes persisted health check', 'PASS' if completed and new_op['metadata']['uid'] != before and au.get('rollbackCount') == old_rollbacks + 1 and '0.0.3' in au.get('failedVersions', []) else 'FAIL',
           'Operator Pod force-deleted during HealthCheck; Deployment recreated it; controller resumed timeout/rollback. This does not fix wrong rollback target.')
    k('logs', 'deployment/operator', '--tail=100')
    k('get', 'events', '--sort-by=.metadata.creationTimestamp')
    # Independent probe of the exact IPC path contract used by the operator.
    apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'ipc-contract', 'namespace': NS},
           'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False,
           'securityContext': {'runAsUser': 1000, 'runAsGroup': 1000, 'fsGroup': 1000},
           'volumes': [{'name': 'ipc', 'emptyDir': {}}, {'name': 'wal', 'emptyDir': {}}],
           'containers': [{'name': 'ipc', 'image': 'claw-ipcbus-test:stock', 'imagePullPolicy': 'Never',
           'securityContext': {'readOnlyRootFilesystem': True},
           'env': [{'name': 'IPC_SOCKET_PATH', 'value': '/var/run/claw'}, {'name': 'WAL_DIR', 'value': '/var/run/claw/wal'}],
           'volumeMounts': [{'name': 'ipc', 'mountPath': '/var/run/claw'}, {'name': 'wal', 'mountPath': '/var/run/claw/wal'}]}]}})
    ended = wait(lambda: obj('pod', 'ipc-contract').get('status', {}).get('phase') in ['Failed', 'Succeeded'], 45)
    logs = k('logs', 'ipc-contract', check=False)
    result('stock IPC socket contract (standalone probe)', 'FAIL' if ended and obj('pod', 'ipc-contract')['status']['phase'] == 'Failed' else 'INCONCLUSIVE', logs.strip())
    print('All characterization scenarios complete. Cluster retained until evidence review/explicit cleanup.', flush=True)

if __name__ == '__main__':
    main()
