// Command invite generates invite codes for paymaster sponsorship gating.
//
// Usage:
//
//	invite --db .devnet/bundler.db [--count 5]
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"

	_ "modernc.org/sqlite"

	"github.com/oleary-labs/signet-min-bundler/internal/sponsor"
)

func main() {
	dbPath := flag.String("db", "", "Path to bundler SQLite database (required)")
	count := flag.Int("count", 1, "Number of invite codes to generate")
	flag.Parse()

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "usage: invite --db <path> [--count N]")
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	store, err := sponsor.Open(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init sponsor store: %v\n", err)
		os.Exit(1)
	}

	codes, err := store.GenerateCodes(*count)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate codes: %v\n", err)
		os.Exit(1)
	}

	for _, code := range codes {
		fmt.Println(code)
	}
}
