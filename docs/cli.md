# CLI reference

`portless` is a thin client over the library.
`run`, `alias`, and `route` start a shared background daemon automatically if one is not already running, so several invocations share one proxy and stable URLs.

The control socket defaults to `$PORTLESS_SOCKET`, then `$XDG_RUNTIME_DIR/portless.sock`, then a per-user temp path.
The CA/state directory defaults to `$PORTLESS_STATE_DIR`, then `<user config dir>/portless`.

## run

```
portless run NAME [--port-env VAR] [--socket PATH] -- CMD [args...]
```

Runs a process on an OS-assigned port and gives it the stable name `NAME`.
Assigns a free port, passes it to the child as `$PORT` (override the variable with `--port-env`), registers `NAME`, streams the child's output, and deregisters on exit.
The child's exit code is propagated.

```sh
portless run web -- go run ./cmd/server
portless run api --port-env HTTP_PORT -- ./api-server
```

## alias

```
portless alias NAME HOST:PORT [--socket PATH]
```

Points `NAME` at an already-running service (a container, an external service).
This is name-aliasing, not port elimination — the counterpart to a running server's port.

```sh
portless alias db 127.0.0.1:5432
```

## serve

```
portless serve [--socket PATH] [--proxy ADDR] [--tls] [--no-proxy] [--state-dir DIR]
```

Runs the daemon: the forward proxy and the control API.
Usually started automatically by `run`/`alias`; run it explicitly to control its options.

- `--proxy ADDR` — proxy listen address (default `127.0.0.1:0`, an ephemeral port).
- `--tls` — terminate TLS so `https://<name>` works (clients must trust the CA; see `ca install`).
- `--no-proxy` — control API only, no forward proxy.

## route

```
portless route add NAME (--tcp HOST:PORT | --k8s-service NS/NAME [--target-port P] | --k8s-selector NS SELECTOR) [--kubeconfig PATH] [--socket PATH]
portless route list [--json] [--socket PATH]
portless route rm NAME [--socket PATH]
```

Lower-level route management.
`alias` is sugar for `route add --tcp`.

```sh
portless route add api --k8s-service prod/api --target-port 8080
portless route list --json
```

## env

```
portless env [--shell sh|fish|github] [--socket PATH]
```

Prints proxy environment exports so shell tools reach named routes.

```sh
eval "$(portless env)"                         # sh/bash/zsh
portless env --shell fish | source            # fish
portless env --shell github >> "$GITHUB_ENV"  # GitHub Actions
```

Output sets `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY=localhost,127.0.0.1,::1`.

## status

```
portless status [--json] [--socket PATH]
```

Prints daemon status: pid, version, proxy address, route count, uptime.

## doctor

```
portless doctor [NAME...] [--timeout DUR] [--socket PATH]
```

Waits for routes to become ready and reports timing — the wait-once primitive for CI.
With no names, checks every registered route.

```sh
portless doctor --timeout 60s     # wait once; subsequent curls are fast
```

## ca

```
portless ca path [--state-dir DIR]
portless ca install [--yes] [--state-dir DIR]
portless ca uninstall [--yes] [--state-dir DIR]
```

Manages the local HTTPS certificate authority used by `serve --tls`.

- `path` — generate the CA if needed and print its certificate path (for `curl --cacert`, `NODE_EXTRA_CA_CERTS`, or a browser import).
- `install` / `uninstall` — add or remove the CA from the OS trust store.
  These change system trust, so they confirm first (skip with `--yes`) and refuse in a non-interactive shell without `--yes`.
  May prompt for your password.

```sh
portless serve --tls &
portless ca install
curl https://web/healthz
```

## version

```
portless version
portless --version
```
