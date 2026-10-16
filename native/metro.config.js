const { getDefaultConfig } = require('expo/metro-config');

const config = getDefaultConfig(__dirname);

// Package `exports` must stay on.
//
// Generated protobuf code imports `@bufbuild/protobuf/codegenv2`, and connect
// imports `@connectrpc/connect/protocol*` and `@bufbuild/protobuf/wire|wkt`.
// None of those subpaths exist as files — they are only reachable through each
// package's `exports` map — so turning exports off (as this file used to) makes
// the bundle fail to resolve them.
//
// When a package exposes a nested file that its `exports` map does not list,
// Metro logs a warning and falls back to file-based resolution, so packages
// that predate export maps keep working.
config.resolver = {
  ...config.resolver,
  unstable_enablePackageExports: true,
};

module.exports = config;
