---
title: Analytics stack
description: "Install node_exporter, Prometheus and Grafana with the znnd dashboard, and reach Grafana safely."
---

```bash
sudo nomctl analytics install
```

Installs a monitoring stack on the node:

- **node_exporter** (`NOMCTL_NODE_EXPORTER_VERSION`) from the GitHub release for the host architecture, as a systemd service under its own user.
- **Prometheus** (`NOMCTL_PROMETHEUS_VERSION`) the same way, with `/etc/prometheus/prometheus.yml` and a `node` scrape job added if missing.
- **Grafana** from its apt repository (the key is stored at `/etc/apt/keyrings/grafana.asc`), listening on port 3000.
- The **Prometheus** and **Infinity** datasources in Grafana, the Infinity plugin (`NOMCTL_INFINITY_PLUGIN_VERSION`), the public **Node Exporter Full** dashboard, and the **znnd** dashboard embedded in the nomctl binary.

Every step checks its own precondition, so an interrupted run is completed by running the command again, and re-running on a finished install skips what exists (it still re-applies file ownership and waits for Grafana to answer).

## Reaching Grafana safely

By default nomctl binds Grafana to `127.0.0.1:3000` (a systemd drop-in sets `GF_SERVER_HTTP_ADDR`), so it is not reachable from the network. From your workstation:

```bash
ssh -L 3000:127.0.0.1:3000 root@<host>
```

then open `http://localhost:3000`. To expose it, set `NOMCTL_GRAFANA_HTTP_ADDR=0.0.0.0` before `analytics install` and put an HTTPS reverse proxy in front.

Grafana ships with the credentials `admin` / `admin`. If `NOMCTL_GRAFANA_ADMIN_PASSWORD` is set to anything else when you run `analytics install`, nomctl applies it to Grafana through the API on first install (and keeps using it for datasource setup afterwards). With the default value it warns and leaves the password unchanged, which is only acceptable while Grafana is bound to localhost.

```bash
sudo NOMCTL_GRAFANA_ADMIN_PASSWORD='a long passphrase' nomctl analytics install
```

For a quick look without a browser, `nomctl status` and `nomctl top` cover the same signals from the terminal.
