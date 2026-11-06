import { describe, expect, it } from "vitest";
import { render } from "svelte/server";

import ConnectAgentPanel from "./ConnectAgentPanel.svelte";
import RoomsRail from "./RoomsRail.svelte";
import appSource from "../App.svelte?raw";

const ENDPOINT = "https://cloud.example/index.php/apps/app_api/proxy/gocassini/mcp";

function rendered(appPasswordsUrl: string | null = "https://cloud.example/index.php/settings/user/security") {
  return render(ConnectAgentPanel, {
    props: { connection: { endpoint: ENDPOINT, appPasswordsUrl } },
  }).body.replace(/\s+/g, " ");
}

describe("ConnectAgentPanel", () => {
  it("gives the server's address and the three steps", () => {
    const html = rendered();
    expect(html).toContain(ENDPOINT);
    const steps = html.slice(html.indexOf("<ol"), html.indexOf("</ol>"));
    expect(steps.match(/<li/g)).toHaveLength(3);
    expect(steps).toContain("Create an app password");
    expect(steps).toContain("Add Cassini to your agent");
    expect(steps).toContain("Ask it something");
  });

  it("gives Claude Code a command that works as pasted, once the password is filled in", () => {
    // Claude Code connects using only these instructions (the brief's
    // acceptance): the transport, the address and the bearer header.
    const html = rendered().replaceAll("&quot;", '"').replaceAll("&lt;", "<").replaceAll("&gt;", ">");
    expect(html).toContain(
      `claude mcp add --transport http cassini ${ENDPOINT} --header "Authorization: Bearer <app password>"`,
    );
  });

  it("links to where app passwords are made, and says where when it cannot link", () => {
    expect(rendered()).toContain('href="https://cloud.example/index.php/settings/user/security"');
    const unlinked = rendered(null);
    expect(unlinked).toContain("Personal settings › Security");
    expect(unlinked).not.toContain("settings/user/security");
  });

  it("says what the agent can and cannot do before anyone hands it a credential", () => {
    const html = rendered();
    expect(html).toContain("can read the meetings you see here");
    expect(html).toContain("it can only read");
  });

  it("sends other clients and the CLI to the guide, and can be closed", () => {
    const html = rendered();
    expect(html).toContain("Other agents and the command line");
    expect(html).toContain("agent-meeting-access.md#7-connect-an-agent-over-mcp");
    expect(html).toContain('aria-label="Close Connect an agent"');
  });
});

describe("the way in to Connect an agent", () => {
  function rail(agentOffered: boolean) {
    return render(RoomsRail, { props: { agentOffered } }).body;
  }

  it("is a link at the foot of the rooms rail, not a Browse tab", () => {
    expect(rail(true)).toContain("Connect an agent");
    expect(appSource).not.toContain('browseTab === "agent"');
    expect(appSource).toContain("on:connectAgent={() => (agentOpen = true)}");
  });

  it("is offered only where the provider knows the endpoint", () => {
    // Every other build — standalone export, public view, bundled artifact —
    // has no MCP server, and gets no way in rather than instructions that fail.
    expect(rail(false)).not.toContain("Connect an agent");
    expect(appSource).toContain('typeof dataProvider.agentConnection === "function"');
    expect(appSource).toContain("agentOffered={agentConnection !== null}");
  });

  it("opens as a layer the shell closes on Escape", () => {
    expect(appSource).toContain("prepareOpen || agentOpen");
    expect(appSource.match(/if \(agentOpen\) \{\s*agentOpen = false;/g)).toHaveLength(2);
  });
});
