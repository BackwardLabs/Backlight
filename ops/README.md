# helios ops — local Prometheus + Grafana

Pure client-side observability stack for the helios `/metrics` endpoint. The
helios server does not need any code changes; this directory only adds a
Prometheus that scrapes it and a Grafana that visualises it.

## Prerequisites

- Docker + Docker Compose v2 (`docker compose ...`)
- helios already running locally on port 8080 with `HELIOS_API_TOKEN` set
- The same `HELIOS_API_TOKEN` value exported in the shell that runs
  `docker compose up` so Prometheus can scrape the protected endpoint

## Start

```bash
export HELIOS_API_TOKEN=<the same token helios is running with>

# optional: override the host:port Prometheus scrapes (defaults to host.docker.internal:8080)
# export HELIOS_HOST=host.docker.internal:8080

docker compose -f ops/docker-compose.yml up
```

Prometheus → http://localhost:9090
Grafana → http://localhost:3000 (anonymous viewer enabled, admin/admin to edit)

The helios dashboard is auto-provisioned under `Dashboards → helios → helios v1`.

## Verify the scrape

In Prometheus UI: **Status → Targets** should show `helios` with state UP.

If it's DOWN with "401 Unauthorized": the token in `HELIOS_API_TOKEN` does not
match what helios is running with. Stop the stack, re-export the right token,
and `docker compose up` again.

If it's DOWN with "connection refused": helios isn't actually listening on
the resolved host:port. On Linux the compose file uses `host-gateway` so
`host.docker.internal` resolves to the host. If helios listens only on
`127.0.0.1`, set `HELIOS_LISTEN_ADDR=0.0.0.0:8080` and restart it.

## Panels in the auto-loaded dashboard

| Panel | Series |
| --- | --- |
| Queue depth | `helios_queue_depth` (live SQLite count) |
| Cases by terminal outcome | `sum by (outcome)(helios_case_outcome_total)` |
| State transitions per minute | `sum by (state)(rate(helios_case_state_total[1m])) * 60` |
| lumoskit duration p50 / p95 / p99 | histogram_quantile over `helios_lumoskit_duration_seconds_bucket` |
| Handoff attempts by result | `sum by (result)(rate(helios_handoff_attempt_total[1m])) * 60` |
| Notification attempts | `sum by (channel, result)(rate(helios_notification_attempt_total[1m])) * 60` |
| Cases by current state (totals) | `sum by (state)(helios_case_state_total)` |

## Stop / clean

```bash
docker compose -f ops/docker-compose.yml down            # stop, keep data
docker compose -f ops/docker-compose.yml down -v         # also wipe Prometheus + Grafana volumes
```

## Files

- `docker-compose.yml` — Prometheus + Grafana services
- `prometheus.yml` — scrape config (token mounted at `/etc/prometheus/token`)
- `grafana/provisioning/datasources/prometheus.yml` — auto-add datasource
- `grafana/provisioning/dashboards/helios.yml` — point Grafana at the dashboard dir
- `grafana/dashboards/helios.json` — the actual dashboard panels
