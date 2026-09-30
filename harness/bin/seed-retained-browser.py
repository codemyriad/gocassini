#!/usr/bin/env python3
"""Seed one retained meeting in an owned, disposable matrix container only."""
import base64, json, os, pathlib, sqlite3, subprocess, tempfile, urllib.request, urllib.parse, xml.etree.ElementTree as ET
root=pathlib.Path(__file__).resolve().parents[2]
container=os.environ['CASSINI_EXAPP_CONTAINER']
assert container.startswith('retention-nc') and container.endswith('-exapp'), 'only owned matrix fixtures may be seeded'
base=os.environ['RETENTION_PROBE_URL'].rstrip('/')
assert urllib.parse.urlparse(base).hostname in ('localhost','127.0.0.1')
env=dict(v.split('=',1) for v in json.loads(subprocess.check_output(['docker','inspect','--format','{{json .Config.Env}}',container])) if '=' in v)
headers={'EX-APP-ID':env['APP_ID'],'EX-APP-VERSION':env['APP_VERSION'],'AUTHORIZATION-APP-API':base64.b64encode(('cassini:'+env['APP_SECRET']).encode()).decode(),'OCS-APIRequest':'true'}
def request(method,path,data=None,extra=None):
    req=urllib.request.Request(base+path,method=method,data=data,headers=headers|dict(extra or {}))
    with urllib.request.urlopen(req,timeout=30) as response:return response.read()
leaf='CassiniRecordings/meetings/retention-browser.cassini.transcription.json'
raw=(root/'spec/fixtures/retained-meeting.json').read_bytes()
request('PUT','/remote.php/dav/files/cassini/'+leaf,raw,{'Content-Type':'application/json'})
props=request('PROPFIND','/remote.php/dav/files/cassini/'+leaf,b'<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:fileid/></d:prop></d:propfind>',{'Depth':'0','Content-Type':'application/xml'})
identity=int(ET.fromstring(props).find('.//{http://owncloud.org/ns}fileid').text)
request('POST','/ocs/v2.php/apps/files_sharing/api/v1/shares?format=json',urllib.parse.urlencode({'path':'/'+leaf,'shareType':0,'shareWith':'admin','permissions':1}).encode(),{'Content-Type':'application/x-www-form-urlencoded'})
# Stop before taking a SQLite copy; no live DB/WAL copying or direct writes.
subprocess.run(['docker','stop',container],check=True,stdout=subprocess.DEVNULL)
with tempfile.TemporaryDirectory(prefix='retention-browser-') as tmp:
    db=pathlib.Path(tmp)/'jobs.sqlite3'
    subprocess.run(['docker','cp',container+':/var/lib/cassini-operator/jobs.sqlite3',str(db)],check=True)
    with sqlite3.connect(db) as conn:
        doc=json.loads(raw)
        conn.execute('INSERT INTO meeting_lifecycle(name,file_id,document_path,representation,state,age_anchor,anchor_source,document_id) VALUES(?,?,?,?,?,?,?,?)',('retention-browser.opus',identity,leaf,'transcription','active',doc['retention']['ageAnchor'],doc['retention']['anchorSource'],doc['identity']['documentId']))
    subprocess.run(['docker','cp',str(db),container+':/var/lib/cassini-operator/jobs.sqlite3'],check=True)
subprocess.run(['docker','start',container],check=True,stdout=subprocess.DEVNULL)
print('Seeded one retained browser fixture; Nextcloud fixture teardown owns cleanup.')
