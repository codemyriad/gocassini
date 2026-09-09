import { describe, expect, it } from "vitest";

import {
  applyJob,
  applyPanel,
  applySurface,
  isOperatorPanel,
  OPERATOR_PANELS,
  readJob,
  readPanel,
  readSurface,
  surfaceHash,
} from "./surfaceRouting";

describe("readSurface", () => {
  it("reads the operator surface from the hash", () => {
    expect(readSurface("#surface=operator")).toBe("operator");
  });

  it("defaults to browse when absent, empty, or unknown", () => {
    expect(readSurface("")).toBe("browse");
    expect(readSurface("#")).toBe("browse");
    expect(readSurface("#surface=nonsense")).toBe("browse");
  });

  it("ignores the viewer's meeting/tx/t params alongside it", () => {
    expect(readSurface("#meeting=abc&tx=v3&t=1200ms")).toBe("browse");
    expect(readSurface("#surface=operator&meeting=abc")).toBe("operator");
  });
});

describe("surfaceHash", () => {
  it("marks the operator surface", () => {
    expect(surfaceHash("operator")).toBe("#surface=operator");
  });

  it("emits no marker for browse (keeps standalone/share URLs clean)", () => {
    expect(surfaceHash("browse")).toBe("");
  });

  it("round-trips through readSurface", () => {
    expect(readSurface(surfaceHash("operator"))).toBe("operator");
    expect(readSurface(surfaceHash("browse"))).toBe("browse");
  });
});

describe("applySurface", () => {
  it("adds the operator surface while preserving meeting/tx/t in order", () => {
    expect(applySurface("#meeting=abc&tx=v3&t=1200ms", "operator")).toBe(
      "#surface=operator&meeting=abc&tx=v3&t=1200ms",
    );
  });

  it("removes the surface param when switching back to browse, keeping the rest", () => {
    expect(applySurface("#surface=operator&meeting=abc&tx=v3&t=1200ms", "browse")).toBe(
      "#meeting=abc&tx=v3&t=1200ms",
    );
  });

  it("keeps t= last so parseTimeHash still matches", () => {
    expect(applySurface("#meeting=abc&t=5s", "operator").endsWith("t=5s")).toBe(true);
  });

  it("does not duplicate an existing surface param", () => {
    expect(applySurface("#surface=operator&meeting=abc", "operator")).toBe(
      "#surface=operator&meeting=abc",
    );
  });

  it("returns an empty hash for browse with no other params", () => {
    expect(applySurface("#surface=operator", "browse")).toBe("");
    expect(applySurface("", "browse")).toBe("");
  });
});

describe("readJob", () => {
  it("reads the selected run id from the hash", () => {
    expect(readJob("#surface=operator&job=01KXZV5QZP")).toBe("01KXZV5QZP");
  });

  it("returns an empty string when absent", () => {
    expect(readJob("")).toBe("");
    expect(readJob("#surface=operator")).toBe("");
  });
});

describe("applyJob", () => {
  it("adds the job param while preserving the surface", () => {
    expect(applyJob("#surface=operator", "abc")).toBe("#surface=operator&job=abc");
  });

  it("replaces an existing job rather than appending a second", () => {
    expect(applyJob("#surface=operator&job=old", "new")).toBe("#surface=operator&job=new");
  });

  it("clears the job param when given an empty id", () => {
    expect(applyJob("#surface=operator&job=abc", "")).toBe("#surface=operator");
    expect(applyJob("#job=abc", "")).toBe("");
  });

  it("round-trips through readJob", () => {
    expect(readJob(applyJob("#surface=operator", "01KXZV5QZP"))).toBe("01KXZV5QZP");
  });
});

