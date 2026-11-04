package hostintegration

import (
	"encoding/hex"
	"errors"
	"strings"
)

func decodeDarwinImage(value string) ([]byte, error) {
	const prefix = "«data PNGf"
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, "»") {
		return nil, errors.New("unrecognized macOS PNG clipboard response")
	}
	p, err := hex.DecodeString(strings.TrimSuffix(strings.TrimPrefix(value, prefix), "»"))
	if err != nil {
		return nil, err
	}
	if err := ValidateImage(p); err != nil {
		return nil, err
	}
	return p, nil
}
