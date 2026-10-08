# Security policy

## Reporting a vulnerability

Please **don't** open a public issue. Report it privately through GitHub: **[Report a vulnerability](https://github.com/ridoysheikh/syncloud/security/advisories/new)** (the repository's Security tab).

Include what's affected, how to reproduce it, and the impact you see. You'll get an acknowledgement within a few days. A fix is released as soon as it's ready, and you're credited in the advisory unless you prefer otherwise.

## Supported versions

Only the latest release gets security fixes while SynCloud is below 1.0. Upgrade with **Settings → Updates** ([Upgrades](docs/operations/upgrades.md)).

## Hardening

- Keep the recovery key offline. Turn on MFA, and require it for everyone (**IAM → Settings**).
- Use the root account only for setup. Give people and CI the narrowest [policies](docs/guides/access-control.md).
- Leave the host firewall and security groups on, which is the default.
- Back up the controller ([Backup and restore](docs/operations/backup-restore.md)).
