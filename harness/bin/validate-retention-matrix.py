#!/usr/bin/env python3
"""Isolated pinned Nextcloud/AppAPI fixtures for D-803. No retained stack reset.

Uses disposable Compose project names, ports and manually registered ExApps.
Requires IMAGE_REF built from the checkout; runs the installed DAV/lifecycle probes.
"""
import base64, json, os, pathlib, secrets, subprocess, time, urllib.request, urllib.error, xml.etree.ElementTree as ET
ROOT = pathlib.Path(__file__).resolve().parents[2]
IMAGE = os.environ['IMAGE_REF']
LOCK = json.loads((ROOT/'ci/nextcloud-compatibility.json').read_text())
manifest = ET.parse(ROOT/'appinfo/info.xml').getroot()
APP_ID, VERSION = manifest.findtext('id'), manifest.findtext('version')
routes = [{'url': r.findtext('url'), 'verb': r.findtext('verb'), 'access_level': {'PUBLIC':0,'USER':1,'ADMIN':2}[r.findtext('access_level')]} for r in manifest.findall('./external-app/routes/route')]
selected = os.environ.get('RETENTION_BASELINES', 'nc33,nc34,nc35').split(',')
for baseline in LOCK['baselines']:
    if baseline['id'] not in selected: continue
    run_id = 'retention-' + baseline['id'] + '-' + secrets.token_hex(4)
    container = run_id + '-exapp'
    port = 28100 + int(baseline['id'][2:])
    env = dict(os.environ, NEXTCLOUD_IMAGE=baseline['images']['nextcloud'], COMPAT_DB_IMAGE=baseline['images']['db'], NEXTCLOUD_HOST_PORT=str(port))
    compose = ['docker','compose','-p',run_id,'-f',str(ROOT/'harness/compose.yml')]
    def run(args, **kwargs):
        result = subprocess.run(args,env=env,check=False,**kwargs)
        if result.returncode: raise RuntimeError('Fixture command failed; arguments omitted because registration contains credentials')
        return result
    def occ(*args): return run(compose+['exec','-T','-u','www-data','nextcloud','php','occ',*args],stdout=subprocess.PIPE,stderr=subprocess.STDOUT).stdout
    print('Starting', baseline['id'], baseline['nextcloud_version'], flush=True)
    try:
        run(compose+['up','-d','db','nextcloud'])
        deadline = time.monotonic()+180
        while True:
            try:
                if json.loads(urllib.request.urlopen(f'http://127.0.0.1:{port}/status.php',timeout=5).read())['installed']: break
            except Exception: pass
            if time.monotonic()>deadline: raise RuntimeError('Nextcloud initialization timed out')
            time.sleep(2)
        occ('app:enable','app_api')
        occ('app:enable','circles')
        occ('app:enable','serverinfo')
        run(compose+['exec','-T','-e','OC_PASS='+secrets.token_hex(24),'-u','www-data','nextcloud','php','occ','user:add','--password-from-env','cassini'],stdout=subprocess.DEVNULL)
        occ('config:system:set','allow_local_remote_servers','--type=boolean','--value=true')
        observed=json.loads(occ('status','--output=json'))
        assert observed['versionstring']==baseline['nextcloud_version'], observed['versionstring']
        secret=secrets.token_hex(24)
        occ('app_api:daemon:register','retention_manual','Retention fixture','manual-install','http',container,'http://nextcloud')
        def start_exapp(secret, version):
            run(['docker','run','-d','--name',container,'--network',run_id+'_default',
                 '-e','APP_HOST=0.0.0.0','-e','APP_PORT=8080','-e','APP_ID='+APP_ID,'-e','APP_VERSION='+version,
                 '-e','APP_SECRET='+secret,'-e','AA_VERSION='+baseline['apps']['app_api']['version'],
                 '-e','NEXTCLOUD_URL=http://nextcloud','-e','CASSINI_APPAPI_REQUIRED=true',
                 '--entrypoint','/usr/local/bin/cassini-operator',IMAGE],stdout=subprocess.DEVNULL)
            time.sleep(3)
        old_version = '0.2.0-beta.7'
        start_exapp(secret, old_version)
        reduced = [r for r in routes if '/preview' not in r['url'] and '/operations' not in r['url'] and '\\/preview' not in r['url'] and '\\/operations' not in r['url']]
        assert len(reduced) == len(routes)-2
        info=dict(appid=APP_ID,name='Cassini',daemon_config_name='retention_manual',version=old_version,secret=secret,port=8080,protocol='http',system_app=0,routes=reduced)
        result=occ('app_api:app:register',APP_ID,'retention_manual','--json-info',json.dumps(info),'--force-scopes','--wait-finish')
        assert b'heartbeat check failed' not in result
        occ('app_api:app:disable',APP_ID)
        occ('app_api:app:enable',APP_ID)
        proxy=f'http://127.0.0.1:{port}/index.php/apps/app_api/proxy/{APP_ID}/operator/storage/retention'
        def api(suffix='', data=None, expected=200):
            headers={'Authorization':'Basic '+base64.b64encode(b'admin:admin').decode(),'OCS-APIRequest':'true'}
            if data is not None: headers['Content-Type']='application/json'
            request=urllib.request.Request(proxy+suffix,headers=headers,data=None if data is None else json.dumps(data).encode())
            try: response=urllib.request.urlopen(request,timeout=60)
            except urllib.error.HTTPError as error: response=error
            with response:
                body=response.read()
                assert response.code==expected, f'Installed route {suffix} returned {response.code}, expected {expected}'
                return json.loads(body) if response.code==200 else None
        settings=api()
        api('/operations',expected=404)
        info.update(version=VERSION,routes=routes)
        # Manual deployment cannot rotate the running container for AppAPI.
        # Update its route table normally, then restart with the rotated secret.
        subprocess.run(compose+['exec','-T','-u','www-data','nextcloud','php','occ','app_api:app:update',APP_ID,'--json-info',json.dumps(info),'--wait-finish'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        secret=run(compose+['exec','-T','db','psql','-U','nc','-d','nextcloud','-tAc',"SELECT secret FROM oc_ex_apps WHERE appid='"+APP_ID+"'"],stdout=subprocess.PIPE).stdout.decode().strip()
        assert secret
        run(['docker','rm','-f',container],stdout=subprocess.DEVNULL)
        start_exapp(secret,VERSION)
        occ('app_api:app:disable',APP_ID)
        occ('app_api:app:enable',APP_ID)
        settings=api()
        assert settings['version']==4 and settings['nextcloud']['recordings']['forever'] and settings['nextcloud']['transcriptions']['forever']
        assert api('/operations')['operations']==[]
        preview=api('/preview',data=settings)
        assert preview['convert']==preview['retire']==0
        assert api()['revision']==settings['revision']
        print('PASSED',baseline['id'],'installed route upgrade, defaults and non-mutating preview',flush=True)
        env.update(RETENTION_PROBE_URL=f'http://127.0.0.1:{port}', CASSINI_EXAPP_CONTAINER=container)
        run(['python3',str(ROOT/'harness/bin/validate-retention-dav.py')])
        run([str(ROOT/'harness/bin/validate-retention-lifecycle.sh')])
        run(['python3',str(ROOT/'harness/bin/seed-retained-browser.py')])
        time.sleep(3)
        run(['docker','run','--rm','--ipc=host','--network=host','-v',str(ROOT)+':/workspace','-w','/workspace','-e','RETENTION_PROBE_URL='+env['RETENTION_PROBE_URL'],'mcr.microsoft.com/playwright:v1.63.0-noble','node','cassini-app/scripts/check-retention-installed-browser.mjs'])
        print('PASSED',baseline['id'],'AppAPI DAV and lifecycle',flush=True)
    finally:
        subprocess.run(['docker','rm','-f',container],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        run(compose+['down','--volumes'])
