# Security Policy

## Reporting a vulnerability

Report authentication bypasses, unsafe feed parsing, repository injection, GitHub token exposure, queue abuse, or admin-endpoint vulnerabilities through [GitHub Security Advisories](https://github.com/starcat-app/starcat-weekly-api/security/advisories/new). Do not publish API keys, GitHub tokens, database contents, source payloads, or production logs in an issue.

Security fixes target the current default branch and latest deployed version. Runtime secrets must be injected through environment variables or Fly.io secrets and must never be committed.
