# Data retention

Every 6 hours the controller deletes old history:

| Data | Kept |
| --- | --- |
| Audit log | 365 days |
| Closed incidents | 90 days |
| Finished deployments | 90 days, and always the last 5 per service |
| Finished builds | 90 days, and always the deployed build of each service |
| Daily usage | 400 days |
| Task definitions (revisions) | the newest 50 per service, plus the current and previous revisions, those of in-flight deployments and those that running tasks use |
| Upgrade directories | the last 3 |

Other history has its own limits:

- Alert history is kept 90 days.
- Registry events are kept according to the registry settings.
- Scaling and pool histories keep their newest entries.

On each node, when the disk is above 85% full, the agent removes images that no container uses and that are older than a day.
