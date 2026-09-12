import { describe,it,expect } from 'vitest';
import { execFileSync } from 'node:child_process';
import { initialEnvironment,occCommand,deploymentHandoffScript,providerRequest,type DeploymentEnvironment } from './deploymentGuidance';
import type { RecordingReadiness } from './readiness';

describe('deployment guidance',()=>{
 it('does not infer installation type or host access',()=>{
  const env=initialEnvironment();
  expect(occCommand(env)).toBeNull();
  for(const access of ['unknown','container','provider'] as const) expect(occCommand({...env,installation:'aio',access})).toBeNull();
  expect(occCommand({...env,installation:'other',access:'host'})).toBeNull();
 });
 it('requires explicit execution details for non-AIO containers and host installs',()=>{
  const base:DeploymentEnvironment={...initialEnvironment(),access:'host',installation:'compose'};
  expect(occCommand(base)).toBeNull();
  expect(occCommand({...base,target:'nextcloud',user:'www-data',occPath:'/var/www/html/occ'})).toBe("docker compose exec -T --user 'www-data' 'nextcloud' php '/var/www/html/occ'");
  expect(occCommand({...base,installation:'docker',target:'nc',user:'abc',occPath:'/app/www/public/occ'})).toBe("docker exec -u 'abc' 'nc' php '/app/www/public/occ'");
  expect(occCommand({...base,installation:'host',user:'apache',occPath:'/srv/nextcloud/occ'})).toBe("sudo -u 'apache' php '/srv/nextcloud/occ'");
  expect(occCommand({...base,installation:'snap'})).toBe('sudo nextcloud.occ');
 });
 it('rejects option injection and quotes paths and URLs as literal shell arguments',()=>{
  const env:DeploymentEnvironment={installation:'docker',access:'host',target:'nc',user:'www-data',occPath:"/srv/a'$(touch /tmp/cassini-must-not-exist)`id`/occ"};
  for(const field of ['target','user'] as const) expect(occCommand({...env,[field]:'--privileged'})).toBeNull();
  const script=deploymentHandoffScript("https://cloud.test/a'$(id)`id`/provisioning",env)!;
  expect(()=>execFileSync('bash',['-n'],{input:script})).not.toThrow();
  expect(script).toContain("'\\''");
  expect(script.indexOf('command -v')).toBeLessThan(script.indexOf('config:app:set'));
  expect(script.indexOf('status >/dev/null')).toBeLessThan(script.indexOf('config:app:set'));
  expect(script).toContain("recording_servers --default-value=''");
  expect(script).toContain('umask 077');
 });
 it('retains the AIO persistence guard only for confirmed AIO',()=>{
  const env:DeploymentEnvironment={...initialEnvironment(),access:'host',installation:'aio'};
  const script=deploymentHandoffScript('https://cloud.test/provisioning',env)!;
  expect(script).toContain('NEXTCLOUD_KEEP_DISABLED_APPS=true');
  expect(script).toContain('docker exec -u www-data nextcloud-aio-nextcloud php occ');
  expect(deploymentHandoffScript('https://cloud.test/provisioning',{...env,installation:'snap'})).not.toContain('NEXTCLOUD_KEEP_DISABLED_APPS');
 });
 it('creates provider requests without private response fields or raw diagnostic text',()=>{
  const report={checks:[{id:'talk.hpb',state:'not_verified',message:'private raw error',code:'private-token'}, {id:'private-id',state:'needs_action',message:'private-message'}],test_room_url:'https://private.test/call/secret-room',secret_source:'env',test:{job_id:'private-job'}} as unknown as RecordingReadiness;
  const text=providerRequest('secret',report);
  for(const privateValue of ['private raw error','private-token','private-id','private-message','private.test','secret-room','private-job'])expect(text).not.toContain(privateValue);
  expect(text).toContain('High-performance backend: Not verified');
  expect(text).toContain('do not reply with the secret');
 });
});
