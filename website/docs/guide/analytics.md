---
title: Analytics stack
---

```bash
sudo nomctl analytics install
```

Installs a monitoring stack on the node:

- **node_exporter** (`NOMCTL_NODE_EXPORTER_VERSION`) from the GitHub release for the host architecture, as a systemd service under its own user.
- **Prometheus** (`NOMCTL_PROMETHEUS_VERSION`) the same way, with `/etc/prometheus/prometheus.yml` and a `node` scrape job added if missing.
- **Grafana** from its apt repository (the key is stored at `/etc/apt/keyrings/grafana.asc`), listening on port 3000.
- The **Prometheus** and **Infinity** datasources in Grafana, the Infinity plugin (`NOMCTL_INFINITY_PLUGIN_VERSION`), the public **Node Exporter Full** dashboard, and the **znnd** dashboard embedded in the nomctl binary.

Every step checks its own precondition, so an interrupted run is completed by running the command again, and re-running on a finished install changes nothing.

Log in at `http://<host>:3000` with `NOMCTL_GRAFANA_ADMIN_USER` / `NOMCTL_GRAFANA_ADMIN_PASSWORD` (default `admin` / `admin`; change it).

For a quick look without a browser, `nomctl status` and `nomctl top` cover the same signals from the terminal.
