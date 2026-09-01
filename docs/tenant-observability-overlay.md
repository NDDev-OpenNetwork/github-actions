# Tenant observability overlay

The public rules bundle (`config/observability-rules.yaml`) is the engine's
own alerting and changes only with the engine. A tenant hosted on the fleet's
OpenObserve still needs alerts about its own streams, and those alerts must
never require editing the public bundle.

The overlay is how: a second bundle file with the same schema, carried by the
estate, merged into the base at load time by every rules command:

```
gha-fleet validate-observability-rules --config config/observability-rules.yaml \
  --overlay /etc/gha-fleet/observability-rules-estate.yaml
gha-fleet reconcile-openobserve-alerts --overlay /etc/gha-fleet/observability-rules-estate.yaml \
  --endpoint ... --username-file ... --password-file ... --enable --apply
```

The union is governed, not free-form:

- an overlay is a complete, valid bundle: same schema version, backend and
  organization, sorted ids, every per-rule contract enforced;
- an overlay **adds** rules; an id collision with the base (or an earlier
  overlay) refuses the whole load — a tenant can never redefine an engine
  alert;
- the merged bundle re-validates as one document, so the 64-rule budget and
  ordering hold for the union.

## SQL rules

Tenant streams are logs, not metrics, so overlay rules may use
`query_language: sql`. A SQL rule reads its logs stream over the alert
period; the condition lives in the statement itself (`HAVING`), and
`operator`/`threshold` gate the number of result rows. An absence alert is
expressible because a global aggregate always returns exactly one row:

```sql
SELECT count(*) AS record_count FROM "anton_logs" HAVING count(*) < 1
```

returns one row exactly when the window saw no records, and the trigger
`>= 1` fires on it. A single statement only; the trigger threshold must be a
whole non-negative row count.

## Runbooks

### anton_telemetry_stale

The `anton_logs` stream saw no records for the alert window. The collector on
the tenant host (`server-anton-kz`), the network path to the fleet
OpenObserve, or the tenant host itself is down. Check the tenant host's
collector service and its egress; records arriving again clears the
condition. Evaluation failures are separately covered by
`alert_evaluation_failed`, so a query that stops evaluating does not hide
behind this rule.

### anton_healthcheck_problem

A healthcheck request line in `anton_logs` answered with a 4xx or 5xx. The
stream carries two producers with different access-log formats — the backend
writes `... HTTP/1.1" 200 OK`, nginx writes `... HTTP/2.0" 200 109` — so the
predicate matches the status code after the HTTP-version quote, which both
formats share. (The first version tested for the absence of `200 OK` and
false-fired on a healthy nginx 200; the current form was proven on live data
in both directions before deployment.) Check the tenant application's
containers; healthchecks answering 2xx again clears the condition.
