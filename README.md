# unraid-runner-manager

A small web app for hosting self-hosted GitHub Actions runners on Unraid without clicking through the Docker tab.

- Create a runner by pasting a repository and a registration token. Everything else, including a human-readable runner name like `fluffy-toaster`, the container name, and the data folder, is generated.
- Delete a runner and its data folder in one click.
- Multiple users. An admin creates accounts, every user sees only their own runners, and admins see everything. Container names and data folders are namespaced per user so runners never clash.
- Live container status and log streaming.
- No database of runners. The list is read live from Docker using labels on the containers. The only stored data is user accounts and sessions in a SQLite file.

Runners use the [myoung34/github-runner](https://github.com/myoung34/docker-github-actions-runner) image with the same environment and mounts as the Community Applications template, plus `--restart=always` so they come back after a reboot.

## How it works

The app talks to the Docker Engine through the socket. Runner containers carry `runner-manager.*` labels that hold the owner, repository, runner name, and host data path, so the app can rebuild its view from Docker alone at any time. Deleting a runner removes the container and then the data folder through a bind mount of the runner data root.

Runners created this way show up in the Unraid Docker tab but have no dockerMan template, so Unraid offers Start, Stop, Logs, and Remove there but not Edit. Manage them from this app instead.

Because runners register with a single-use token, the app cannot deregister them from GitHub. After deleting a runner here, remove the offline entry under the repository's Settings → Actions → Runners.

## Naming convention

| Thing | Pattern | Example |
| --- | --- | --- |
| Runner name in GitHub | `<adjective>-<noun>` or `<adjective>-<adjective>-<noun>` | `brave-little-teapot` |
| Container | `<CONTAINER_PREFIX>-<user>-<runner>` | `Github-Runner-max-brave-little-teapot` |
| Data folder on the host | `<runner data root>/<user>/<runner>` | `/mnt/user/appdata/github-runners/max/brave-little-teapot` |
| Work directory | `<data folder>/work` | `/mnt/user/appdata/github-runners/max/brave-little-teapot/work` |

## Install on Unraid

1. In the Docker tab choose **Add Container**, switch to advanced view, and paste the template URL: `https://raw.githubusercontent.com/Klice/unraid-runner-manager/main/deploy/unraid-runner-manager.xml`. Or copy `deploy/unraid-runner-manager.xml` to `/boot/config/plugins/dockerMan/templates-user/` and pick it from the template dropdown.
2. Set an admin password. It is used only once, to create the first admin account when the database is empty.
3. Check the runner data root path. Deleting a runner deletes its sub-folder here.
4. Start the container and open the WebUI. Sign in with the admin account, then create users under **Users**.

The app runs as root inside its container because the runner image writes root-owned files into the data folders and the app has to be able to delete them.

### Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADMIN_PASSWORD` | | Password for the first admin account. Required on first start only. At least 10 characters. |
| `ADMIN_USERNAME` | `admin` | Username for the first admin account. |
| `RUNNER_DATA_DIR` | `/runners` | Where the runner data root is mounted inside this container. |
| `RUNNER_DATA_HOST_ROOT` | detected | Host path of the runner data root. Detected from the container's own mounts when empty. |
| `DATA_DIR` | `/config` | Folder for the SQLite database. |
| `RUNNER_IMAGE` | `myoung34/github-runner:latest` | Image used for new runners. |
| `CONTAINER_PREFIX` | `Github-Runner` | Prefix for runner container names. |
| `UNRAID_HOSTNAME` | `Tower` | Passed to runners as `HOST_HOSTNAME`. |
| `TZ` | `UTC` | Timezone passed to runners. |
| `LISTEN_ADDR` | `:8080` | Listen address. |
| `SECURE_COOKIES` | `false` | Set to `true` behind an HTTPS reverse proxy. |
| `SESSION_TTL` | `720h` | How long a sign-in lasts. |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker endpoint. |

Required mounts: `/var/run/docker.sock`, the runner data root at `RUNNER_DATA_DIR`, and a folder at `DATA_DIR` for the database.

### Security notes

Anything with access to the Docker socket is effectively root on the host. Keep the app on your LAN or behind a VPN, do not port-forward it, and give accounts only to people you trust with your server. Sign-in is rate limited, passwords are hashed with argon2id, and state-changing requests are rejected when they come from another site.

## Development

Open the repository in the dev container, or install Go 1.27, golangci-lint, and Docker locally.

```sh
make help          # list targets
make test          # full test suite with the race detector
make lint          # go vet + golangci-lint
make run           # run locally against the local Docker socket with a dev admin
make docker-build  # build the image
```

`make run` uses `.dev/` for the database and runner data, creates an `admin` user with the password from `DEV_ADMIN_PASSWORD` (default `change-me-please`), and listens on http://localhost:8080. Set `RUNNER_IMAGE=alpine:3.20` if you want to exercise create and delete without pulling the real runner image.

Layout:

- `cmd/unraid-runner-manager` entry point
- `internal/runner` runner lifecycle on top of Docker
- `internal/dockerapi` Docker client interface and the moby implementation, `internal/dockerfake` an in-memory fake for tests
- `internal/web` HTTP handlers, templates, and static files
- `internal/store` SQLite users and sessions
- `internal/auth` password hashing and sign-in rate limiting
- `internal/names` runner name generator
- `deploy` Unraid template

## CI

Pull requests run lint, the test suite, and a Docker build. Merges to `main` run the same checks and then publish a multi-arch image to `ghcr.io/klice/unraid-runner-manager` tagged `latest` and `sha-<commit>`. Tags matching `v*` add semver tags.
