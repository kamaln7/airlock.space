# airlock.space

NASA's [Astronomy Picture of the Day](https://science.nasa.gov/apod/) served to your terminal as a TUI over SSH.

```sh
ssh airlock.space   # no installation needed :)
```


<p align="center">
  <img src="docs/ascii.png" width="1561" alt="APOD rendered as sextant blocks alongside the day's explanation">
</p>

Terminals that speak the kitty graphics protocol (kitty, Ghostty) get the real
photograph.

<p align="center">
  <img src="docs/photo.png" width="1561" alt="The same picture full-bleed via the kitty graphics protocol">
</p>

## Running locally

```sh
go run ./cmd/airlockspace
```

No API key or other setup is needed. airlock reads NASA's public, keyless
[APOD Basic JSON feed](https://science.nasa.gov/wp-json/wp/v2/apod-basic)
on science.nasa.gov. Being public does not mean unlimited: NASA may throttle or
reject requests, and airlock makes no rate-limit guarantee. It caches the latest
picture, previously requested dates, and downloaded images to keep requests
low, and keeps serving stale data when a refresh fails.

Set `APOD_DATE=YYYY-MM-DD` to show a specific day instead of the latest picture
(useful for testing).

### `pkg/apod`

The public `github.com/kamaln7/airlock.space/pkg/apod` package is a small
client for the feed. `NewClient(opts ...Opts)` provides `Latest(ctx)` and
`ByDate(ctx, date)`. Both return `(*Picture, error)`, with a nil picture on
failure. Transport, JSON decoding, and date parsing errors are returned
directly.

```go
client := apod.NewClient(
    apod.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}),
    apod.WithMaxResponseBytes(4 << 20),
)
picture, err := client.Latest(ctx)
```

`Picture` carries `Date`, `Title`, `ArticleURL` (the APOD post on
science.nasa.gov), `ImageURL` (the full-size featured image, possibly empty)
and `ExplanationHTML` (the explanation exactly as published, markup included).
Article and image URLs are distinct. Unknown JSON fields are not retained.

The `Opts` are:

* `WithHTTPClient` sets the HTTP client (default `http.DefaultClient`); nil
  leaves it unchanged. The client imposes no timeout of its own, so timeouts
  belong to this HTTP client or the caller's context.
* `WithBaseURL` sets the API root, ignoring trailing slashes; empty leaves it
  unchanged.
* `WithMaxResponseBytes` bounds the bytes read from the JSON response body. The
  default is 2 MiB, zero disables limiting, and negative values leave the
  current limit unchanged. The cap counts every byte the decoder reads for the
  value it decodes, including unknown fields. It is not strict whole-body
  validation: nothing is checked beyond that read limit, trailing response size
  included, and a body cut off by the cap surfaces as a decode error. It does
  not limit image downloads.

Options are applied in order; the last valid option for a setting wins.
`NewClient()` uses the defaults.

Dates are calendar days. `ByDate` uses the year, month and day of the date it is
given, in that date's own location, and rejects dates before the first APOD on
1995-06-16 or a response for a different day. `Picture.Date` is that day at
midnight UTC. No timezone conversion is applied anywhere, so the day shown is the
day NASA published. A client's `TZ`, if the SSH session happens to carry one, is
informational only and never shifts the date.

### How airlock uses it

airlock owns the 15-second metadata HTTP timeout. It converts
`ExplanationHTML` to Markdown once per picture, and the TUI renders that
Markdown with [Glamour](https://github.com/charmbracelet/glamour) inside the
explanation viewport, so emphasis, paragraphs and link text are styled
rather than flattened. The explanation is shown as the text itself, with no
leading `Explanation:` label, and its paragraph breaks and line breaks are
preserved.

Links inside the explanation show only their label; the URL is never printed
after it. The label is emitted as an
[OSC 8](https://gist.github.com/egmontkob/eb114294efbcd5adb1944c9f3cb5feda)
hyperlink and left to the terminal: terminals that support OSC 8 make it
clickable, and terminals that do not show the plain label. airlock does not
detect hyperlink support, since there is no portable way to query it. The
article link, title and footer links are separate from this and unchanged.

Pictures keep their article link and image URL separately. A video entry shows
its featured poster when it has one; an entry with no featured image is
text-only, with its title, explanation and article link.

Cache records written by the previous API client are ignored and fetched again.
Images are whatever the upstream feed's `ImageURL` provides.

## Self-hosting

### NixOS

See: [NixOS deployment guide](docs/nixos-deployment.md).

### systemd

* `airlocksshd` runs as a systemd service with `DynamicUser=yes` - a transient unprivileged user.
* `airlocksshd.socket` allow systemctl to bind to port 22 and hand the listener over to airlocksshd
without granting it elevated privileges.
* move your ssh server to another port (`/etc/ssh/sshd_config`) or change airlock's port in `airlocksshd.socket`.

1. Compile and transfer to the host: 

  ```sh
  GOOS=linux GOARCH=amd64 go build -o airlocksshd ./cmd/airlocksshd
  scp airlocksshd root@your-host:/opt/airlocksshd
  scp deploy/airlocksshd.{service,socket} root@your-host:/etc/systemd/system/
  ```

2. On the host, generate a host ssh key for airlock:

  ```sh
  mkdir -p /etc/airlocksshd && chmod 700 /etc/airlocksshd
  ssh-keygen -t ed25519 -f /etc/airlocksshd/id_ed25519 -N ''
  ```

  Keep this same host key across deploys so clients don't see a host-key mismatch warning.

3. Set root-only perms:

  ```sh
  chmod 600 /etc/airlocksshd/*
  chown -R root:root /etc/airlocksshd
  ```

4. Start the service:

  ```sh
  systemctl daemon-reload && systemctl enable --now airlocksshd.socket
  ```

Redeploys are `scp` the new binary over, then `systemctl restart airlocksshd`;
the socket keeps listening across the restart.

### DIY

Run `airlocksshd` however you like. It:

* needs no NASA API key; it reads the public APOD Basic feed on science.nasa.gov
* falls back to binding to `SSH_HOST`:`SSH_PORT` itself (`localhost:23234`)
* keeps its host key at `SSH_HOST_KEY_PATH` (`.airlocksshd/id_ed25519`, generated on first start)
* caches source images under `STATE_DIRECTORY` or your user cache dir
