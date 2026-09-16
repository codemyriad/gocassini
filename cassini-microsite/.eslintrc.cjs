module.exports = {
  // Build output and generated types are not source; linting them floods the
  // report with errors from minified bundles. public/embed/ is the published
  // viewer embed (D-775) — built from cassini-viewer and committed here, so it
  // is build output that happens to live under public/.
  ignorePatterns: ["dist/", ".astro/", "public/embed/"],
  env: {
    node: true,
    browser: true,
    es2024: true,
  },
  extends: [
    "eslint:recommended",
    "plugin:astro/recommended",
    "plugin:@typescript-eslint/recommended",
  ],
  parserOptions: {
    ecmaVersion: "latest",
    sourceType: "module",
  },
  rules: {
    semi: ["error", "always"],
    quotes: ["error", "double", { "allowTemplateLiterals": true }],
    "@typescript-eslint/triple-slash-reference": "off",
  },
  overrides: [
    {
      files: ["*.astro"],
      parser: "astro-eslint-parser",
      parserOptions: {
        parser: "@typescript-eslint/parser",
        extraFileExtensions: [".astro"],
      },
      rules: {},
    },
  ],
};
