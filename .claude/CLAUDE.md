<!--
GENERATED FILE - DO NOT EDIT DIRECTLY
generator: gds
bundle: 0.4.0-dev
source-tree-digest: sha256:f9f9787082b9f3b25ba11ec267f28c58a5d00a61e3a59c77a868756f360a901c
input-digest: sha256:51828487bd1128260a8feb5dead1dfc51f30c11b5fb209ccc57cbb459709d4d6
output-digest: sha256:88cb57297d8d713287872a8afaca8d42f7146ecf7a091e4996e65eee8f962665
edit-source:
  - .gds/repository.yaml
  - policies/base/repository-default.yaml
  - policies/owners/organization-default.yaml
  - policies/repositories/github-actions.yaml
  - templates/agents/repository.md.tmpl
  - templates/github-actions/go.yml.tmpl
  - templates/harnesses/claude.md.tmpl
-->
@AGENTS.md

# Claude Code delta

`AGENTS.md` above is the repository brief and is imported, not restated. This
file carries only what differs for Claude Code.

- Skills load on task match, not on arrival. A task contained in this repository
  goes straight to the relevant source and its verification command; `gds-orient`
  is for estate, device, topology and cross-repository scope.
- Serena memories under `.serena/memories/` are derived evidence. Their status
  records that sources were current and the body is the reviewed one; it does not
  certify the prose is correct.
- Prefer the repository's own commands over ad-hoc equivalents, so a failure is
  reproducible from the brief alone.
