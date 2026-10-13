// Package db manages the Postgres databases devkit creates, through psql.
package db

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Client runs psql against the local server as the current user.
type Client struct {
	Out io.Writer
}

// Exists reports whether the database exists.
func (c Client) Exists(name string) (bool, error) {
	out, err := c.query(fmt.Sprintf("select count(*) from pg_database where datname = '%s'", escape(name)))
	if err != nil {
		return false, err
	}
	return out == "1", nil
}

// Drop removes the database, ending any open session on it first.
func (c Client) Drop(name string) error {
	sessions, err := c.query(fmt.Sprintf("select count(*) from pg_stat_activity where datname = '%s'", escape(name)))
	if err != nil {
		return err
	}
	if sessions != "0" && sessions != "" {
		fmt.Fprintf(c.Out, "Terminating %s open session(s) on %s...\n", sessions, name)
	}
	return c.exec(fmt.Sprintf(`drop database if exists "%s" with (force)`, name))
}

// CreateFromTemplate copies template into a new database with a file copy,
// which takes about a second for a development-sized database.
func (c Client) CreateFromTemplate(name, template string) error {
	return c.exec(fmt.Sprintf(`create database "%s" template "%s" strategy file_copy`, name, template))
}

func (c Client) query(sql string) (string, error) {
	cmd := exec.Command("psql", "-X", "-qtA", "-d", "postgres", "-c", sql)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("psql: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (c Client) exec(sql string) error {
	cmd := exec.Command("psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", "postgres", "-c", sql)
	cmd.Stdout = c.Out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql: %s: %w", sql, err)
	}
	return nil
}

func escape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
