# Security policy

## Supported version

This project is pre-1.0. Security fixes target the latest `main` and latest
tagged release.

## Threat model

HarnessMesh launches powerful coding-agent CLIs against developer repositories.

The main risks are therefore not limited to the HarnessMesh binary itself:

- autonomous file modification,
- shell command execution,
- prompt injection from repository content,
- credential exposure through agent tools,
- untrusted test/build scripts,
- malicious or accidental peer feedback,
- runaway collaboration loops,
- sensitive source code sent to cloud providers.

## v0.1 controls

- one writable executor, one read-only reviewer
- explicit max rounds
- optional wall-clock limit
- per-agent timeout
- Claude Code max-turn and budget controls
- bounded context projection
- evidence required for reviewer findings
- repeated-finding loop detection
- no shell interpolation for `test_command`
- no browser-session or cookie proxying
- no credentials written by HarnessMesh reports
- config display redacts likely secret environment variable names

## Repository privacy

If an agent uses a cloud model, projected repository content may be sent to that
provider through the agent.

HarnessMesh cannot make a cloud-backed harness local merely by placing a proxy
in front of it.

Use local model backends or a policy-controlled Switchyard deployment when code
must remain on-device.

## Untrusted repositories

Do not run writable autonomous agents on an untrusted repository without an
appropriate sandbox/container.

Repository instructions, tests, build systems, hooks, MCP configuration and
tooling can all be attacker-controlled inputs.

## Reporting

Please report vulnerabilities privately through GitHub Security Advisories when
the repository is published.
