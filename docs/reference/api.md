# API

Everything the dashboard and synctl do goes through one HTTP API under `/api/v1`.

## Where to find it

| | |
| --- | --- |
| **Interactive reference** | **API & CLI** in the dashboard (`/developers`): every operation with its IAM action, and a request builder |
| **OpenAPI 3** | `GET https://<dashboard>/api/v1/openapi.json` |
| **Live events** | WebSocket `wss://<dashboard>/api/v1/stream` (services, tasks, nodes, deployments…) |

## Authentication

| Method | Use |
| --- | --- |
| **Signed requests** with an access key | scripts and CI. The scheme is SigV4-style, as the SDKs and synctl do it. |
| **Personal access token** | `Authorization: Bearer syn_pat_…`, for quick scripts |
| **Session cookie** | the dashboard |

Every request is checked against the caller's [IAM policies](../guides/access-control.md). Errors are JSON, in the form `{"error": {"code", "message"}}`, with conventional status codes. For example, `423` means a [locked environment](../guides/deployments.md#locking-an-environment).

## SDKs

- **Go:** `go get github.com/ridoysheikh/syncloud/sdk/go/syncloud`
- **TypeScript:** in `sdk/typescript`

```go
c, err := syncloud.New("https://dashboard.example.com", syncloud.Credentials{
	AccessKeyID:     os.Getenv("SYNCLOUD_ACCESS_KEY_ID"),
	SecretAccessKey: os.Getenv("SYNCLOUD_SECRET_ACCESS_KEY"),
})
svcs, err := c.ListServices(ctx, "shop", "production")
```

The quickest way to try an endpoint is `synctl api GET /api/v1/services`.

## Pagination

History lists (deployments, builds, job runs, events, audit) take `limit` and `before` (a cursor), and answer `{"items": [...], "next": "<cursor>"}`. `next` is empty at the end.
