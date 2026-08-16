use rayon::prelude::*;
use serde::Serialize;
use sha2::{Digest, Sha256};
use std::fmt::Write;

#[derive(Debug, PartialEq, Serialize)]
pub struct Summary {
    pub records: usize,
    pub checksum: String,
}

pub fn summarize(records: usize) -> Summary {
    let parts: Vec<[u8; 32]> = (0..records)
        .into_par_iter()
        .map(|index| {
            let mut hasher = Sha256::new();
            hasher.update(b"nddev-runner-benchmark-v1");
            hasher.update(index.to_le_bytes());
            hasher.finalize().into()
        })
        .collect();

    let mut aggregate = Sha256::new();
    for part in parts {
        aggregate.update(part);
    }
    let digest = aggregate.finalize();
    let mut checksum = String::with_capacity(digest.len() * 2);
    for byte in digest {
        write!(&mut checksum, "{byte:02x}").expect("writing to a String cannot fail");
    }
    Summary {
        records,
        checksum,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn summary_is_deterministic_and_bounded() {
        let first = summarize(10_000);
        let second = summarize(10_000);
        assert_eq!(first, second);
        assert_eq!(first.records, 10_000);
        assert_eq!(first.checksum.len(), 64);
    }
}
