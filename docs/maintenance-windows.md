# Maintenance windows on a live member

Everything here was measured on the fleet, most of it on 2026-09-01, when the
drain marker, the slab healer and the member-visibility observer shipped in
one arc and the first live windows found what the fakes could not.

## Drain, through the marker

`gha-fleet drain-member --config /etc/gha-fleet/platform.yaml \
  --reason "<why>" --apply` is the only correct way to take a member out:

- it writes a **drain marker** beside the gate state and leaves the pressure
  timer running, so every tick republishes a fresh closed state carrying
  `drained: <reason>` — the staleness alert stays armed and silent for the
  right reason, and the drain survives a reboot;
- it **recycles warm occupants** (force-stop, then delete: Incus refuses to
  delete a running instance) instead of waiting out disposable capacity;
- it **waits for real jobs** to finish and never aborts one;
- `--restore --apply` clears the marker, republishes from live pressure, and
  heals members drained by older controllers.

The reason string travels: the gate publishes it into the cluster member
config, and the observer reads it there to tell maintenance from an incident.

## What the observer does during a hold

While an offline member carries a `drained: ` reason, the member is **held
out**: inventory gap counts (orphan / missing / uncovered-beyond-grace) move
to `*_unattributable` snapshot fields, a loud listing failure moves to
`listing_unavailable`, the platform stays healthy, and
`gha_fleet_visibility_held_out_members` plus the per-member
`gha_fleet_visibility_degraded` rows say so. The `fleet_visibility_degraded`
ticket pages when a hold outlives half an hour, so the suppression can never
hide a member that failed to come back. An offline member **without** a drain
reason fails collection immediately.

## Stopping the Incus daemon for real

The daemon is socket-activated, and the pressure timer connects to the local
socket every ~11 seconds. Two consequences, both observed:

- `systemctl stop incus` with the socket still up resurrects the daemon
  within seconds. During the thin-pool trim windows the resurrected daemon
  came back **without its storage pool** and answered cluster queries
  partially and silently — the original inventory-gap false pages.
- To hold the daemon down, stop the socket first, and for anything longer
  than seconds, runtime-mask both units:
  `systemctl mask --runtime incus.socket incus && systemctl stop incus.socket incus`.
  Undo with `rm /run/systemd/system/incus.{socket,service}`,
  `systemctl daemon-reload`, then start — a plain `systemctl unmask` does not
  remove a runtime mask.
- The gate publisher needs the local Incus API, so a real daemon-down hold
  longer than the staleness window (about two minutes) will page
  `compute_pressure_state_stale`. Keep such holds short or acknowledge the
  page; the cluster marks the member OFFLINE after its heartbeat threshold
  (~20 s), which is when held-out detection engages.

## The slab healer

`gha-slab-heal.timer` (hourly, per member) runs `gha-fleet slab-heal --apply`:
heal only above the alert's own SUnreclaim budget, only with an open gate
(a closed gate belongs to its operator), only when every other member is
open, only on a jobless member, only outside a twelve-hour cooldown. The heal
drains through the marker, records the cooldown, and reboots only after the
drain reports drained; on boot, `gha-slab-heal-restore.service` reopens the
gate **iff** the standing marker carries the `slab-heal: ` prefix — an
operator's drain is never touched.

## The thin pool and the loop file

One disk per server is the standing decision. The pool's discards mode is
`passdown`, `fstrim.timer` is armed on every member, and worker churn keeps
the loop file at its true size — no shrink ritual exists or is needed. If a
member's file drifts far above the pool's `data_percent`, the measured
recovery is a drain window with the temporary-thin-volume sweep recorded in
the estate's `one-disk-fleet-rollout.json`. LVM metadata archiving is off
(`archive = 0`): reproducible builds are the rollback story, not file copies.
