# Extra dashboards

A companion app drops its own Grafana dashboards (`*.json`) here (SPEC.md R76a). Grafana reads this folder every minute. A dashboard in a subfolder goes in the Grafana folder of that name (`Companion/calls.json` in "Companion"); one at the top, in "General". Use dashboard UIDs of your own.

A release's archive holds this folder empty, so a release or upgrade never writes, replaces or deletes anything in it. This README is in the repository only. See docs/monitoring.md, "A companion app".
