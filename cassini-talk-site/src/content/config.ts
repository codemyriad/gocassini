import { defineCollection, z } from "astro:content";

const help = defineCollection({
  type: "content",
  schema: z.object({
    title: z.string(),
    description: z.string(),
    /** The category it sits under in the sidebar and on the help centre. */
    section: z.string(),
    updated: z.coerce.date(),
    /** "cassini.talk 0.1.0 for iPhone and iPad": the app and version the steps were checked against. */
    appliesTo: z.string(),
  }),
});

export const collections = { help };
