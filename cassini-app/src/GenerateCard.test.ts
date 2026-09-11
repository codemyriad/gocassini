import { describe, expect, it } from "vitest";

import generateCardSource from "./GenerateCard.svelte?raw";

// Source-level assertions, the convention this repo follows for .svelte files
// (see NeedsSetupCard.test.ts): the suite runs in node with no DOM harness.
// What is asserted here is what a reader would call a bug — the card inventing
// copy, rendering a control that 403s, or keeping a run of its own under the
// button after the shell has taken it.

describe("GenerateCard", () => {
  it("hands the created run to the shell and renders nothing of its own under the button", () => {
    // Submit closes the panel (D-749): the record goes up as `created`, the
    // shell puts it at the top of the browse list, and this card keeps no run,
    // no poll and no retry of its own. Every one of those used to live here,
    // and each was a second copy of something the list already showed.
    expect(generateCardSource).toContain('dispatch("created", started);');
    expect(generateCardSource).not.toContain("{#if run}");
    expect(generateCardSource).not.toContain("readInsight");
    expect(generateCardSource).not.toContain("retryInsight");
    expect(generateCardSource).not.toContain("setTimeout");
    expect(generateCardSource).not.toContain("onDestroy");
    expect(generateCardSource).not.toContain("NeedsSetupCard");
    expect(generateCardSource).not.toContain("buildRunFailureNotice");
  });

  it("still says when the request itself was refused", () => {
    // The one thing left under the button besides the disclosure: a POST that
    // failed is this card's to report, since nothing reached the list.
    expect(generateCardSource).toContain("createError = describe(error);");
    expect(generateCardSource).toContain('<p class="text-xs text-error" role="alert">{createError}</p>');
  });

  it("has no second notion of admin", () => {
    // The client is null for exactly the people the operator boundary probe
    // denied, and `operator/settings/workflows` is ADMIN at the proxy — so the
    // template picker exists precisely where it would not 403.
    expect(generateCardSource).toContain("$: isAdmin = operatorClient !== null;");
    expect(generateCardSource).toContain("{#if isAdmin}");
    expect(generateCardSource).toContain("This runs the template your administrator configured");
  });

  it("lists no runs, its own included", () => {
    // It used to open on a listing of every previous run, then on the one it
    // started; both were a second place insights are shown, free to disagree
    // with the browse catalogue behind the panel.
    expect(generateCardSource).not.toContain("listInsights");
    expect(generateCardSource).not.toContain("let run:");
  });

  it("opens on a real template, not on a synthetic default", () => {
    // "This deployment's default" named no template and disclosed no question,
    // and the template it stood for was in the list beside it.
    expect(generateCardSource).not.toContain("This deployment's default");
    expect(generateCardSource).toContain('const SHIPPED_DEFAULT_WORKFLOW = "summarise";');
    expect(generateCardSource).toContain("workflows[0].id");
  });

  it("shows what the chosen template will ask", () => {
    // The name says nothing about what the model is asked to do; the question
    // is the affordance, and the description says what comes back.
    expect(generateCardSource).toContain("chosenWorkflowEntry.question");
    expect(generateCardSource).toContain("chosenWorkflowEntry.description");
  });

  it("is contained by the panel it sits in", () => {
    // This mounts into a shadow root inside Nextcloud's own page, where a fixed
    // overlay escapes the app and covers Nextcloud's chrome (App.svelte).
    expect(generateCardSource).not.toContain("position: fixed");
    expect(generateCardSource).not.toContain("position:fixed");
  });

  it("says where the transcripts go before they go there", () => {
    // The model call uses the instance's key, so a run is attributable to the
    // deployment; the document lands in the requester's own files. Both are
    // said here rather than discovered afterwards.
    expect(generateCardSource).toContain("configured AI endpoint");
    expect(generateCardSource).toContain("written into your own Nextcloud files");
  });

  it("never puts a raw workflow id in front of somebody who has no names", () => {
    // The registry is ADMIN at the proxy, so a non-admin can look nothing up —
    // and they have no picker either, so the only run this card ever shows them
    // is one they started against the deployment's configured template. It is
    // named by the question asked, or by the template's display name where this
    // reader has one, never by the raw id.
    expect(generateCardSource).not.toContain("run.workflowId");
  });

  it("lets anybody choose the endpoint, not only administrators", () => {
    // Where your own transcripts go is the asker's decision. The template
    // picker is admin-only because its registry is ADMIN at the proxy; the
    // provider list is a USER route carrying ids and names, so this one is not
    // behind {#if isAdmin}.
    const adminBlock = generateCardSource.slice(
      generateCardSource.indexOf("{#if isAdmin}"),
      generateCardSource.indexOf("{#if providers.length > 0}"),
    );
    expect(adminBlock).not.toContain(">Provider<");
    expect(generateCardSource).toContain("listAIProviders(operatorBasePath)");
    expect(generateCardSource).toContain("$: if (operatorBasePath !== \"\" && !providersAsked)");
  });

  it("defaults to the first provider", () => {
    // Somebody who does not care should get a working run without touching
    // anything.
    expect(generateCardSource).toContain("chosenProvider = providers[0].id;");
  });

  it("shows the endpoint's default model and offers no second place to choose one", () => {
    // One endpoint, one model, set in AI providers (D-749). A per-run combobox
    // was a second place to choose a model for one job, and its empty default
    // let the child inherit a model chosen for a different endpoint.
    expect(generateCardSource).not.toContain("ModelCombobox");
    expect(generateCardSource).not.toContain("listAIProviderModels");
    expect(generateCardSource).not.toContain("loadingModelsFor");
    expect(generateCardSource).toContain("chosenProviderEntry?.model");
    expect(generateCardSource).toContain("the endpoint's own default");
    // The wire keeps `model`, empty, so a per-run override can return without
    // a request-shape change.
    expect(generateCardSource).toContain('const chosenModel = "";');
    expect(generateCardSource).toContain("model: chosenModel,");
  });

  it("still runs when the endpoints cannot be listed", () => {
    // No list means no provider on the request, which the operator reads as
    // "this deployment's own" — exactly what happened before there was a
    // picker. It narrows the card; it does not block it.
    expect(generateCardSource).toContain("providersError = describe(error);");
    expect(generateCardSource).toContain("The AI endpoints could not be listed");
  });

  it("offers the question box only where a question can be asked", () => {
    // `POST insights` refuses a question a workflow has no slot for, and no
    // prompt this image ships carries one — so an unconditional box is a
    // control whose every use is a 400. The rule is read off the registry's own
    // `instruction` bytes, which is what the operator decides on too, so the box
    // appears by itself the day a question-taking workflow ships.
    expect(generateCardSource).toContain("workflowTakesQuestion(chosenWorkflowEntry)");
    expect(generateCardSource).toContain("{#if questionAccepted}");
    // The other half of the same refusal: a workflow with a slot for a question
    // cannot run without one.
    expect(generateCardSource).toContain("questionMissing");
    expect(generateCardSource).toContain("disabled={creating || questionMissing}");
    // Text typed against one template must not ride along into another that
    // would be refused for carrying it.
    expect(generateCardSource).toContain('question: questionAccepted ? question : ""');
  });
});
