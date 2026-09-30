# mailsink

> [!NOTE]
> Development happens on [git.coffeylabs.org/jcoffey-dev/mailsink](https://git.coffeylabs.org/jcoffey-dev/mailsink); the copy on GitHub is a read-only mirror.
> Report issues at **[git.coffeylabs.org/jcoffey-dev/mailsink/issues](https://git.coffeylabs.org/jcoffey-dev/mailsink/issues)**, and join discussions at **[community.coffeylabs.org](https://community.coffeylabs.org)**.

An SMTP server for test and development networks. Applications on other
machines send mail to it as they would to a real server. It accepts mail for
the domains you allow and discards it.

- **Nothing is kept.** Message bodies are read off the socket and discarded
  as they arrive. They are never held in memory as a whole or written to
  disk, and the container's filesystem is read-only.
- **Nothing is relayed.** The server contains no code that opens an outbound
  connection, and the runtime image has only its one static binary: no MTA,
  no shell, no network tools. Mail to a domain outside the allowlist gets
  `550 5.7.1` at `RCPT TO`.
- **Only local clients.** Connections from addresses outside
  `ALLOWED_NETWORKS` get `554` and are closed.
- **No logging.** The server writes nothing while it runs, and the compose
  file sets Docker's logging driver to `none`.

## Configure

All settings are build arguments at the top of the `Dockerfile`, with
comments explaining each one. They are compiled into the binary, so the
running container reads no environment variables or config files. To change
a setting, edit the Dockerfile and rebuild. A bad value fails the build.

| Setting | Default |
|---|---|
| `ALLOWED_DOMAINS` | `example.test,*.example.test` (`*` accepts any domain) |
| `ALLOWED_NETWORKS` | the RFC 1918 ranges and `fc00::/7` |
| `SMTP_HOSTNAME` | `mailsink.test` |
| `SMTP_PORT` | `25` |
| `MAX_MESSAGE_SIZE` | `26214400` (25 MiB) |
| `MAX_RECIPIENTS` | `100` |
| `MAX_CONNECTIONS` | `100` |

## Install

### From the registry

Images for amd64 and arm64 are published at
`registry.coffeylabs.org/jcoffey-dev/mailsink`. No login is needed to pull
them. The tag `latest` follows `main`, and each build is also tagged with its
short commit hash.

The published image uses the default settings above. If those fit your test
network, use it as it is. Otherwise build your own (see below), since the
settings are compiled in and can't be changed when the container starts.

With Compose, take `compose.yaml` from this repository and replace the
`build: .` and `image:` lines with:

```yaml
    image: registry.coffeylabs.org/jcoffey-dev/mailsink:latest
```

Then:

```sh
docker compose up -d
sudo ./egress-lockdown.sh      # optional second layer; see below
```

Without Compose:

```sh
docker network create -o com.docker.network.bridge.name=br-mailsink mailsink
docker run -d --name mailsink --restart unless-stopped \
  --network mailsink -p 25:25 \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --log-driver none \
  registry.coffeylabs.org/jcoffey-dev/mailsink:latest
```

`egress-lockdown.sh` finds the container by its bridge name, `br-mailsink`,
so keep that name if you use the script. To fetch just the script:

```sh
curl -fsSLO https://git.coffeylabs.org/jcoffey-dev/mailsink/raw/branch/main/egress-lockdown.sh
chmod +x egress-lockdown.sh
```

### Build your own

Clone the repository, change the settings at the top of the `Dockerfile`, then:

```sh
docker compose up -d --build
sudo ./egress-lockdown.sh      # optional second layer; see below
```

The Dockerfile cross-compiles, so one build covers amd64 and arm64 without
emulation:

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t mailsink .
```

## Use

Point applications at the Docker host on port 25. Any username and password
are accepted, so apps configured for authenticated SMTP work unchanged. There
is no TLS: clients must allow a plaintext connection.

`ALLOWED_NETWORKS` is checked against the client's source address. Clients on
the network reach the container through Docker's NAT, which keeps their real
address. A connection from the Docker host itself goes through Docker's
userland proxy, so it appears to come from the bridge gateway (in
`172.16.0.0/12`). If you narrow `ALLOWED_NETWORKS`, tests run on the host
itself will be refused.

## Blocking egress at the firewall

Docker can't publish a port from an `internal` network, so the container sits
on a normal bridge (`br-mailsink`). `egress-lockdown.sh` adds iptables rules
that drop every new connection starting from that bridge, both out to the
network and to the Docker host itself. Inbound SMTP and its replies still
pass. Run it as root after the container is up, and again after a reboot.
`--remove` takes the rules out. The script assumes Docker's iptables firewall
backend.

## License

GPL-3.0-or-later. See `LICENSE`.
