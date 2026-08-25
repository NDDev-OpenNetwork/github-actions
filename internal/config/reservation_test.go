package config

import "testing"

func TestEffectiveReservationDefaultsToHardLimit(t *testing.T) {
	pool := Pool{Resources: Resources{VCPU: 4, MemoryMiB: 6144}}
	if got, want := pool.EffectiveReservation(), (Reservation{CPUUnits: 4, MemoryMiB: 6144}); got != want {
		t.Fatalf("implicit reservation = %#v, want hard limit %#v", got, want)
	}
}

func TestEffectiveReservationPreservesExplicitMeasuredEnvelope(t *testing.T) {
	pool := Pool{
		Resources:   Resources{VCPU: 4, MemoryMiB: 6144},
		Reservation: Reservation{CPUUnits: 2, MemoryMiB: 4096},
	}
	if got, want := pool.EffectiveReservation(), pool.Reservation; got != want {
		t.Fatalf("explicit reservation = %#v, want %#v", got, want)
	}
}
