package clusterinit

import "testing"

// The init panel validates Ceph device rows with these helpers. They accept
// what the flag and --config path accept, including a /dev/ prefix and stable
// /dev/disk/by-id/ paths, and still refuse unsafe values.
func TestDeviceFieldsAcceptFlagPathSpellings(t *testing.T) {
	for _, v := range []string{"sdb", "nvme0n1", "loop0", "/dev/sdb", "disk/by-id/nvme-INTEL_SSDPED1D480GA_PHMB747600CW480DGN", "/dev/disk/by-id/nvme-INTEL_SSDPED1D480GA_PHMB747600CW480DGN"} {
		if err := ValidateDeviceNameField(v); err != nil {
			t.Errorf("%q refused: %v", v, err)
		}
		if err := ValidateNodeDevicePairField("kube-dc001=" + v); err != nil {
			t.Errorf("pair with %q refused: %v", v, err)
		}
	}
	for _, v := range []string{"/etc/passwd x", "/dev/", "-sdb", "sd b", "/dev/sdb\n", "$(id)", "/sdb"} {
		if err := ValidateDeviceNameField(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}
