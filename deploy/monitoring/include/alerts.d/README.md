# Extra alert rules

A companion app drops its own Grafana alert rule files (`*.yaml`, `*.yml` or `*.json`, Grafana's provisioning format) here (SPEC.md R76a). At start, Grafana provisions them with this stack's own rules. Use a folder, group and rule UIDs of your own. Restart Grafana after changing a file here: `docker compose restart grafana`.

A release's archive holds this folder empty, so a release or upgrade never writes, replaces or deletes anything in it. This README is in the repository only. See docs/monitoring.md, "A companion app".
