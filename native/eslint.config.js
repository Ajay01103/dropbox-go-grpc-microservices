// https://docs.expo.dev/guides/using-eslint/
const { defineConfig } = require('eslint/config');
const expoConfig = require("eslint-config-expo/flat");

module.exports = defineConfig([
  expoConfig,
  {
    // `src/gen` is produced by `buf generate` from the proto definitions; its
    // own `eslint-disable` header is all the control this repo has over it, and
    // linting it only reports the generator's formatting choices.
    ignores: ["dist/*", "src/gen/**"],
  }
]);
