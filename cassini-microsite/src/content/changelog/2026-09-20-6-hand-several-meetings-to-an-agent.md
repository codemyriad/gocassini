---
title: Hand several meetings to an agent as one document
date: 2026-09-20
version: 0.2.0-beta.7
---

`cassini meetings context` takes several meeting ids and prints one document holding all of them, in the order you named them, so a question can be asked of a set of meetings rather than one at a time. `--timestamps` cites where each passage starts, and `--local` reads portable `.opus` files off disk, needing none of the connection settings.

The same document is available inside the app. Pick meetings in the browse list, across rooms, and Prepare hands you all of them to copy or download. It is the same document the command prints for the same meetings in the same order, byte for byte, because the app asks the same producer for it rather than assembling its own.

Before it hands anything over, Prepare says what the bundle will not contain: how many of the picked meetings have no summary, how many predate the single file format, and whether the word count it shows is a total or a floor. The format has a published schema, so anything reading these documents can rely on their shape.
