# Jellych

A lightweight server that leverages [`ffmpeg`](https://github.com/ffmpeg/ffmpeg) to stream Twitch livestreams via `Web-UI` and `Jellyfin` (*requires [Jellyfin Plugin](https://github.com/c2bw/jellyfin-plugin-jellych)*).

<img src="docs/images/home.jpg" alt="Jellych web UI" height="450">

## Configuration

> [!WARNING]
> Jellych does not authenticate the web UI or most control API endpoints.
> If you expose it outside a trusted LAN, put it behind a reverse proxy or
> tunnel that provides authentication. `JELLYFIN_WEBHOOK_SECRET` only protects
> the Jellyfin webhook endpoint.

Required environment variables:

- `TWITCH_CLIENT_ID` for Twitch API access, register an application on the [Twitch Developer Portal](https://dev.twitch.tv/console/apps)
- `TWITCH_CLIENT_SECRET` for Twitch API access, register an application on the [Twitch Developer Portal](https://dev.twitch.tv/console/apps)
- `SERVER_URL` publicly accessible URL where the server is reachable, also used for Jellyfin webhook
- `JELLYFIN_WEBHOOK_SECRET` shared secret expected on `X-Jellych-Secret`

Optional environment variables:

- `LOG_LEVEL` logging level (default `INFO`)
- `VOD_RETENTION_DAYS` number of days to keep downloaded VOD files (default `30`, must be a positive integer)

Optional flags:

- `-addr` (default `:8080`) HTTP listen address
- `-config` (default `/data/config`) path to the channels config directory, which contains persistent configuration files
- `-vods` folder where manually downloaded VODs are saved

## Run

### Docker Compose Example

```bash
services:
  jellych:
    image: ghcr.io/c2bw/jellych:latest
    restart: unless-stopped
    environment:
      - LOG_LEVEL=INFO
      - VOD_RETENTION_DAYS=30
      - TWITCH_CLIENT_ID=your_client_id
      - TWITCH_CLIENT_SECRET=your_client_secret
      - SERVER_URL=http://localhost:8080
      - JELLYFIN_WEBHOOK_SECRET=your_webhook_secret
    ports:
      - "8080:8080"
    volumes:
      - vods_volume:/data/vods
      - config_volume:/data/config

volumes:
  vods_volume:
  config_volume:
```

### Configure Jellyfin

- Dashboard > Live TV > Add Tuner Device > M3U Tuner -> Then enter the URL to your server's `/api/twitch.m3u` endpoint, e.g. `http://localhost:8080/api/twitch.m3u`
- Dashboard > Scheduled Tasks > Refresh Guide -> Every 15 minutes
- *OPTIONAL: create a library for the VODs folder*
- Install Jellyfin plugin: https://github.com/c2bw/jellyfin-plugin-jellych
