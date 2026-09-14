package main

import (
	"fmt"
	"os"

	backuppkg "github.com/jothost/panel/agent/internal/backup"
)

// openPanelBackup runs -open-panel-backup and returns the exit status.
//
// What it prints is read by an operator rebuilding a host, and by the
// installer, which shows it to them: where the backup came from and when, so
// that restoring the wrong one is a decision rather than an accident. The key
// is never printed.
func openPanelBackup(archive, keyFile, dumpTo string) int {
	if keyFile == "" || dumpTo == "" {
		fmt.Fprintln(os.Stderr, "agent: -open-panel-backup needs -key-file and -dump-to")
		return 2
	}
	key, err := backuppkg.ReadKeyFile(keyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}
	opened, err := backuppkg.OpenPanelBackup(archive, key, dumpTo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}
	fmt.Printf("database=%s\n", opened.Database)
	fmt.Printf("hostname=%s\n", opened.Manifest.Hostname)
	fmt.Printf("created_at=%s\n", opened.Manifest.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"))
	fmt.Printf("agent_version=%s\n", opened.Manifest.AgentVersion)
	fmt.Printf("dump_bytes=%d\n", opened.Bytes)
	return 0
}
