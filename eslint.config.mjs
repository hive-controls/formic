// Mirrors the workshop's flat config: .mts sources are type-checked by tsc, not linted.
export default [
  {
    ignores: ["node_modules/**", "fixtures/**"],
  },
  {
    files: ["**/*.js", "**/*.mjs", "**/*.ts"],
    rules: {},
  },
];
