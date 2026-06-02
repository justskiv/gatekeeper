# Security Policy

## Reporting Vulnerabilities

Please report security issues privately to the project maintainers. Do
not publish bot tokens, Tribute API keys, Telegram webhook secrets,
database backups, raw provider payloads or invite URLs in public issues.

When reporting a vulnerability, include the affected version or commit,
the deployment mode, a minimal reproduction and the expected impact.
Redact secrets and personal data before sharing logs or payload samples.

## Secret Handling

Runtime secrets must be provided through environment variables or an
environment file with restrictive permissions. Do not bake secrets into
Docker images, systemd units or repository files.

SQLite databases and backups contain personal data and access state.
Treat database files, WAL sidecars and backup copies as secrets.
