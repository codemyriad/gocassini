import { checkLabels, type RecordingReadiness } from './readiness';

export type Installation = 'unknown' | 'aio' | 'docker' | 'compose' | 'host' | 'snap' | 'other';
export type ServerAccess = 'unknown' | 'host' | 'container' | 'provider';
export interface DeploymentEnvironment {
  installation: Installation;
  access: ServerAccess;
  target: string;
  user: string;
  occPath: string;
}
export const initialEnvironment = (): DeploymentEnvironment => ({ installation:'unknown', access:'unknown', target:'', user:'', occPath:'' });
const quote = (s:string) => "'" + s.replaceAll("'", "'\\''") + "'";
const name = (s:string) => /^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/.test(s);
const absolutePath = (s:string) => s.startsWith('/') && !/[\r\n\0]/.test(s);

export function occCommand(env: DeploymentEnvironment): string | null {
  if (env.access !== 'host') return null;
  switch(env.installation) {
    case 'aio': return 'docker exec -u www-data nextcloud-aio-nextcloud php occ';
    case 'snap': return 'sudo nextcloud.occ';
    case 'host': return name(env.user) && absolutePath(env.occPath) ? `sudo -u ${quote(env.user)} php ${quote(env.occPath)}` : null;
    case 'docker': return name(env.target) && name(env.user) && absolutePath(env.occPath) ? `docker exec -u ${quote(env.user)} ${quote(env.target)} php ${quote(env.occPath)}` : null;
    case 'compose': return name(env.target) && name(env.user) && absolutePath(env.occPath) ? `docker compose exec -T --user ${quote(env.user)} ${quote(env.target)} php ${quote(env.occPath)}` : null;
    default: return null;
  }
}

// This is a host-side advanced fallback. Inputs are confirmed installation
// details, never executable command fragments. No saved credentials enter it.
export function deploymentHandoffScript(url:string, env:DeploymentEnvironment): string | null {
  const occ=occCommand(env);
  if(!occ || !/^https?:\/\//.test(url)) return null;
  const commands=['bash','curl','jq', ...(env.installation==='aio'||env.installation==='docker'||env.installation==='compose'?['docker']:['sudo'])];
  return [
    '#!/usr/bin/env bash', 'set -euo pipefail', 'umask 077',
    `for tool in ${commands.join(' ')}; do`,
    `  command -v "$tool" >/dev/null || { printf 'Missing required tool: %s. Ask the host administrator to install it.\\n' "$tool" >&2; exit 1; }`,
    'done',
    ...(env.installation==='compose'?['docker compose version >/dev/null']:[]),
    ...(env.installation==='aio'?[
      `docker exec nextcloud-aio-nextcloud sh -c 'test "$TALK_RECORDING_ENABLED" != yes && test "$REMOVE_DISABLED_APPS" != yes' || {`,
      `  printf '%s\\n' 'Disable AIO Talk Recording and set NEXTCLOUD_KEEP_DISABLED_APPS=true on the mastercontainer. Recreate through AIO, then retry.' >&2`,
      '  exit 1', '}',
    ]:[]),
    `${occ} status >/dev/null`,
    'backup=$(mktemp ./cassini-recording-backend.XXXXXX)',
    `${occ} config:app:get spreed recording_servers --default-value='' > "$backup"`,
    `printf 'Previous recording backend saved to %s\\n' "$backup"`,
    `read -r -p 'Nextcloud administrator username: ' admin_user`,
    `recording_servers=$(curl --fail --silent --show-error --user "$admin_user" ${quote(url)} | jq -ce '.recording_servers | select(.servers | length > 0)')`,
    `${occ} config:app:set spreed recording_servers --value="$recording_servers"`,
    `${occ} config:app:set spreed call_recording --value=yes`,
    'unset recording_servers',
  ].join('\n');
}

export type RepairPurpose = 'handoff' | 'secret' | 'hpb';
export function providerRequest(purpose:RepairPurpose, report:RecordingReadiness):string {
  const request = {
    handoff:'Please connect Nextcloud Talk to the installed Cassini recording backend. Review the existing recorder configuration, keep a backup, and make the change persistent across restarts. Confirm where Nextcloud, HPB and the ExApp deployment engine run.',
    secret:"Please help configure Cassini with the Talk signaling server's internal-client secret ([clients] internalsecret). This differs from Talk's recording-backend and signaling-backend secrets. Enter it directly in Cassini Setup or deployment configuration through an approved secure channel; do not reply with the secret in this ticket.",
    hpb:'Please verify whether Nextcloud Talk has a standalone signaling server with HPB media support and internal-client authentication available for Cassini recording. If not, please advise whether you can enable or provide this capability and configure Talk to use it.',
  }[purpose];
  const states:Record<string,string>={passed:'Passed',needs_action:'Needs action',not_verified:'Not verified'};
  const checks=report.checks.filter(c=>checkLabels[c.id]&&states[c.state]).map(c=>`- ${checkLabels[c.id]}: ${states[c.state]}`);
  return ['Cassini recording setup — administrator/provider request','',request,'',
    'Reported checks (point-in-time observations; expired evidence is not a failure):',...checks,'',
    'After configuring the service, please help run Check again and a short recording started through Talk, followed by playback confirmation.',
    'This request omits room URLs, recording IDs, credentials and raw logs. I can provide the public Nextcloud address separately.',
  ].join('\n');
}
