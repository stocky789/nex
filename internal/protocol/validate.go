// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"errors"
)

const maxPoolWireID = 128

func requireNonEmptyBoundedID(value, emptyMsg string) error {
	if value == "" || len(value) > maxPoolWireID {
		return errors.New(emptyMsg)
	}
	return nil
}

func requireBoundedIDMax(value, tooLongMsg string) error {
	if len(value) > maxPoolWireID {
		return errors.New(tooLongMsg)
	}
	return nil
}

func requirePoolCommandTargets(masterHostID, memberHostID, masterErr, memberErr string) error {
	if err := requireNonEmptyBoundedID(masterHostID, masterErr); err != nil {
		return err
	}
	return requireNonEmptyBoundedID(memberHostID, memberErr)
}

func signingBytesClearSignature[T any](v T, clear func(*T)) ([]byte, error) {
	clear(&v)
	return json.Marshal(v)
}
