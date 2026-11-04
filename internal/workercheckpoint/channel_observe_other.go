//go:build !linux

package workercheckpoint

import "errors"

func (*Channel) observeOriginal() (Identity, error) {
	return Identity{}, errors.New("original worker source binding requires Linux")
}
