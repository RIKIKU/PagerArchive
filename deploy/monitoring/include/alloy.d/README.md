# Extra Alloy configuration

A companion app drops its own Alloy configuration (`*.alloy`) here (SPEC.md R76a). At start, Alloy runs this stack's `alloy/config.alloy` and every `*.alloy` here as one configuration, so a file here can use this stack's components, such as `prometheus.remote_write.prometheus.receiver`, and needs labels of its own that no other file uses. Restart Alloy after changing a file here: `docker compose restart alloy`.

A release's archive holds this folder empty, so a release or upgrade never writes, replaces or deletes anything in it. This README is in the repository only. See docs/monitoring.md, "A companion app".
