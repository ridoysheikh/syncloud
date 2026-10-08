# Access control (IAM)

Everyone signs in as a **user**. What they may do comes from **policies** attached to them, to their **groups**, or to a **role** they assume. Every request is checked, and every change is in the audit log.

## Users and service accounts

| Kind | Signs in with | For |
| --- | --- | --- |
| **Person** | email and password, plus MFA (TOTP) | people |
| **Service account** | access keys and tokens only | CI and automation |

Create them under **IAM → Users**, or:

```sh
synctl iam users create ann@example.com --name "Ann"
synctl iam users create ci-shop --kind service
```

The **root account** created at setup bypasses every check. Use it for setup and emergencies only.

## Policies

Built-in (managed) policies cover the usual cases. The per-project ones are attached as `NAME:project`:

| Policy | Grants |
| --- | --- |
| `AdministratorAccess` | everything |
| `ReadOnly` | read everything, change nothing |
| `BillingViewer` | quotas and usage |
| `ProjectOwner:shop` | everything in one project, including its security groups and who has access |
| `Developer:shop` | build, deploy, operate and debug the project's services |
| `Deployer:shop` | read the project and deploy, scale and roll back (for CI) |

```sh
synctl iam attach Developer:shop --user ann@example.com
synctl iam attach Deployer:shop --user ci-shop
synctl iam groups create backend && synctl iam attach Developer:shop --group backend
```

**Custom policies** are JSON documents with `Allow` and `Deny` statements over actions (`service:UpdateService`) and resources (`srn:syncloud:service/shop/production/api`). Edit them under **IAM → Policies**. `synctl iam actions` lists every action.

**Check before you grant:**

```sh
synctl iam simulate ann@example.com service:DeleteService srn:syncloud:service/shop/production/api
```

## Roles and temporary credentials

A **role** grants its policies to whoever assumes it, for a limited time (15–720 minutes), optionally only with MFA:

```sh
synctl sts assume-role deployer
```

## Your own credentials

On **IAM → My security**:

- your password and MFA;
- **access keys** (at most two, so you can rotate them);
- personal access tokens.

Account-wide settings, such as **require MFA for everyone**, are under **IAM → Settings**.

## Audit log

**IAM → Audit log** records who did what, where and when, including denied attempts, shells, revealed passwords and SQL run with writes allowed:

```sh
synctl audit --actor ann@example.com --since 24h
```

---

Related: [Network security](network-security.md)
