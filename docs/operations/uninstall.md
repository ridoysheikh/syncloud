# Uninstall

```sh
sudo syncloud-controller uninstall [--purge]    # on the controller host
sudo syncloud-agent uninstall [--purge]         # on a worker
```

Uninstall removes:

- the systemd units,
- the SynCloud containers and Docker networks,
- the WireGuard interface `wg-syncloud`,
- the nftables tables.

Data is kept unless you pass `--purge`, which also deletes the data directories and the platform volumes. `install.sh --uninstall [--purge]` runs the same commands and then removes the binaries.
