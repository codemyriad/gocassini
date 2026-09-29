#!/usr/bin/env python3
"""Synthetic installed-AppAPI retention mechanics probe. Never uses seed files.

Requires an installed local harness. Creates disposable users and a group,
uses ExApp act-as-user credentials for DAV/OCS, and deletes its users on exit.
No version/trash purge and no instance retention settings are changed.
"""
import base64
import json
import os
import subprocess
import uuid
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


BASE = os.environ.get('RETENTION_PROBE_URL', 'http://127.0.0.1:28080').rstrip('/')
CONTAINER = os.environ.get('CASSINI_EXAPP_CONTAINER', 'nc_app_gocassini')
OPENER = urllib.request.build_opener(NoRedirect)
ENV = json.loads(subprocess.check_output(['docker', 'inspect', '--format', '{{json .Config.Env}}', CONTAINER]))
ENV = dict(item.split('=', 1) for item in ENV if '=' in item)
SECRET = ENV['APP_SECRET']
PREFIX = 'retention_probe_' + uuid.uuid4().hex[:12]
PASSWORD = uuid.uuid4().hex + uuid.uuid4().hex
OWNER, READER, GROUP_READER, DOWNSTREAM = [PREFIX + suffix for suffix in ('_o', '_r', '_g', '_d')]
GROUP = PREFIX + '_group'
CREATED = []
GROUP_CREATED = False


def request(method, path, user=None, data=None, headers=None, expected=(200, 201, 204), admin=False):
    h = dict(headers or {})
    if admin:
        pair = os.environ.get('ADMIN_USER', 'admin') + ':' + os.environ.get('ADMIN_PASSWORD', 'admin')
        h['Authorization'] = 'Basic ' + base64.b64encode(pair.encode()).decode()
    else:
        h.update({'AUTHORIZATION-APP-API': base64.b64encode((user + ':' + SECRET).encode()).decode(),
                  'EX-APP-ID': ENV['APP_ID'], 'EX-APP-VERSION': ENV['APP_VERSION']})
        if ENV.get('AA_VERSION'):
            h['AA-VERSION'] = ENV['AA_VERSION']
    if isinstance(data, dict):
        data = urllib.parse.urlencode(data).encode()
        h['Content-Type'] = 'application/x-www-form-urlencoded'
    if isinstance(data, str):
        data = data.encode()
    req = urllib.request.Request(BASE + path, data=data, headers=h, method=method)
    try:
        response = OPENER.open(req, timeout=30)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read()
        assert response.code in expected, f'{method} returned {response.code}; expected {expected}'
        return response.code, body


def ocs(method, path, user=None, data=None, admin=False):
    _, body = request(method, '/ocs/v2.php/' + path + ('&' if '?' in path else '?') + 'format=json',
                      user, data, {'OCS-APIRequest': 'true'}, expected=(200,), admin=admin)
    obj = json.loads(body)['ocs']
    assert obj['meta']['statuscode'] in (100, 200), 'OCS operation failed'
    return obj['data']


def dav(user, leaf):
    return '/remote.php/dav/files/' + urllib.parse.quote(user, safe='') + '/' + urllib.parse.quote(leaf.lstrip('/'), safe='/')


def state(user, leaf):
    _, body = request('PROPFIND', dav(user, leaf), user,
                      '<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:fileid/><d:getetag/></d:prop></d:propfind>',
                      {'Depth': '0', 'Content-Type': 'application/xml'}, (207,))
    root = ET.fromstring(body)
    return root.find('.//{http://owncloud.org/ns}fileid').text, root.find('.//{DAV:}getetag').text


def share(user, leaf, kind, recipient=None, **extra):
    data = {'path': '/' + leaf, 'shareType': kind, 'permissions': 17 if kind != 3 else 1, **extra}
    if recipient:
        data['shareWith'] = recipient
    return ocs('POST', 'apps/files_sharing/api/v1/shares', user, data)


def received(user, share_id):
    rows = ocs('GET', 'apps/files_sharing/api/v1/shares?shared_with_me=true', user)
    return next(row for row in rows if str(row['id']) == str(share_id))


def check_read(user, share_id, body):
    row = received(user, share_id)
    assert request('GET', dav(user, row['file_target']), user)[1] == body
    return row


