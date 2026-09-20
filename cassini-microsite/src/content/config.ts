import { defineCollection, z } from "astro:content";

const docs = defineCollection({
  type: "content",
  schema: z.object({
    title: z.string(),
    description: z.string().optional(),
    // Where this page was copied from in the gocassini repo, and when. The site
    // docs mirror vetted repo docs by hand, so these two make the drift
    // visible: diff the page against `source` as of `copied`.
    source: z.string().optional(),
    copied: z.string().optional(),
  }),
});

const changelog = defineCollection({
  type: "content",
  schema: z.object({
    title: z.string(),
    date: z.coerce.date(),
    // The release an entry shipped in, so a reader can tell what to install.
    version: z.string().optional(),
    author: z.string().optional(),
    draft: z.boolean().optional(),
  }),
});

export const collections = { docs, changelog };
