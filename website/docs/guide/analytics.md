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

Grafana listens on all interfaces on port 3000, over plain HTTP, and starts with the credentials `admin` / `admin`. nomctl uses `NOMCTL_GRAFANA_ADMIN_USER` / `NOMCTL_GRAFANA_ADMIN_PASSWORD` to talk to the API; it does not change Grafana's password. So before opening port 3000 to the Internet:

1. Change the password in Grafana (Administration, Users) and set `NOMCTL_GRAFANA_ADMIN_PASSWORD` to match so future `analytics install` runs can still configure datasources.
2. Prefer not to expose the port at all. From your workstation:

   ```bash
   ssh -L 3000:127.0.0.1:3000 root@<host>
   ```

   then open `http://localhost:3000`. If you need it reachable, put an HTTPS reverse proxy in front and firewall port 3000.

For a quick look without a browser, `nomctl status` and `nomctl top` cover the same signals from the terminal.
