# Contributing

Changes should be coherent, tested and minimal. An implementation agent may
choose the design and execution details needed to achieve the stated outcome;
an issue or human review ceremony is not a prerequisite for local work.

Before integration run:

```bash
make verify
```

Lifecycle changes require success, cancellation, duplicate delivery, timeout,
restart and cleanup coverage. External side effects require stable identity,
bounded execution and idempotent reconciliation. Performance claims require a
reproducible equivalent-workload comparison.

Do not add real estate data or self-hosted runner labels to public workflows.
