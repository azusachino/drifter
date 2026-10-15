# HTTP server

The separate `drifter-server` executable starts without provider credentials.
It reads `WEREAD_API_KEY` and `HARDCOVER_API_KEY` from the runtime environment
only when needed. Configuration is strict settings-only TOML; keep secrets out
of TOML files.

## Container on Linux

Compose uses host networking so strict Host/Origin checks see the same
`127.0.0.1:8081` authority as the browser. There is deliberately no `ports`
mapping: the example config binds only to `127.0.0.1`, and wildcard listening
is not supported. Host networking gives the container direct access to the
Linux host network namespace; use it only on a trusted local host. Docker
Desktop's host networking behavior differs and is not the target of this setup.

The example TOML is bind-mounted read-only with a private SELinux label. Replace
its source with your own settings-only TOML if needed; do not put credentials in
it. State uses a Compose-managed named volume. On first use, the engine copies
the image's pre-created `/var/lib/drifter` directory into that empty volume;
it is owned by uid/gid `10001` with mode `0700`. The database and owner lock are
created mode `0600`, and the volume persists across restarts and ordinary
`compose down`. Use `compose down --volumes` only when intentionally deleting
that local state.

Build the image and export provider credentials in the current shell only if
provider requests are needed. Startup itself needs neither key:

```sh
podman build -f Containerfile -t drifter-server:local .
```

On Linux, start with `podman compose --env-file /dev/null up -d` or
`docker compose --env-file /dev/null up --build -d`. `/dev/null` prevents
Compose from loading a local `.env`; provider variables come only from the
shell environment. Do not commit credentials or a `.env` file.

On a Podman machine, the external Compose provider may need its API socket. Set
it in the shell, then run the same Podman Compose command:

```sh
export DOCKER_HOST="unix://$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}')"
podman compose --env-file /dev/null up -d
```

That machine's host network namespace is the Linux VM's, not macOS's; this is
not evidence that macOS can reach the loopback listener. On Linux, test access
from the Linux host. Compose sends SIGTERM and allows 30 seconds for graceful
shutdown. Stop with Ctrl-C or `podman compose down` / `docker compose down`.

## Rootless runtime verification

Verified with Podman 5.8.7 on the macOS-hosted Linux/arm64 VM (`rootless=true`,
SELinux `Enforcing`). The Compose smoke used a unique project, temporary
credential-free TOML (`server.addr = "127.0.0.1:0"`) and the existing local
image. A temporary override replaced only the config bind source; it used the
same read-only mount and private SELinux label. The tested invocation was:

```sh
socket=$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}')
env -u WEREAD_API_KEY -u HARDCOVER_API_KEY \
  DOCKER_HOST="unix://$socket" \
  podman compose --env-file /dev/null -p drifter-rootless-smoke \
  -f compose.yaml -f "$temporary_override" up -d --no-build --pull never
```

The temporary override mounted its TOML read-only with `bind.selinux: Z`;
SELinux relabeling fixed the earlier rootless bind-read denial. The named state
volume was copied from the image directory and had mode `0700`, uid/gid
`10001:10001`; the database and owner lock were mode `0600`. All four
credential-free GETs (`/healthz`, `/readyz`, `/`, `/api/mappings`) returned
200; mappings were `[]`. A concurrent server using that DB was refused as
owned/locked. Compose SIGTERM stop exited 0; starting the same service again
returned ready and mappings successfully, with the DB inode, size, mode and
owner unchanged. The volume and temporary config were removed with that unique
project's `compose down --volumes`; no images were pruned.

The probe helper joined the server container's network namespace, so these
results test the Linux VM's loopback only. Rootless verification did not test
macOS reachability or a rootful Docker engine.

The full Compose lifecycle smoke above predates extraction of the startup runner
into `internal/server`. After that source change, the focused runner test,
current image rebuild and image help check passed:

```sh
go test ./internal/server -count=1
podman build -f Containerfile -t drifter-server:local .
env -u WEREAD_API_KEY -u HARDCOVER_API_KEY \
  podman run --rm --pull=never drifter-server:local --help
```

The runner test covers help, sanitized parse errors, no-credential startup,
health, shutdown and `database.path` precedence. The full Compose lifecycle was
not repeated; its earlier results apply to the unchanged Compose/volume/network
configuration, while current runner behavior is evidenced by the Go tests.

These owning gates passed:

```sh
make check
mise exec -- rumdl check README.md docs/architecture.md docs/cli.md \
  docs/plans/http-server.md docs/plans/matching-worklist.md \
  docs/plans/matching-catalogs.md docs/plans/read-only-matching.md \
  docs/walkthroughs/README.md docs/server.md
make e2e-probe ROUTE=/
make e2e
git diff --check
```