def main():
    global GROUP_CREATED
    for user in (OWNER, READER, GROUP_READER, DOWNSTREAM):
        ocs('POST', 'cloud/users', data={'userid': user, 'password': PASSWORD}, admin=True)
        CREATED.append(user)
    ocs('POST', 'cloud/groups', data={'groupid': GROUP}, admin=True)
    GROUP_CREATED = True
    ocs('POST', 'cloud/users/' + GROUP_READER + '/groups', data={'groupid': GROUP}, admin=True)
    old, new = 'probe.opus', 'probe.cassini.transcription.json'
    audio = b'synthetic original bytes'
    text = b'{"format":"cassini.transcription.v1","synthetic":true}'
    request('PUT', dav(OWNER, old), OWNER, audio)
    identity, etag = state(OWNER, old)
    direct = share(OWNER, old, 0, READER)
    group = share(OWNER, old, 1, GROUP)
    public = share(OWNER, old, 3, password=PASSWORD, expireDate='2099-01-01')
    recipient_path = check_read(READER, direct['id'], audio)['file_target'].lstrip('/')
    reshare = share(READER, recipient_path, 0, DOWNSTREAM)
    request('MOVE', dav(READER, recipient_path), READER, headers={
        'Destination': BASE + dav(READER, 'renamed-by-recipient.opus'), 'Overwrite': 'F'})
    # All stale methods must refuse mutation, including source MOVE preconditions.
    for method in ('PUT', 'MOVE', 'DELETE'):
        headers = {'If-Match': '"definitely-stale"'}
        if method == 'MOVE':
            headers.update({'Destination': BASE + dav(OWNER, new), 'Overwrite': 'F'})
        request(method, dav(OWNER, old), OWNER, text if method == 'PUT' else None, headers, (412,))
        assert request('GET', dav(OWNER, old), OWNER)[1] == audio
    # Destination collisions must preserve both leaves.
    request('PUT', dav(OWNER, new), OWNER, b'collision')
    request('MOVE', dav(OWNER, old), OWNER, headers={'If-Match': etag,
            'Destination': BASE + dav(OWNER, new), 'Overwrite': 'F'}, expected=(412,))
    assert request('GET', dav(OWNER, new), OWNER)[1] == b'collision'
    request('DELETE', dav(OWNER, new), OWNER)
    request('PUT', dav(OWNER, old), OWNER, text, {'If-Match': etag, 'Content-Type': 'application/json'})
    assert state(OWNER, old)[0] == identity
    request('MOVE', dav(OWNER, old), OWNER, headers={'If-Match': state(OWNER, old)[1],
            'Destination': BASE + dav(OWNER, new), 'Overwrite': 'F'})
    assert state(OWNER, new)[0] == identity
    for user, sid in ((READER, direct['id']), (GROUP_READER, group['id']), (DOWNSTREAM, reshare['id'])):
        check_read(user, sid, text)
    after = ocs('GET', 'apps/files_sharing/api/v1/shares/' + str(public['id']), OWNER)
    if isinstance(after, list):
        after = after[0]
    for key in ('id', 'token', 'password', 'expiration', 'permissions'):
        assert after.get(key) == public.get(key), f'public share {key} changed'
    # Revoke access while retaining the file and prove the revoked user cannot read.
    revoked_path = received(READER, direct['id'])['file_target']
    ocs('DELETE', 'apps/files_sharing/api/v1/shares/' + str(direct['id']), OWNER)
    request('GET', dav(READER, revoked_path), READER, expected=(403, 404))
    request('DELETE', dav(OWNER, new), OWNER, headers={'If-Match': state(OWNER, new)[1]})
    request('GET', dav(OWNER, new), OWNER, expected=(404,))
    print(json.dumps({'file_id': identity, 'conditional_mutations': 'passed',
                      'direct_group_reshare_identity': 'passed', 'public_attributes': 'passed',
                      'recipient_rename_and_revoke': 'passed', 'history_purge': 'not performed'}))


try:
    main()
finally:
    for user in reversed(CREATED):
        ocs('DELETE', 'cloud/users/' + user, admin=True)
    if GROUP_CREATED:
        ocs('DELETE', 'cloud/groups/' + GROUP, admin=True)
    users = ocs('GET', 'cloud/users?search=' + PREFIX, admin=True)
    assert not users['users'], 'probe users remain after cleanup'
    print('Synthetic users and their files removed.')
