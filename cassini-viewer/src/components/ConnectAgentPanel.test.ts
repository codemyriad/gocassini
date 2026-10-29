import { describe, expect, it } from "vitest";
import { render } from "svelte/server";

import ConnectAgentPanel from "./ConnectAgentPanel.svelte";
import appSource from "../App.svelte?raw";

const ENDPOINT = "https://cloud.example/index.php/apps/app_api/proxy/gocassini/mcp";

function rendered(appPasswordsUrl: string | null = "https://cloud.example/index.php/settings/user/security") {
  return render(ConnectAgentPanel, {
    props: { connection: { endpoint: ENDPOINT, appPasswordsUrl }, active: true },
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
    expect(unlinked).toContain("Personal settings → Security");
    expect(unlinked).not.toContain("settings/user/security");
  });

  it("says what the agent can and cannot do before anyone hands it a credential", () => {
    const html = rendered();
    expect(html).toContain("sees exactly the meetings you see here");
    expect(html).toContain("can only read them");
  });
});

describe("the Connect an agent tab", () => {
  it("is offered only where the provider knows the endpoint", () => {
    // Every other build — standalone export, public view, bundled artifact —
    // has no MCP server, and gets no tab rather than instructions that fail.
    expect(appSource).toContain('typeof dataProvider.agentConnection === "function"');
    expect(appSource).toContain("{#if agentConnection}");
  });
});
