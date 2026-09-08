# Webhooks

With `github.webhook: true` (the default in [1Password
mode](configuration.md#zero-config-from-1password)), arc reacts the moment a job
queues instead of waiting up to a poll interval:

- it starts a **Cloudflare quick tunnel** — no Cloudflare account, no inbound
  firewall rule, no port forwarding — and takes the random
  `https://<name>.trycloudflare.com` URL it is handed;
- it registers a `workflow_job` webhook pointing at that URL, on the org, or on
  every watched repo for a personal account;
- when the tunnel dies it restarts and re-registers, since the URL changes.

`cloudflared` downloads itself if it isn't on the host already.

Polling drops to a 2-minute safety net for missed deliveries. Every failure in
this pipeline degrades to polling rather than to a stalled cluster: a tunnel
that won't start, a registration that's refused, a delivery that never arrives
all cost latency, not capacity.

## Hooks and hosts

The webhook path carries this host's id, so several arc hosts on one account
each keep their own hook instead of overwriting the last one registered. Every
host gets every event; the idle-aware demand logic in
[Scaling](scaling.md#several-hosts-one-account) is what keeps them from
double-provisioning.

The delivery secret is regenerated on every arc start, so an existing hook is
always rewritten rather than trusted. `arc uninstall` deletes the hooks this
host registered — a host that disappears without it leaves a hook posting to a
dead tunnel URL, which GitHub retries and eventually disables on its own.
