package openshell.sandbox

# Baseline OPA test suite for the sandbox policy rego. Run with:
#
#   opa test assets/sandbox/sandbox-policy.rego assets/sandbox/sandbox-policy_test.rego
#
# Wired into `make test-rego` (separate from `go test`); `opa` is not a
# build dep, so CI runs this only when opa is on PATH. The suite is
# deliberately small — covers the canonical allow / deny pivots so
# accidental rego-side regressions surface, not every edge case.
# Extend per-PR when changing matching logic.
#
# Run through `make test-rego` when OPA is available.

# --- allow_network ------------------------------------------------------

# Two policies; the request matches anthropic by host+port+binary.
test_allow_network_matches_anthropic if {
	allow_network with input as {
		"network": {
			"host": "api.anthropic.com",
			"port": 443,
			"protocol": "tcp",
		},
		"exec": {"executable": "/usr/bin/node"},
	}
		with data.network_policies as {
			"anthropic": {
				"endpoints": [{
					"host": "api.anthropic.com",
					"ports": [443],
					"protocol": "tcp",
				}],
				"binaries": [{"path": "/usr/bin/node"}],
			},
		}
}

# Same policy data, but the binary isn't on the allow list — deny.
test_allow_network_rejects_unknown_binary if {
	not allow_network with input as {
		"network": {
			"host": "api.anthropic.com",
			"port": 443,
			"protocol": "tcp",
		},
		"exec": {"executable": "/usr/bin/curl"},
	}
		with data.network_policies as {
			"anthropic": {
				"endpoints": [{
					"host": "api.anthropic.com",
					"ports": [443],
					"protocol": "tcp",
				}],
				"binaries": [{"path": "/usr/bin/node"}],
			},
		}
}

# Empty policy data with a network request → default-deny applies.
test_allow_network_default_deny if {
	not allow_network with input as {
		"network": {"host": "x", "port": 1, "protocol": "tcp"},
		"exec": {"executable": "/bin/sh"},
	}
		with data.network_policies as {}
}

# --- deny_reason --------------------------------------------------------

# Reason "missing input.network" surfaces when the request has no network.
test_deny_reason_missing_network if {
	deny_reason == "missing input.network" with input as {}
}

# Reason "missing input.exec" surfaces when network is present but exec isn't.
test_deny_reason_missing_exec if {
	deny_reason == "missing input.exec" with input as {"network": {"host": "x", "port": 1}}
}

# --- network_action -----------------------------------------------------

# network_action = "deny" when no policy matches.
test_network_action_default_deny if {
	network_action == "deny" with input as {
		"network": {"host": "x", "port": 1, "protocol": "tcp"},
		"exec": {"executable": "/bin/sh"},
	}
		with data.network_policies as {}
}

# network_action = "allow" when a policy matches binary + endpoint.
test_network_action_allow_match if {
	network_action == "allow" with input as {
		"network": {"host": "api.anthropic.com", "port": 443, "protocol": "tcp"},
		"exec": {"executable": "/usr/bin/node"},
	}
		with data.network_policies as {
			"anthropic": {
				"endpoints": [{
					"host": "api.anthropic.com",
					"ports": [443],
					"protocol": "tcp",
				}],
				"binaries": [{"path": "/usr/bin/node"}],
			},
		}
}
