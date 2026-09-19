# D-793 tutorial

## Tag a recording with an appearance

In the viewer, create a tag from the picker, choose its colour/icon, and apply it. The resulting `.opus` embeds those fields under `annotations.tags[]`.

To inspect an archived recording:

```sh
./bin/cassini annotate show /absolute/path/to/recording.opus --json
```

## Change an existing tag

Open **Manage tags**, edit its name, colour or icon, then save. Cassini starts one background job over the meetings you can read that carry the tag. The manager shows progress; refresh after it completes.

Other rooms or recordings outside your access keep their archived tag name and appearance. A downloaded `.opus` and the public viewer read the saved fields directly.
