#!/usr/bin/env python3
"""Real-cluster characterization. All API calls require our private kind context.
No assertions use Claw's claimed success alone: inspect Pod UID/image/revision.
The runtime process is real; the additional probe/channel are test-only fixtures.
"""
import datetime
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(subprocess.check_output(['git', 'rev-parse', '--show-toplevel'], text=True).strip())
TASK = ROOT.parent
OUT = ROOT / 'docs/demo/lifecycle-fixed-20261013'
CLUSTER = 'k8s4claw-fixed-20261013'
NS = 'lifecycle-test'
IMAGE = 'ghcr.io/prismer-ai/k8s4claw-openclaw'
os.environ['KUBECONFIG'] = str(TASK / '.state/kind-fixed.config')
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

def prepare_health_check(tag, timeout='50s'):
    # Registry discovery is excluded: seed the persisted state normally written
    # by initiation, using a verified actual previous-good workload reference.
    previous = obj('sts', 'agent')['spec']['template']['spec']['containers'][0]['image']
    assert healthy(previous.rsplit(':', 1)[1])
    patch('claw', 'agent', {'spec': {'autoUpdate': {'enabled': False, 'schedule': '0 0 1 1 *', 'healthTimeout': timeout, 'maxRollbacks': 3}}})
    now = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='microseconds').replace('+00:00', 'Z')
    patch('claw', 'agent', {'status': {'autoUpdate': {'currentVersion': previous.rsplit(':', 1)[1], 'availableVersion': tag, 'lastCheck': now}}}, status=True)
    patch('claw', 'agent', {'metadata': {'annotations': {'claw.prismer.ai/target-image': IMAGE + ':' + tag,
          'claw.prismer.ai/previous-image': previous,
          'claw.prismer.ai/update-phase': 'HealthCheck', 'claw.prismer.ai/update-started': now}}})

def enable_health_check():
    patch('claw', 'agent', {'spec': {'autoUpdate': {'enabled': True}}})

def phase_finished():
    return obj('claw', 'agent')['metadata'].get('annotations', {}).get('claw.prismer.ai/update-phase') is None

def broken():
    p=obj('pod','agent-0')
    return p['spec']['containers'][0]['image']==IMAGE+':0.0.3' and any(c.get('state',{}).get('waiting',{}).get('reason') in ['RunContainerError','CrashLoopBackOff','CreateContainerError'] for c in p.get('status',{}).get('containerStatuses',[]))

