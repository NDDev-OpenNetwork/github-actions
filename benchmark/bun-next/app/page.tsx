import { summarize } from "../lib/summarize";

export default function Page() {
  const summary = summarize(10_000);
  return (
    <main>
      <h1>NDDev runner benchmark</h1>
      <p data-checksum={summary.checksum}>{summary.records} deterministic records</p>
    </main>
  );
}
