<!-- CODEGRAPH_START -->

## CodeGraph

In repositories indexed by CodeGraph (a `.codegraph/` directory exists at the repo root), reach for it BEFORE grep/find or reading files when you need to understand or locate code:

- **MCP tool** (when available): `codegraph_explore` answers most code questions in one call — the relevant symbols' verbatim source plus the call paths between them, including dynamic-dispatch hops grep can't follow. Name a file or symbol in the query to read its current line-numbered source. If it's listed but deferred, load it by name via tool search.
- **Shell** (always works): `codegraph explore "<symbol names or question>"` prints the same output.

If there is no `.codegraph/` directory, skip CodeGraph entirely — indexing is the user's decision.
<!-- CODEGRAPH_END -->

## Generate Code Locally Using Remote Plugins

Use the locally installed Buf CLI to validate local Protobuf schemas and generate code using plugins executed on a remote Buf Schema Registry (BSR) server.

Required execution model
Protobuf source files (.proto) reside in the local repository.
The Buf CLI runs locally in the configured development environment.
Code-generation plugins execute remotely through the configured BSR server.
Generated files are written to the configured local output directories.
Do not require local protoc-gen-* binaries for plugins configured as remote.

Remote plugin execution does not imply that source files or generated output must be stored remotely.

1. Inspect the Repository

Before making changes, inspect:

buf.yaml — workspace and module configuration.
buf.gen.yaml — remote plugins, versions, options, and output directories.
buf.lock — dependency lockfile, if present.
.proto files — schemas and imports.
Makefile, Taskfile.yml, package scripts, and CI workflows — existing generation commands.
Generated-code directories and .gitignore rules.

Preserve the existing project structure, naming conventions, plugin versions, and generation workflow.

Do not create duplicate configurations or replace working project conventions without a clear reason.

2. Verify the Environment

Ensure the local development environment has:

A compatible Buf CLI version.
Network access to the configured BSR server.
Required authentication and authorization.
Access to all configured remote plugins.

Check the CLI version:

buf --version

Check the configuration:

buf config ls

Follow the project's documented authentication procedure for the configured BSR server.

Never hardcode credentials or tokens in repository files, scripts, or configuration.

Do not install local plugin binaries solely to use plugins already configured for remote execution.

3. Configure Remote Plugins

Use the existing buf.gen.yaml whenever possible.

For a Buf v2 configuration, remote plugins must use the remote field.

Example:

version: v2

plugins:

- remote: <bsr-host>/<organization>/<plugin-name>:<version>
  out: gen/go
  opt:
  - paths=source_relative

Replace placeholders with the actual server hostname, organization, plugin name, and pinned version.

Configuration rules:

Use remote: for every plugin that must execute remotely.
Use the correct hostname for the configured BSR server.
Pin plugin versions and revisions where supported and required.
Preserve existing plugin options and output directories.
Include every generator required by the project.
Follow the project's existing package and output conventions.
Do not guess plugin names, versions, organizations, or server addresses.
Do not replace a private BSR hostname with buf.build unless explicitly required.
Do not replace remote plugins with local: or protoc_builtin: as a workaround for remote failures.

If required server or plugin details are unavailable, report the missing configuration rather than inventing values.

4. Validate and Generate Code

Run commands from the repository root unless project documentation specifies otherwise.

Lint Protobuf schemas
buf lint

Fix relevant schema or lint errors before generation. Do not weaken lint rules merely to make generation succeed.

Generate code
buf generate

If a specific template is required:

buf generate --template path/to/buf.gen.yaml

Use the project's canonical generation command when one exists.

The generation workflow must:

Read the configured local Protobuf sources.
Resolve the configured dependencies.
Invoke the configured remote plugins.
Retrieve the generated output.
Write generated files to the configured local output directories.

Do not manually reproduce this workflow using direct plugin binaries or unrelated generation tools.

5. Preserve Remote Execution

Remote plugin execution is mandatory for generators configured as remote.

Do not silently fall back to locally installed plugins.
Do not download arbitrary plugin binaries as a workaround.
Do not switch to another server without authorization.
Do not disable TLS verification or bypass authentication controls.
Do not change plugin versions merely to suppress generation differences.
Do not expose secrets in logs, source code, generated output, or error reports.
Do not modify server configuration unless the task explicitly requires it.

If a remote plugin fails, report the plugin reference and the relevant error. Investigate the hostname, permissions, plugin name, version, and server availability.

Stop generation if remote execution cannot be completed. Never report successful remote generation when it has not occurred.

6. Manage Dependencies

Inspect buf.yaml and buf.lock before changing dependencies.

Follow the repository's existing dependency-management workflow.
Preserve pinned dependency versions and lockfile consistency.
Avoid unnecessary dependency updates.
Do not remove dependencies merely to resolve an unrelated generation error.
Verify that imported Protobuf definitions resolve correctly.

Only update dependencies when required by the task or to resolve a verified compatibility issue.

7. Handle Protobuf Schema Changes

When modifying Protobuf definitions:

Edit the appropriate .proto files.
Preserve existing field numbers and wire compatibility.
Reserve deleted field numbers and names when appropriate.
Run configured lint checks.
Run breaking-change checks if supported by the repository.
Regenerate code using the configured remote plugins.
Review generated output and downstream references.
Run relevant tests.

Do not manually edit generated files to implement changes that belong in Protobuf definitions or generator configuration.

Do not introduce breaking API changes without explicit authorization.

8. Verify Generated Output

After generation, run:

git status --short
git diff --check
git diff --stat

Verify that:

Expected files were generated.
Output paths and package names are correct.
Generated code matches the intended schema changes.
No unrelated files were modified.
Generated artifacts follow the repository's version-control policy.

Inspect generated changes before committing them.

Do not delete generated directories indiscriminately or overwrite unrelated developer changes.

9. Troubleshooting
   Buf CLI missing or incompatible

Check buf --version. Install or select the version required by the project.

Authentication or authorization failure

Verify the documented login procedure, server hostname, credentials, and access permissions. Never print or commit tokens.

Remote plugin resolution failure

Verify the server hostname, organization, plugin name, version, revision, and plugin availability.

Import or dependency failure

Inspect buf.yaml, dependency declarations, and buf.lock. Resolve dependencies using the established project workflow.

Unexpected generated-code changes

Check the schema changes, remote plugin versions, plugin revisions, options, managed-mode settings, and output paths.

Remote server unavailable

Report the failure and stop. Do not silently substitute local plugins or another server.

10. Definition of Done

A generation task is complete when:

The requested Protobuf changes are implemented.
Schema validation passes.
The configured remote plugins execute successfully.
Generated code is written to the correct local directories.
Relevant compatibility checks and tests pass.
No unrelated changes are introduced.
Any unresolved server, authentication, or plugin issue is reported clearly. 11. Completion Report

Summarize:

Protobuf files changed.
Remote plugins and versions used.
Commands executed and their outcomes.
Generated directories or files changed.
Validation, compatibility checks, and tests performed.
Any unresolved configuration or remote-server issues.
Official Documentation
Buf remote plugins: https://buf.build/docs/generate/remote-plugins/
Buf v2 generation configuration: https://buf.build/docs/configuration/v2/buf-gen-yaml/
Buf CLI reference: https://buf.build/docs/reference/cli/buf/generate/
