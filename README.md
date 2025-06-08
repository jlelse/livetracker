# LiveTracker

LiveTracker is a lightweight web application for real-time GPS tracking. It is designed to receive location updates from the OsmAnd Android app's [Online tracking](https://osmand.net/docs/user/plugins/trip-recording/) feature and display the live track in a web browser. The backend is written in Go and stores location data in a SQLite database, while the frontend uses Leaflet.js for interactive map visualization.

## Features

- Receive and store GPS location updates from OsmAnd (or compatible clients)
- Live map view in the browser with real-time updates via WebSocket
- Historical track display (last 3 hours shown on first load; all data is kept in the database)
- Basic authentication for the web interface and WebSocket
- Simple, single-binary deployment (no external dependencies except SQLite)

## Getting Started

### Prerequisites
- Go 1.24 or newer (for building from source)
- SQLite3 (installed by default on most Linux systems)
- SQLite development library (e.g. `libsqlite3-dev` on Debian/Ubuntu, `sqlite-devel` on Fedora/RedHat, `sqlite-dev` on Alpine)
- [OsmAnd](https://osmand.net/) app (for sending location updates)

### Preferred: Run with Docker

The recommended way to run LiveTracker is using the prebuilt Docker image from the GitHub Container Registry:

```sh
docker run -p 8080:8080 \
  -e LIVETRACKER_API_TOKEN=yourtoken \
  -e LIVETRACKER_BASIC_AUTH_USER=youruser \
  -e LIVETRACKER_BASIC_AUTH_PASS=yourpass \
  -e LIVETRACKER_SQLITE_PATH=/data/tracker.db \
  -v $(pwd)/data:/data \
  ghcr.io/jlelse/livetracker:latest
```

> **Note:** The `-v $(pwd)/data:/data` option mounts the entire data directory from your host system into the container. This is required because SQLite in WAL mode creates extra files (e.g., `tracker.db-wal`, `tracker.db-shm`) that must be persisted along with the main database file. Adjust the path as needed.

You can also build the image yourself:

```sh
docker build -t livetracker .
docker run -p 8080:8080 \
  -e LIVETRACKER_API_TOKEN=yourtoken \
  -e LIVETRACKER_BASIC_AUTH_USER=youruser \
  -e LIVETRACKER_BASIC_AUTH_PASS=yourpass \
  -e LIVETRACKER_SQLITE_PATH=/data/tracker.db \
  -v $(pwd)/data:/data \
  livetracker
```

### Docker Compose Example

You can use Docker Compose for easier setup:

```yaml
services:
  livetracker:
    image: ghcr.io/jlelse/livetracker:latest
    ports:
      - "8080:8080"
    environment:
      LIVETRACKER_API_TOKEN: yourtoken
      LIVETRACKER_BASIC_AUTH_USER: youruser
      LIVETRACKER_BASIC_AUTH_PASS: yourpass
      LIVETRACKER_SQLITE_PATH: /data/tracker.db
    volumes:
      - ./data:/data
```

Start with:
```sh
docker compose up -d
```

### Build from Source

1. Clone the repository:
   ```sh
   git clone https://github.com/jlelse/LiveTracker.git
   cd LiveTracker
   ```
2. Install the SQLite development library for your Linux distribution (see Prerequisites).
3. Build the application using Go with build flags to use the system's SQLite3 version:
   ```sh
   go build -tags=linux,libsqlite3,sqlite_fts5 -o livetracker
   ```

### Configuration

The application is configured via environment variables:

| Variable                      | Default    | Description                                 |
|-------------------------------|------------|---------------------------------------------|
| LIVETRACKER_PORT              | 8080       | HTTP server port                            |
| LIVETRACKER_SQLITE_PATH       | tracker.db | Path to SQLite database file                |
| LIVETRACKER_API_TOKEN         | default    | API token for /track endpoint               |
| LIVETRACKER_BASIC_AUTH_USER   | admin      | Username for web interface & WebSocket      |
| LIVETRACKER_BASIC_AUTH_PASS   | admin      | Password for web interface & WebSocket      |

**Important:** Change the default API token and credentials for production use!

### Usage

1. **Start the server:**
   ```sh
   LIVETRACKER_API_TOKEN=yourtoken \
   LIVETRACKER_BASIC_AUTH_USER=youruser \
   LIVETRACKER_BASIC_AUTH_PASS=yourpass \
   ./livetracker
   ```

2. **Configure OsmAnd:**
   - Go to *Configure Trip Recording with Online tracking* in OsmAnd ([see docs](https://osmand.net/docs/user/plugins/trip-recording/))
   - Add a new service with the following URL:
     ```
     http://<your_server_ip>:8080/track?token=yourtoken&lat={0}&lon={1}&timestamp={2}&hdop={3}&altitude={4}&speed={5}&bearing={6}
     ```
   - Replace `<your_server_ip>` and `yourtoken` accordingly.

3. **Open the web interface:**
   - Visit `http://<your_server_ip>:8080/` in your browser
   - Log in with the configured username and password
   - Watch the live track update in real time!

## Data Retention

All received location data is stored in the SQLite database. On first load, the web interface displays the last 3 hours of history, but older data remains available in the database for future use or export.

## Production Use

For production deployments, it is strongly recommended to run LiveTracker behind a reverse proxy with HTTPS, such as [Caddy](https://caddyserver.com/) or Nginx. This ensures secure access to your tracking data and credentials.

## Development & Testing

- Run tests:
  ```sh
  go test -v ./...
  ```
- The project includes a Dockerfile with a test stage for CI/CD.

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) for details.
