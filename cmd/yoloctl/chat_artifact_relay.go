package main

import _ "embed"

// chatArtifactRelay is the fixed program that reads one chat artifact inside
// the confined chat runtime; see its docstring for the contract. It is
// source-owned and passed as `python3 -c`, never built from request data.
//
//go:embed chat_artifact_relay.py
var chatArtifactRelay string
