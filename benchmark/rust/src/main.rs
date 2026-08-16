fn main() -> anyhow::Result<()> {
    let summary = nddev_runner_benchmark::summarize(250_000);
    println!("{}", serde_json::to_string(&summary)?);
    Ok(())
}
