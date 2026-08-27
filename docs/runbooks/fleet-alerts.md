# Fleet alert response

Every fleet alert is symptom-oriented. Confirm the exact metric and correlated
queue intent, GitHub job, provider lease and Incus instance before recovery.

OpenTelemetry collects and transforms every fleet signal, OTLP/HTTP transports
it, and OpenObserve stores, queries, dashboards and alerts it. PromQL in this
contract is OpenObserve's query syntax; no Prometheus server, agent,
remote-write path or second metric store is part of the product.

`config/observability-rules.yaml` is the canonical rule source. Each rule names
one real OpenObserve metric stream for API ownership and keeps its PromQL
expression separate from the comparison operator and threshold. Render the
exact v0.92 API payload with:

```bash
gha-fleet render-openobserve-alerts \
  --config config/observability-rules.yaml \
  --destination fleet_oncall
```

The default output is disabled. Use `--enable` only after the named destination
has passed an independent delivery-and-recovery test. A missing, synthetic or
silent destination is not an acceptable reason to enable rules. The renderer's
stream names must all exist in the target metrics inventory before apply.

Use `reconcile-openobserve-alerts` for deployment. The default invocation is a
read-only plan; `--apply` performs the listed create/update/delete operations
and then requires an empty read-back plan. Credentials are accepted only from
private absolute one-line files, never command arguments. The reconciler may
delete an obsolete alert only when its live tags contain `managed-by:gds`; all
other live alerts are outside its ownership.

```bash
gha-fleet reconcile-openobserve-alerts \
  --config config/observability-rules.yaml \
  --destination fleet_oncall \
  --endpoint https://openobserve.example.invalid \
  --username-file /run/credentials/openobserve-username \
  --password-file /run/credentials/openobserve-password
```

- Platform, lifecycle or diagnostics pages: stop promotion work, preserve
  journals and diagnostic bundles, identify the oldest exact identity, and use
  only the bounded recovery operation that matches authoritative evidence.
- GitHub correlation pages: inspect only the age-qualified metrics. Raw
  unbound/missing counters describe normal queued and assigned capacity phases
  and are diagnostic context, not a recovery trigger. Persistent identity
  paging begins only after the intent is running and its state-entry grace has
  elapsed.
- Collector queue pages: preserve the queue directory, restore the private
  OpenObserve route/backend, and verify queue drain plus exact record recovery.
- OOM or pressure pages: close admission; never stop an already running worker
  merely to make utilization look healthy.
- Host-signal tickets: use `gha_fleet_host_signal_events` cumulative deltas.
  LVM activation and overlay `xino=off` are workload-volume context; audit
  suppression and workqueue-hog alerts act only on their bounded burst budget.
  Raw host-signal logs are intentionally not duplicated into the fleet stream.
- Compliance alerts: require complete coverage from every declared host before
  trusting package, reboot, kernel or SRSO state. A kernel-reported hardware or
  microcode boundary is not a software rollout failure.
- Provider terminal-circuit pages: preserve the retry journal, prove the
  provider/config repair, stop GARM only with zero running leases, dry-run and
  apply the exact `recover-provider-retry` CAS operation, restart GARM, and
  require a fresh job to reach provider create and runner online.
- Persistent provider-retry pages: ignore capacity backpressure and retained
  history; inspect only the current non-capacity deferred class, correlate its
  exact private journal identity, repair the cause, and preserve the record
  until normal successful consolidation removes or supersedes it.
- Slow-burn tickets: inspect class/tenant percentiles and capacity evidence;
  do not page an operator for a trend without an immediate action.

OpenObserve v0.92 pauses outcome evaluation during notification silence.
Managed pages therefore use ten-minute silence and tickets use fifteen-minute
silence; do not interpret an older `firing` outcome until that bounded window
has elapsed. Silence never replaces the rule's sustained range-query hold.

Recovery means the triggering expression is false for its configured hold
window, health is green, and no orphan, missing, uncovered or telemetry-loss
counter remains non-zero.
