# Fleet dashboards

`config/observability-dashboards.yaml` is the versioned public dashboard
contract. It contains no tenant, host, destination or credential facts and uses
only bounded fleet/Collector metrics exported by the public observer product.

Validate and render the exact OpenObserve-neutral JSON model with:

```bash
gha-fleet validate-observability-dashboards \
  --config config/observability-dashboards.yaml
gha-fleet render-openobserve-dashboards \
  --config config/observability-dashboards.yaml
```

The six dashboards cover:

- capacity and pressure;
- correlation integrity;
- lifecycle phase latency;
- provider reliability;
- priority/class fairness;
- telemetry buffering and freshness.

Private estate deployment owns OpenObserve endpoint, folder identity,
destinations and read-back evidence. Dashboard queries are also the canonical
human decision surfaces used by alert response and CD health verification; a
private deployment must not silently edit them.

Dashboards do not prove alerts deliver. `config/observability-rules.yaml` owns
the separately tested alert contract, and a real backend lifecycle record must
prove Collector buffering, recovered records and explicit loss counters before
the observability product is accepted.
