package params

import "os"

// IoConfig defines the shared io parameters.
type IoConfig struct {
	ReadWritePermissions os.FileMode
}

var defaultIoConfig = &IoConfig{
	ReadWritePermissions: 0600, //-rw------- Read and Write permissions for user
}

// BeaconIoConfig returns the current io config for
// the beacon chain.
func BeaconIoConfig() *IoConfig {
	return defaultIoConfig
}
