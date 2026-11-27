import { afterEach, describe, expect, it, vi } from "vitest";
import { copyWithFallback } from "./clipboard";

function fakeField() {
  return { select: vi.fn(), setSelectionRange: vi.fn() } as unknown as HTMLInputElement & {
    select: ReturnType<typeof vi.fn>;
    setSelectionRange: ReturnType<typeof vi.fn>;
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("copyWithFallback", () => {
  it("uses the async clipboard when it is allowed", async () => {
    const writeText = vi.fn(async () => {});
    const execCommand = vi.fn(() => true);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    vi.stubGlobal("document", { execCommand });
    const field = fakeField();
    expect(await copyWithFallback("s3cret", field)).toBe(true);
    expect(writeText).toHaveBeenCalledWith("s3cret");
    expect(execCommand).not.toHaveBeenCalled();
    expect(field.select).not.toHaveBeenCalled();
  });

  it("falls back to execCommand over the selected field", async () => {
    vi.stubGlobal("navigator", { clipboard: { writeText: vi.fn(async () => Promise.reject(new Error("denied"))) } });
    const execCommand = vi.fn(() => true);
    vi.stubGlobal("document", { execCommand });
    const field = fakeField();
    expect(await copyWithFallback("s3cret", field)).toBe(true);
    expect(field.select).toHaveBeenCalled();
    expect(field.setSelectionRange).toHaveBeenCalledWith(0, 6);
    expect(execCommand).toHaveBeenCalledWith("copy");
  });

  it("reports a refusal and leaves the field selected", async () => {
    vi.stubGlobal("navigator", {});
    vi.stubGlobal("document", {
      execCommand: vi.fn(() => {
        throw new Error("not allowed");
      }),
    });
    const field = fakeField();
    expect(await copyWithFallback("s3cret", field)).toBe(false);
    expect(field.select).toHaveBeenCalledTimes(2);
  });

  it("copes with no field", async () => {
    vi.stubGlobal("navigator", {});
    vi.stubGlobal("document", { execCommand: vi.fn(() => false) });
    expect(await copyWithFallback("s3cret", null)).toBe(false);
  });
});