def main():
    print('REAL FIXED SOURCE KIND TEST; no init environment workaround; no paid inference.', flush=True)
    command(['git','rev-parse','HEAD'])
    assert command(K+['config','current-context'],quiet=True).strip()=='kind-'+CLUSTER
    nodes=json.loads(command(K+['get','nodes','-o','json'],quiet=True))
    assert [n['metadata']['name'] for n in nodes['items']]==[CLUSTER+'-control-plane']
    tags=['claw-operator-fixed:test','claw-init-fixed:test','claw-ipcbus-fixed:test','claw-channel-probe:test']+[IMAGE+':'+t for t in ['0.0.1','0.0.2','0.0.3','latest']]
    command([str(TASK/'.tools/bin/kind'),'load','docker-image','--name',CLUSTER]+tags)
    # Remove only the earlier failed test namespace, if present.
    k('delete','namespace',NS,'--ignore-not-found','--timeout=60s')
    k('create','namespace',NS)
    command(K+['apply','-f',str(ROOT/'config/crd/bases')])
    k('wait','--for=condition=Established','crd/claws.claw.prismer.ai','--timeout=30s')
    apply({'apiVersion':'v1','kind':'ServiceAccount','metadata':{'name':'operator','namespace':NS}})
    # Repository RBAC, not cluster-admin; tests exercise product permissions too.
    command(K+['apply','-f',str(ROOT/'config/rbac/role.yaml')])
    apply({'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRoleBinding','metadata':{'name':'fixed-test-operator'},
           'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'ClusterRole','name':'k8s4claw-operator'},
           'subjects':[{'kind':'ServiceAccount','name':'operator','namespace':NS}]})
    command(K+['apply','-f',str(ROOT/'config/rbac/clawops_role.yaml')])
    apply({'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRoleBinding','metadata':{'name':'fixed-test-clawops'},
           'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'ClusterRole','name':'k8s4claw-clawops'},
           'subjects':[{'kind':'ServiceAccount','name':'operator','namespace':NS}]})
    apply({'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'operator','namespace':NS},
           'spec':{'replicas':1,'selector':{'matchLabels':{'app':'lifecycle-operator'}},
           'template':{'metadata':{'labels':{'app':'lifecycle-operator'}},'spec':{'serviceAccountName':'operator',
           'containers':[{'name':'operator','image':'claw-operator-fixed:test','imagePullPolicy':'Never',
             'args':['--disable-webhooks','--leader-elect=false'],
             'resources':{'requests':{'cpu':'100m','memory':'128Mi'},'limits':{'memory':'512Mi'}}}]}}}})
    k('rollout','status','deployment/operator','--timeout=90s')
    apply({'apiVersion':'v1','kind':'Secret','metadata':{'name':'test-only','namespace':NS},
           'stringData':{'ANTHROPIC_API_KEY':'invalid-test-only-not-a-secret','ANTHROPIC_BASE_URL':'http://127.0.0.1:9'}})
    apply({'apiVersion':'claw.prismer.ai/v1alpha1','kind':'ClawChannel','metadata':{'name':'probe','namespace':NS},
           'spec':{'type':'custom','mode':'bidirectional','sidecar':{'image':'claw-channel-probe:test'}}})
    apply({'apiVersion':'claw.prismer.ai/v1alpha1','kind':'Claw','metadata':{'name':'agent','namespace':NS,
           'annotations':{'claw.prismer.ai/image-pull-policy':'Never','claw.prismer.ai/target-image':IMAGE+':0.0.1'}},
           'spec':{'runtime':'openclaw','config':{'testMarker':'fixed-contract'},
             'channels':[{'name':'probe','mode':'bidirectional'}],
             'credentials':{'secretRef':{'name':'test-only'}},
             'resources':{'requests':{'cpu':'100m','memory':'128Mi'},'limits':{'cpu':'500m','memory':'512Mi'}}}})
    ok=wait(lambda:healthy('0.0.1'),150)
    snapshot('03-startup')
    k('logs','agent-0','-c','claw-init',check=False)
    logs=k('logs','agent-0','-c','runtime',check=False)
    k('logs','agent-0','-c','ipc-bus',check=False)
    result('real runtime startup','PASS' if ok and '"mock":false' in logs else 'FAIL','Unmodified real-source gateway binary in scratch test image, no init ENV workaround, placeholder credentials and no inference.')
    assert ok
    probe=k('exec','agent-0','-c','runtime','--','/probe')
    result('init/config/workspace and IPC contract','PASS' if probe.count('PASS ')==5 else 'FAIL',probe.strip())
    assert probe.count('PASS ')==5
    old_uid=obj('pod','agent-0')['metadata']['uid']
    prepare_health_check('0.0.2','120s')
    enable_health_check()
    ok=wait(lambda:phase_finished() and healthy('0.0.2'),160)
    snapshot('04-normal-upgrade')
    au=obj('claw','agent')['status']['autoUpdate']
    result('normal upgrade health gate','PASS' if ok and obj('pod','agent-0')['metadata']['uid']!=old_uid and au.get('currentVersion')=='0.0.2' and len([x for x in au.get('versionHistory',[]) if x['status']=='Healthy'])==1 else 'FAIL','Same real binary under tags 0.0.1 -> 0.0.2. Actual Pod replacement, target/revision convergence and durable Healthy history required. OCI discovery excluded.')
    assert ok
    for number,interrupt in [(1,False),(2,True)]:
        prepare_health_check('0.0.3','70s' if interrupt else '50s')
        assert wait(broken,90), 'bad image must actually fail container start'
        enable_health_check()
        snapshot('05-bad-active' if not interrupt else '07-before-operator-interruption')
        if interrupt:
            op=json.loads(k('get','pods','-l','app=lifecycle-operator','-o','json',quiet=True))['items'][0]
            before=op['metadata']['uid']
            assert obj('claw','agent')['metadata']['annotations']['claw.prismer.ai/update-phase']=='HealthCheck'
            print('Operator UID before interruption: '+before,flush=True)
            k('delete','pod',op['metadata']['name'],'--grace-period=0','--force','--wait=false')
            assert wait(lambda:any(p['metadata']['uid']!=before and p.get('status',{}).get('phase')=='Running' for p in json.loads(k('get','pods','-l','app=lifecycle-operator','-o','json',quiet=True))['items']))
        ok=wait(lambda:phase_finished() and healthy('0.0.2'),180)
        snapshot('06-rollback' if not interrupt else '08-operator-recovery')
        au=obj('claw','agent')['status']['autoUpdate']
        passed=ok and au.get('rollbackCount')==number and len([x for x in au.get('versionHistory',[]) if x['status']=='RolledBack'])==number
        if interrupt:
            newop=json.loads(k('get','pods','-l','app=lifecycle-operator','-o','json',quiet=True))['items'][0]
            print('Operator UID after recovery: '+newop['metadata']['uid'],flush=True)
            passed=passed and newop['metadata']['uid']!=before
        result('operator interruption and rollback recovery' if interrupt else 'bad version automatic previous-good recovery','PASS' if passed else 'FAIL','Exact previous-good :0.0.2 healthy; failed Pod recovered by product controller without harness deletion. Rollback count/history expected '+str(number)+'.')
        assert passed
    print('Observe 35 seconds of subsequent reconciles: outcome must not be counted twice.',flush=True)
    time.sleep(35)
    au=obj('claw','agent')['status']['autoUpdate']
    snapshot('09-stable-followup')
    result('durable accounting after followup','PASS' if au['rollbackCount']==2 and len(au['versionHistory'])==3 and phase_finished() and healthy('0.0.2') else 'FAIL','One Healthy outcome plus two distinct failed update attempts; no duplicate count/history.')
    k('exec','agent-0','-c','runtime','--','/probe')
    k('logs','deployment/operator','--tail=100')
    k('get','events','--sort-by=.metadata.creationTimestamp')
    print('Scenarios complete; review evidence then delete only this isolated cluster.',flush=True)

if __name__=='__main__':
    try: main()
    except Exception as e:
        result('harness completion','FAIL',str(e))
        k('get','pods','-o','wide',check=False)
        k('logs','deployment/operator','--tail=50',check=False)
        raise
