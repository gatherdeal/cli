package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// commandNode represents either a runnable command or an intermediate group.
// Groups are inferred from registry paths, so new apps need no parser changes.
type commandNode struct {
	path     []string
	app      string
	command  *Command
	children map[string]*commandNode
}

func commandTree(commands []*Command) *commandNode {
	root := &commandNode{children: map[string]*commandNode{}}
	for _, c := range commands {
		node := root
		for i, part := range c.Path {
			if node.children[part] == nil {
				node.children[part] = &commandNode{path: c.Path[:i+1], app: c.App, children: map[string]*commandNode{}}
			}
			node = node.children[part]
		}
		node.command = c
	}
	return root
}

func (n *commandNode) choices() []string {
	names := make([]string, 0, len(n.children))
	for name := range n.children {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// groupSummaries describes each command group, keyed by its path. Every
// group of the registry needs an entry; TestEveryGroupHasSummary checks it.
var groupSummaries = map[string]string{
	"auth":              "Log in, check or end the access, and list local profiles.",
	"auth profiles":     "Inspect local profiles.",
	"crm":               "Read CRM contacts.",
	"crm contacts":      "List and show CRM contacts.",
	"outlets":           "List and look up the outlets this access may read.",
	"skills":            "Install or print the GatherDeal agent skill.",
	"whatsapp":          "Manage WhatsApp agents and send WhatsApp messages.",
	"whatsapp agents":   "List, create, pair, update and delete WhatsApp agents.",
	"whatsapp messages": "List, show and send WhatsApp messages.",
}

func (n *commandNode) summary() string {
	if n.command != nil {
		return n.command.Summary
	}
	if s, ok := groupSummaries[strings.Join(n.path, " ")]; ok {
		return s
	}
	return "Commands: " + strings.Join(n.choices(), ", ") + "."
}

func (n *commandNode) resolve(args []string) (*commandNode, []string, error) {
	if len(args) > 0 && args[0] == "help" {
		args = append(append([]string{}, args[1:]...), "--help")
	}
	for len(args) > 0 && n.command == nil {
		part := args[0]
		if len(args) == 1 && (part == "--help" || part == "-h" || part == "-help") {
			return n, nil, nil
		}
		child := n.children[part]
		if child == nil {
			kind := "command"
			if len(n.path) == 0 {
				kind = "app or command"
			} else if len(n.path) == 1 && n.app != "" {
				kind = "resource"
			}
			return nil, nil, usageError("unknown %s %q; valid choices: %s", kind, part, strings.Join(n.choices(), ", "))
		}
		n, args = child, args[1:]
	}
	return n, args, nil
}

func writeGroupHelp(w io.Writer, n *commandNode) {
	var b strings.Builder
	name := "gatherdeal"
	if len(n.path) == 0 {
		b.WriteString("gatherdeal - read and manage your GatherDeal business data from the terminal or an AI agent.\n\n")
	} else {
		name += " " + strings.Join(n.path, " ")
		fmt.Fprintf(&b, "%s\n\n", n.summary())
	}
	fmt.Fprintf(&b, "Usage: %s <command> [flags]\n\nCommands:\n", name)
	for _, child := range n.choices() {
		fmt.Fprintf(&b, "  %-22s %s\n", child, n.children[child].summary())
	}
	b.WriteString("\nEvery command (except agent-context, skills show and help) prints one JSON envelope on stdout.\n")
	fmt.Fprintf(&b, "Run `%s` for the machine-readable listing and exit codes.\n", strings.Join(append([]string{"gatherdeal", "commands"}, n.path...), " "))
	fmt.Fprint(w, b.String())
}
