package controller

import "github.com/leogr/yoloctl/internal/operator"

const OperatorMount = operator.Mount
const OperatorSocket = operator.Socket
const HelperMount = operator.HelperMount

// OperatorClient contains routing metadata, not an authorization credential.
// The actual identity belongs to the provisioned listener inside the VM.
type OperatorClient = operator.Client

func LoadOperatorClient() (*OperatorClient, error) {
	return operator.LoadClient()
}
