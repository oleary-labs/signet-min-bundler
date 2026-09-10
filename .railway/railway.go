package main

import "github.com/railwayapp/railway-go-sdk"

// Scopes this file to the bundler service only, so a plan/apply from this repo
// never touches the signet-platform service or Postgres in the same project.
const Partial = "signet-min-bundler"

func Railway(ctx railway.Context) railway.Project {
	ctx = railway.NewContext(ctx)

	// Keystore and the SQLite mempool live here; the container filesystem is
	// ephemeral, so losing this volume loses the invite-code whitelist too.
	data := railway.Volume("signet-min-bundler-volume", map[string]any{
		"region": "sfo",
		"sizeMB": 50000,
	})

	signetMinBundler := railway.ServiceNamed("signet-min-bundler", railway.ServiceConfig{
		"healthcheck":        "/healthz",
		"healthcheckTimeout": 300,

		// Single operator, single hot key: a second replica would race on
		// nonces and double-submit bundles.
		"replicas": 1,

		"deploy": map[string]any{
			"restartPolicyType":       "ON_FAILURE",
			"restartPolicyMaxRetries": 10,
		},

		"volumeMounts": map[string]any{
			"/data": data,
		},

		// Preserve() keeps the value Railway already holds, so secrets are
		// declared (and therefore not deleted on apply) without living in git.
		"variables": map[string]any{
			"BUNDLER_KEYSTORE_B64":      railway.Preserve(),
			"BUNDLER_KEYSTORE_PASSWORD": railway.Preserve(),
			"BUNDLER_PROVER_API_KEY":    railway.Preserve(),
			"BUNDLER_RPC_URL":           railway.Preserve(),
			"BUNDLER_LOG_LEVEL":         "info",
		},
	})

	return railway.ProjectNamed("signet-platform", []any{data, signetMinBundler})
}
