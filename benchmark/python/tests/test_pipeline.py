from nddev_runner_benchmark import Record, summarize


def test_summary_is_deterministic() -> None:
    assert summarize(5_000) == summarize(5_000)
    assert len(summarize(5_000)) == 64


def test_record_rejects_unknown_fields() -> None:
    try:
        Record(identifier=1, endpoint="https://benchmark.invalid", unknown=True)  # type: ignore[call-arg]
    except ValueError:
        return
    raise AssertionError("unknown record field was accepted")
