export type Summary = Readonly<{ records: number; checksum: string }>;

export function summarize(records: number): Summary {
  let state = 2166136261;
  for (let index = 0; index < records; index += 1) {
    state ^= index;
    state = Math.imul(state, 16777619) >>> 0;
  }
  return { records, checksum: state.toString(16).padStart(8, "0") };
}