describe("operator panels (D-723)", () => {
  it("defaults to the run console when no panel is named", () => {
    expect(readPanel("")).toBe("recordings");
    expect(readPanel("#surface=operator")).toBe("recordings");
    expect(readPanel("#surface=operator&job=01KXZV5QZP")).toBe("recordings");
  });

  it("reads every panel the nav offers, and rejects anything else", () => {
    for (const panel of OPERATOR_PANELS) {
      expect(readPanel(`#surface=operator&panel=${panel}`)).toBe(panel);
      expect(isOperatorPanel(panel)).toBe(true);
    }
    expect(readPanel("#surface=operator&panel=nonsense")).toBe("recordings");
    expect(isOperatorPanel("nonsense")).toBe(false);
    expect(isOperatorPanel(null)).toBe(false);
  });

  it("adds the panel param last, preserving the surface and the job", () => {
    expect(applyPanel("#surface=operator", "endpoints")).toBe("#surface=operator&panel=endpoints");
    expect(applyPanel("#surface=operator&job=abc", "pipeline")).toBe(
      "#surface=operator&job=abc&panel=pipeline",
    );
  });

  it("replaces an existing panel rather than appending a second", () => {
    expect(applyPanel("#surface=operator&panel=endpoints", "templates")).toBe(
      "#surface=operator&panel=templates",
    );
  });

  it("writes no marker for the run console, the way browse writes none", () => {
    expect(applyPanel("#surface=operator&panel=endpoints", "recordings")).toBe("#surface=operator");
    expect(applyPanel("#panel=endpoints", "recordings")).toBe("");
    expect(applyPanel("", "recordings")).toBe("");
  });

  it("round-trips through readPanel", () => {
    expect(readPanel(applyPanel("#surface=operator", "templates"))).toBe("templates");
    expect(readPanel(applyPanel("#surface=operator&panel=templates", "recordings"))).toBe(
      "recordings",
    );
  });

  it("leaves the surface and viewer params alone", () => {
    expect(readSurface(applyPanel("#surface=operator&meeting=abc", "endpoints"))).toBe("operator");
    expect(applyPanel("#surface=operator&meeting=abc", "endpoints")).toContain("meeting=abc");
  });
});

// The shell and the viewing layer share one location.hash, and
// core/transcript.ts's parseTimeHash anchors `#t=…` at end-of-string
// (cassini-viewer/src/viewer/hashRouting.ts writes t= last for that reason). A
// shell param written after it does not collide with a viewer key — it silently
// costs a meeting deep-link its seek time. applySurface already guards this by
// writing surface= first; the appending writers have to guard it too.
describe("shell params never displace the viewer's t=", () => {
  it("keeps t= last when the panel is added", () => {
    expect(applyPanel("#surface=operator&meeting=abc&t=5s", "endpoints")).toBe(
      "#surface=operator&meeting=abc&panel=endpoints&t=5s",
    );
    expect(applyPanel("#meeting=abc&tx=v3&t=1200ms", "templates").endsWith("t=1200ms")).toBe(true);
  });

  it("keeps t= last when a run is deep-linked", () => {
    expect(applyJob("#surface=operator&meeting=abc&t=5s", "01KXZV5QZP")).toBe(
      "#surface=operator&meeting=abc&job=01KXZV5QZP&t=5s",
    );
  });

  it("still appends when the hash carries no t= at all", () => {
    expect(applyPanel("#surface=operator&meeting=abc", "endpoints")).toBe(
      "#surface=operator&meeting=abc&panel=endpoints",
    );
    expect(applyJob("#surface=operator", "abc")).toBe("#surface=operator&job=abc");
  });

  it("survives the whole nav walk a user can take from a meeting deep link", () => {
    // Browse with a seek → Operator → a settings row → back to Browse. The seek
    // has to still be readable at the end of it.
    const browsing = "#meeting=abc&t=5s";
    const operator = applySurface(browsing, "operator");
    const settings = applyPanel(operator, "endpoints");
    expect(applySurface(settings, "browse").endsWith("t=5s")).toBe(true);
  });
});

describe("the settings surface that never shipped (D-723)", () => {
  // #207 drafted configuration as a third `surface=settings`, and D-723 folded
  // it into Operator before either reached main. Nobody holds such a URL, so
  // there is no redirect for one — it degrades the same way any hand-edited
  // surface name does.
  it("treats it as an unknown surface, not as a settings deep link", () => {
    expect(readSurface("#surface=settings")).toBe("browse");
    expect(readPanel("#surface=settings")).toBe("recordings");
  });

  it("names only the two surfaces that exist", () => {
    expect(surfaceHash("operator")).toBe("#surface=operator");
    expect(applySurface("#surface=settings&meeting=abc", "browse")).toBe("#meeting=abc");
  });
});
