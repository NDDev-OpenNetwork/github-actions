package incuspolicy

const (
	// DisableNestedVirtualizationRawQEMU is required on Incus 6.0.0. That
	// release accepts security.nesting=false for virtual machines but does not
	// remove the VMX/SVM CPU features from its generated QEMU command line.
	// Keeping this value closed and immutable prevents pool configuration from
	// becoming an arbitrary QEMU argument injection surface.
	DisableNestedVirtualizationRawQEMU = "-cpu host,-vmx,-svm"
)

// VMInstanceConfig returns a fresh map so callers cannot mutate shared policy.
func VMInstanceConfig() map[string]string {
	return map[string]string{
		"raw.qemu": DisableNestedVirtualizationRawQEMU,
	}
}
