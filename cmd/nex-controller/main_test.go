// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"testing"
	"time"
)

func TestClusterRelayWriteDeadlineLeavesResponseMargin(t *testing.T) {
	if clusterRelayWriteTimeout <= 30*time.Second {
		t.Fatalf("write timeout %s can expire before the cluster relay timeout response", clusterRelayWriteTimeout)
	}
}
