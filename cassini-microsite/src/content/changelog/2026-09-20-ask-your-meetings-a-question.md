---
title: Ask your meetings a question
date: 2026-09-20
version: 0.2.0-beta.7
---

Pick meetings in the browse list, open Prepare context, and press Generate. Cassini sends those transcripts to the AI endpoint you chose and writes the answer into your own Nextcloud Files, in a Cassini Insights folder in your home. You own the file outright, so Nextcloud's own sharing decides who else may read it.

One of the templates is "Ask your own question": you type the question, and the answer comes back in at most three sentences, with the evidence of who said what, and what these meetings do not answer.

Every meeting is fetched as you, at the moment the run happens, so an answer can only ever be built out of meetings you could already open yourself. A run says what it is doing rather than spinning: queued, then running, then a result or a failure named in words you can act on. A local model reading five meetings takes minutes, and the card tells you so.

Endpoints and models are chosen in the app's own Settings, beside Browse and Operator. Moving between a hosted provider and a model server of your own is one settings change with no redeploy, and registering your first endpoint turns meeting summaries on.
