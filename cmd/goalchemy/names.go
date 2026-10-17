package main

import (
	"flag"
	"fmt"
	"strings"
)

// nameFlags selects private identifiers: -names, -name-collisions, and the
// older -compact-names, which selects compact names (or, set to false, the
// default).
type nameFlags struct {
	names, collisions *string
	compact           *bool
}

func registerNameFlags(fs *flag.FlagSet) *nameFlags {
	return &nameFlags{
		names:      fs.String("names", "", "private identifiers: source (default), readable or compact"),
		collisions: fs.String("name-collisions", "", "under source names, rename (default) or fail on a collision"),
		compact:    fs.Bool("compact-names", false, "use short private generated identifiers (same as -names=compact)"),
	}
}

// hostFlag collects the repeatable -go-host flag: Go files or directories
// compiled into the generated Go package, whose names source names avoid.
type hostFlag []string

func (h *hostFlag) String() string { return strings.Join(*h, ",") }

func (h *hostFlag) Set(value string) error {
	*h = append(*h, value)
	return nil
}

func registerHostFlag(fs *flag.FlagSet) *hostFlag {
	var h hostFlag
	fs.Var(&h, "go-host", "Go file or directory compiled into the generated Go package; repeatable")
	return &h
}

// resolve applies the flags that were set over the configured mode and
// policy and reports conflicting choices.
func (n *nameFlags) resolve(fs *flag.FlagSet, mode, policy string) (string, string, error) {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["compact-names"] {
		mode = ""
		if *n.compact {
			mode = "compact"
		}
	}
	if set["names"] {
		if set["compact-names"] && *n.compact && *n.names != "compact" {
			return "", "", fmt.Errorf("-compact-names conflicts with -names=%s", *n.names)
		}
		mode = *n.names
	}
	if set["name-collisions"] {
		policy = *n.collisions
	}
	return mode, policy, nil
}
