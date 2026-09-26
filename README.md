# unraid-runner-manager

A small web app for hosting self-hosted GitHub Actions and GitLab CI runners on Unraid without clicking through the Docker tab.

- Create a runner by pasting a repository or project URL and a token. Everything else, including a human-readable runner name like `fluffy-toaster`, the container name, and the data folder, is generated.
- Delete a runner and its data folder in one click.
- Multiple users. An admin creates accounts, every user sees only their own runners, and admins see everything. Container names and data folders are namespaced per user so runners never clash.
- Live container status and log streaming.
- No database of runners. The list is read live from Docker using labels on the containers. The only stored data is user accounts and sessions in a SQLite file.

GitHub runners use the [myoung34/github-runner](https://github.com/myoung34/docker-github-actions-runner) image with the same environment and mounts as the Community Applications template. GitLab runners use the official [gitlab/gitlab-runner](https://docs.gitlab.com/runner/install/docker/) image with the Docker executor. Both get `--restart=always` so they come back after a reboot.

## How it works

The app talks to the Docker Engine through the socket. Runner containers carry `runner-manager.*` labels that hold the owner, repository, runner name, and host data path, so the app can rebuild its view from Docker alone at any time. Deleting a runner removes the container and then the data folder through a bind mount of the runner data root.

Runners created this way show up in the Unraid Docker tab but have no dockerMan template, so Unraid offers Start, Stop, Logs, and Remove there but not Edit. Manage them from this app instead.

GitHub runners register with a single-use token, so the app cannot deregister them from GitHub. After deleting one here, remove the offline entry under the repository's Settings → Actions → Runners. GitLab runners keep their authentication token in `config.toml` inside the data folder, so the app removes them from GitLab as part of delete.

## Naming convention

| Thing | Pattern | Example |
| --- | --- | --- |
| Runner name | `<adjective>-<noun>` or `<adjective>-<adjective>-<noun>` | `brave-little-teapot` |
| GitHub container | `<CONTAINER_PREFIX>-<user>-<runner>` | `Github-Runner-max-brave-little-teapot` |
| GitLab container | `<GITLAB_CONTAINER_PREFIX>-<user>-<runner>` | `Gitlab-Runner-max-brave-little-teapot` |
| Data folder on the host | `<runner data root>/<user>/<runner>` | `/mnt/user/appdata/github-runners/max/brave-little-teapot` |
| GitHub work directory | `<data folder>/work` | `/mnt/user/appdata/github-runners/max/brave-little-teapot/work` |
| GitLab config, cache, builds | `<data folder>/config`, `/cache`, `/builds` | `/mnt/user/appdata/github-runners/max/brave-little-teapot/config` |

For GitHub the generated name is also the runner name shown in the repository. For GitLab the name shown in the project is the description you typed when creating the runner there; the generated name is used for the container and folder only.

## Using the app

### First sign-in

The administrator creates your account and gives you a temporary password. Sign in with it and the app asks you to pick your own password before showing anything else.

### Create a GitHub runner

1. On GitHub open the repository that should get the runner, go to **Settings → Actions → Runners**, and click **New self-hosted runner**. Ignore the download and configure commands. Copy only the value after `--token` from the configure step. The token is valid for one hour and can be used once.
2. In the app click **New runner** and pick **GitHub**.
3. Paste the repository as `owner/repo` or as its GitHub URL, paste the token, and optionally add comma-separated labels such as `toaster, gpu`.
4. Click **Create runner**. The app picks a name like `brave-little-teapot`, pulls the runner image if needed, creates the container, and starts it. The page shows the progress and switches to the log view once the container is up.
5. Back in GitHub, the runner appears under **Settings → Actions → Runners** as **Idle** within a minute or two. If it does not, open the runner in the app and check the logs. A wrong or expired token shows up there as a registration error, in which case delete the runner and create it again with a fresh token.

Use it in a workflow with the default labels or one of yours:

```yaml
jobs:
  build:
    runs-on: [self-hosted, linux, x64]
```

### Create a GitLab runner

1. On GitLab open the project, go to **Settings → CI/CD → Runners**, and click **New project runner**. Set the tags and description there, decide whether it runs untagged jobs, and leave **Lock to current projects** off if other projects of yours should be able to use it. Create it and copy the `glrt-` token from the next page. Skip the install and register steps shown there.
2. In the app click **New runner** and pick **GitLab**.
3. Paste the project URL, for example `https://gitlab.com/max/toy-gallery`, and the token. Self-managed instances work the same way, the host in the URL is the one the runner registers with.
4. Click **Create runner**. The app checks the token with GitLab right away, writes the runner's `config.toml` into its data folder, pulls the image if needed, and starts the container. A rejected token fails immediately with that reason.
5. Back in GitLab, the runner shows as online under **Settings → CI/CD → Runners** within a minute or two.

Jobs run with the Docker executor: each job gets a fresh container from the image named in `.gitlab-ci.yml`, or the default job image when none is named. Jobs can use `docker` because the socket is bound into job containers. To share the runner with your other projects, enable it under **Other available runners** in each project's CI/CD settings.

```yaml
build:
  image: golang:1.27
  tags: [toaster]
  script: go build ./...
```

Runners are started with `restart=always`, so they survive an Unraid reboot without any manual action.

### Day to day

- The **Runners** page refreshes on its own and mirrors the container state: running, stopped, paused, or creating. Use the buttons on each row to stop, start, or resume a runner.
- Click a runner name to see its details and a live log stream. Pick 100, 500, or 2000 lines and turn **Follow** off to scroll back.
- Admins see every runner with an **Owner** column and can switch between **All users** and **Mine**. Other users see only their own.

### Keeping runner images up to date

The GitHub runner agent updates itself. Runner container images do not, so the app labels every runner for [Watchtower](https://github.com/nicholas-fedor/watchtower), and one Watchtower container on the server keeps them current. Use the maintained `nickfedor/watchtower` image; the original `containrrr/watchtower` was archived in December 2025.

Run Watchtower with these settings, so it only touches runners and never interrupts a job:

| Setting | Value | Why |
| --- | --- | --- |
| `WATCHTOWER_LABEL_ENABLE` | `true` | Only containers carrying the enable label are considered. Runners have it; nothing else on your server does. |
| `WATCHTOWER_LIFECYCLE_HOOKS` | `true` | Runs the job check inside GitHub runners before updating them. |
| `WATCHTOWER_TIMEOUT` | `30m` | How long a container may take to stop. GitLab runners finish the running job first. |
| `WATCHTOWER_CLEANUP` | `true` | Removes old images. |
| `WATCHTOWER_SCHEDULE` | `0 0 4 * * *` | Check once a day at 04:00. |

Mount `/var/run/docker.sock` into Watchtower as usual. What the labels do:

- Both kinds get `com.centurylinklabs.watchtower.enable=true`.
- GitHub runners get a pre-update hook that looks for a running `Runner.Worker` process and exits with code 75 when one exists, which makes Watchtower skip that runner until the next check.
- GitLab runners get `com.centurylinklabs.watchtower.stop-signal=SIGQUIT`. The GitLab runner treats that signal as "finish the current job, then exit".

Watchtower recreates a container from its own configuration, so labels, environment, mounts, and the data folder all survive, and the runner keeps its registration. Runners created before this feature do not have the labels; delete and recreate them once. Set `WATCHTOWER_LABELS=false` on the app if you do not want the labels at all.

### Delete a runner

Click **Delete** on the runner and confirm. This removes the container and its data folder on the server. A GitLab runner is also removed from GitLab using the token stored in its config. A GitHub runner keeps showing as **Offline** in the repository until you remove it under **Settings → Actions → Runners**, because the app has no GitHub credentials.

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
| `RUNNER_IMAGE` | `myoung34/github-runner:latest` | Image used for new GitHub runners. |
| `CONTAINER_PREFIX` | `Github-Runner` | Prefix for GitHub runner container names. |
| `GITLAB_RUNNER_IMAGE` | `gitlab/gitlab-runner:latest` | Image used for new GitLab runners. |
| `GITLAB_JOB_IMAGE` | `alpine:latest` | Image GitLab jobs run in when the pipeline does not name one. |
| `GITLAB_CONTAINER_PREFIX` | `Gitlab-Runner` | Prefix for GitLab runner container names. |
| `UNRAID_HOSTNAME` | `Tower` | Passed to runners as `HOST_HOSTNAME`. |
| `TZ` | `UTC` | Timezone passed to runners. |
| `LISTEN_ADDR` | `:8080` | Listen address. |
| `WATCHTOWER_LABELS` | `true` | Label runner containers for Watchtower. See [Keeping runner images up to date](#keeping-runner-images-up-to-date). |
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

Runner containers are created by the Docker daemon, so the bind mounts they get must be paths the daemon can see. Inside the dev container that is the path of the workspace on your machine, not `/workspaces/...`. The dev container exports it as `LOCAL_WORKSPACE_FOLDER` and `make run` uses it for `RUNNER_DATA_HOST_ROOT`. If you started the dev container before that variable existed, pass it by hand: `make run LOCAL_WORKSPACE_FOLDER=/path/to/unraid-runner-manager`. With the wrong path a GitLab runner fails with `Failed to load config stat /etc/gitlab-runner/config.toml`.

Layout:

- `cmd/unraid-runner-manager` entry point
- `internal/runner` runner lifecycle on top of Docker
- `internal/provider` the provider interface, with `github` and `gitlab` implementations that own parsing, registration, container spec, and deregistration
- `internal/dockerapi` Docker client interface and the moby implementation, `internal/dockerfake` an in-memory fake for tests
- `internal/web` HTTP handlers, templates, and static files
- `internal/store` SQLite users and sessions
- `internal/auth` password hashing and sign-in rate limiting
- `internal/names` runner name generator
- `deploy` Unraid template

## CI

Pull requests run lint, the test suite, and a Docker build. Merges to `main` run the same checks and then publish a multi-arch image to `ghcr.io/klice/unraid-runner-manager` tagged `latest` and `sha-<commit>`. Tags matching `v*` add semver tags.
