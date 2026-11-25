# Extra Authentik blueprints

A companion app on the same host drops its own Authentik blueprints (`*.yaml`) here, such as its OIDC client (SPEC.md R84l). Authentik's server and worker mount this folder read-only at `/blueprints/extra/` and apply every blueprint under `/blueprints`, so one dropped here is applied like PagerArchive's own. Empty, nothing more is applied.

A release's archive holds this folder empty, so unpacking a release (an install, an upgrade, automatic or by hand) never writes, replaces or deletes anything in it. This README is in the repository only. See docs/deploy.md, "A companion app".
