# The synctl CLI

`synctl` does everything the dashboard does, so it works in scripts and CI.

## Get it

From the GitHub release (Linux, macOS and Windows):

```sh
# Linux or macOS: set OS to linux or darwin, ARCH to amd64 or arm64
OS=linux ARCH=amd64
curl -fsSLo synctl https://github.com/ridoysheikh/syncloud/releases/latest/download/synctl-$OS-$ARCH
chmod +x synctl && sudo mv synctl /usr/local/bin/
```

Other ways to get it:

- From your own controller: **API & CLI** in the dashboard, or `https://<dashboard>/downloads/synctl-linux-amd64`. The controller always serves the matching version.
- With Go: `go install github.com/ridoysheikh/syncloud/cmd/synctl@latest`.

## Sign in

Choose one:

- **In the browser:** run `synctl login --endpoint https://<dashboard>` and approve the code in the dashboard. The credentials last 12 hours.
- **With an access key,** for CI and scripts: create a key under **IAM → My security**, or with `synctl iam access-keys create`, then run:

  ```sh
  synctl configure      # asks for the URL, key ID and secret
  ```

  Or set `SYNCLOUD_ENDPOINT`, `SYNCLOUD_ACCESS_KEY_ID` and `SYNCLOUD_SECRET_ACCESS_KEY`.

Credentials live in `~/.syncloud/credentials`. Use `--profile NAME` (or `SYNCLOUD_PROFILE`) to keep several clusters apart.

## Everyday use

```sh
export SYNCLOUD_PROJECT=shop          # instead of -p shop on every command
synctl services list
synctl services run web --image nginx:1.27 --port 80
synctl logs -f web
synctl exec service/web -- sh
synctl services rollback web 3
```

- **Environment:** `-e staging` picks the environment. The default is `production`.
- **JSON output:** `-o json` prints JSON for scripts.
- **Any other endpoint:** `synctl api GET /api/v1/...` sends a signed request to any API endpoint.

The [synctl reference](../reference/synctl.md) lists every command.

## In CI

Create a **service account** with the **Deployer** policy for one project ([Access control](../guides/access-control.md)), give CI its access key, and deploy with:

```sh
docker push registry.<base-domain>/shop/api:$GIT_SHA          # the built-in registry
synctl services run api -p shop --image @registry/shop/api:$GIT_SHA --port 8080
```
