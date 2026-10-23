# Security policy

## Supported versions

Only the latest release receives security fixes. Upgrade before reporting a problem you found in an older one.

## Reporting a vulnerability

Report vulnerabilities privately through GitHub private vulnerability reporting:

<https://github.com/a-lafon/postgres-dba-mcp/security/advisories/new>

Do not open a public issue, pull request or discussion for a vulnerability. Include the version (`postgres-dba-mcp --version`), the access mode and transport, the PostgreSQL version, and the smallest input that shows the problem.

## In scope

- A bypass of restricted mode: a statement, function call or tool argument that changes data, session state or server files, or runs code, while `--access-mode=restricted` is set.
- A password disclosed in the server's logs, in an error, or in tool output.
- A bypass of the `Host` or `Origin` checks of the HTTP transports.

## Out of scope

- Reads of data the database role is allowed to read. The role is the barrier (see the README, sections Read-only role and Sensitive columns).
- Anything possible in `--access-mode=unrestricted`, which runs statements with the role's rights by design.
- Reaching an HTTP transport that was bound beyond loopback with `--allow-remote`: it has no authentication.
- Denial of service by a legal, expensive query, which `--statement-timeout` bounds.
