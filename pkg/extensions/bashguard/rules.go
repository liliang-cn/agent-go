package bashguard

import (
	"path"
	"strings"
)

// Rule is one named kind of destructive command. Set Command for a rule
// about a single simple command, Pipeline for one about how commands are
// joined; a rule may set both.
type Rule struct {
	// Name identifies the rule in findings, allowlist entries and
	// WithoutRules. It must be unique.
	Name string
	// Reason is what the model (or the approver) reads when the rule fires.
	// Say what the command would do, not how it was detected.
	Reason string
	// Command reports whether one simple command matches.
	Command func(c Command) bool
	// Pipeline reports whether a pipeline matches. script is the whole
	// command line, for rules that depend on what else it does.
	Pipeline func(p Pipeline, script Script) bool
}

// Rule names of the default set.
const (
	RuleRecursiveDeleteCritical = "recursive_delete_critical_path"
	RuleDiskOverwrite           = "disk_overwrite"
	RuleRecursivePermRoot       = "recursive_permission_change_root"
	RuleDownloadToShell         = "download_piped_to_shell"
	RuleForcePushMain           = "force_push_main"
	RuleFirewallDisable         = "firewall_disable"
	RuleWriteEtc                = "write_into_etc"
	RuleCredentialExfil         = "credential_to_network"
	RuleKillAll                 = "kill_all_processes"
)

// DefaultRules returns a fresh copy of the default rule set.
func DefaultRules() []Rule {
	return []Rule{
		{
			Name:    RuleRecursiveDeleteCritical,
			Reason:  "it recursively deletes the filesystem root, a home directory or a top-level system directory, which cannot be undone",
			Command: recursiveDeleteCritical,
		},
		{
			Name:    RuleDiskOverwrite,
			Reason:  "it writes directly to a block device, destroying the filesystem on it",
			Command: diskOverwrite,
		},
		{
			Name:    RuleRecursivePermRoot,
			Reason:  "it recursively changes ownership or permissions of the whole filesystem, which breaks the system and cannot be undone",
			Command: recursivePermRoot,
		},
		{
			Name:     RuleDownloadToShell,
			Reason:   "it runs a script downloaded from the network without anyone reading it first; download it to a file and inspect it instead",
			Pipeline: downloadToShell,
		},
		{
			Name:    RuleForcePushMain,
			Reason:  "it force-pushes to main/master, overwriting shared history that others depend on",
			Command: forcePushMain,
		},
		{
			Name:    RuleFirewallDisable,
			Reason:  "it stops the firewall or flushes its rules, exposing the machine to the network",
			Command: firewallDisable,
		},
		{
			Name:    RuleWriteEtc,
			Reason:  "it writes into /etc, changing system-wide configuration",
			Command: writeEtc,
		},
		{
			Name:     RuleCredentialExfil,
			Reason:   "it sends a private key, .env file or cloud credential to the network",
			Command:  credentialUpload,
			Pipeline: credentialPipedOut,
		},
		{
			Name:    RuleKillAll,
			Reason:  "it kills every process (or every root process) on the machine",
			Command: killAll,
		},
	}
}

// ---- helpers ----

// flags splits args into single-letter short flags, long flags and operands.
// Everything after "--" is an operand.
func flags(args []string) (short map[rune]bool, long map[string]bool, operands []string) {
	short, long = map[rune]bool{}, map[string]bool{}
	end := false
	for _, a := range args {
		switch {
		case end || a == "-" || !strings.HasPrefix(a, "-"):
			operands = append(operands, a)
		case a == "--":
			end = true
		case strings.HasPrefix(a, "--"):
			name := strings.TrimPrefix(a, "--")
			if i := strings.IndexByte(name, '='); i >= 0 {
				name = name[:i]
			}
			long[name] = true
		default:
			for _, r := range a[1:] {
				short[r] = true
			}
		}
	}
	return
}

// cleanPath normalises a path operand: collapses //, ., .. and a trailing
// slash, and drops a trailing /* or /. (which address the same directory's
// contents).
func cleanPath(p string) string {
	if p == "" {
		return p
	}
	for _, suf := range []string{"/*", "/.*", "/."} {
		for strings.HasSuffix(p, suf) && len(p) > len(suf) {
			p = strings.TrimSuffix(p, suf)
		}
	}
	if p == "/*" {
		return "/"
	}
	if strings.HasPrefix(p, "~") {
		head, rest, _ := strings.Cut(p, "/")
		if rest == "" {
			return head
		}
		c := path.Clean("/" + rest)
		if c == "/" {
			return head
		}
		return head + c
	}
	c := path.Clean(p)
	if strings.HasPrefix(c, "~") {
		// "./~" is a directory named ~, not home; keep it relative.
		return "./" + c
	}
	return c
}

func isHome(p string) bool {
	p = cleanPath(p)
	if !strings.HasPrefix(p, "~") {
		return false
	}
	return !strings.Contains(p, "/")
}

var systemDirs = map[string]bool{
	"/": true, "/etc": true, "/usr": true, "/bin": true, "/sbin": true, "/var": true,
	"/boot": true, "/lib": true, "/lib64": true, "/opt": true, "/System": true,
	"/home": true, "/Users": true, "/private": true, "/private/etc": true, "/private/var": true,
}

func isCriticalPath(p string) bool {
	return isHome(p) || systemDirs[cleanPath(p)]
}

// ---- default rules ----

func recursiveDeleteCritical(c Command) bool {
	if c.Name != "rm" {
		return false
	}
	short, long, ops := flags(c.Args)
	if !(short['r'] || short['R'] || long["recursive"]) {
		return false
	}
	for _, o := range ops {
		if isCriticalPath(o) {
			return true
		}
	}
	return false
}

var blockDevPrefixes = []string{
	"/dev/sd", "/dev/nvme", "/dev/disk", "/dev/rdisk", "/dev/hd", "/dev/vd", "/dev/xvd", "/dev/mmcblk",
}

func isBlockDevice(p string) bool {
	p = cleanPath(p)
	for _, pre := range blockDevPrefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

func diskOverwrite(c Command) bool {
	switch {
	case c.Name == "dd":
		for _, a := range c.Args {
			if v, ok := strings.CutPrefix(a, "of="); ok && isBlockDevice(v) {
				return true
			}
		}
	case c.Name == "mkfs" || strings.HasPrefix(c.Name, "mkfs.") || c.Name == "newfs" || strings.HasPrefix(c.Name, "newfs_"):
		for _, a := range c.Args {
			if isBlockDevice(a) {
				return true
			}
		}
	}
	return false
}

func recursivePermRoot(c Command) bool {
	if c.Name != "chmod" && c.Name != "chown" && c.Name != "chgrp" {
		return false
	}
	short, long, ops := flags(c.Args)
	if !(short['R'] || long["recursive"]) {
		return false
	}
	for _, o := range ops {
		if cleanPath(o) == "/" {
			return true
		}
	}
	return false
}

var downloaders = map[string]bool{"curl": true, "wget": true}

func runsDownload(ps []Pipeline) bool {
	for _, p := range ps {
		for _, c := range p.Commands {
			if downloaders[c.Name] {
				return true
			}
		}
	}
	return false
}

func downloadToShell(p Pipeline, _ Script) bool {
	downloaded := false
	for _, c := range p.Commands {
		if shells[c.Name] {
			if downloaded {
				return true
			}
			// bash <(curl ...), bash -c "$(curl ...)"
			if runsDownload(c.Nested) {
				return true
			}
		}
		if downloaders[c.Name] {
			downloaded = true
		}
	}
	return false
}

func isMainRef(ref string) bool {
	ref = strings.TrimPrefix(ref, "+")
	if _, dst, ok := strings.Cut(ref, ":"); ok {
		ref = dst
	}
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return ref == "main" || ref == "master"
}

func forcePushMain(c Command) bool {
	if c.Name != "git" {
		return false
	}
	// skip git's global options (-C dir, -c k=v, --git-dir=...)
	args := c.Args
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "-C" || args[0] == "-c" {
			args = args[1:]
		}
		args = args[1:]
	}
	if len(args) == 0 || args[0] != "push" {
		return false
	}
	short, long, ops := flags(args[1:])
	force := short['f'] || long["force"]
	// ops[0] is the remote; the rest are refspecs.
	for i, o := range ops {
		if i == 0 && len(ops) > 1 {
			continue
		}
		if isMainRef(o) && (force || strings.HasPrefix(o, "+")) {
			return true
		}
	}
	return false
}

var firewallServices = map[string]bool{
	"firewalld": true, "ufw": true, "iptables": true, "ip6tables": true, "nftables": true, "pf": true,
}

func firewallDisable(c Command) bool {
	short, long, ops := flags(c.Args)
	switch c.Name {
	case "systemctl":
		stop := false
		for _, o := range ops {
			switch o {
			case "stop", "disable", "mask", "kill":
				stop = true
			default:
				if stop && firewallServices[strings.TrimSuffix(o, ".service")] {
					return true
				}
			}
		}
	case "service":
		return len(ops) >= 2 && firewallServices[ops[0]] && ops[1] == "stop"
	case "ufw":
		return len(ops) >= 1 && (ops[0] == "disable" || ops[0] == "reset")
	case "iptables", "ip6tables", "iptables-legacy", "ip6tables-legacy", "iptables-nft", "ip6tables-nft":
		return short['F'] || long["flush"]
	case "nft":
		return len(ops) >= 2 && ops[0] == "flush" && ops[1] == "ruleset"
	case "pfctl":
		return short['d']
	}
	return false
}

func inEtc(p string) bool {
	p = cleanPath(p)
	for _, root := range []string{"/etc", "/private/etc"} {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}

func writeEtc(c Command) bool {
	for _, r := range c.Redirects {
		if strings.Contains(r.Op, ">") && !strings.HasSuffix(r.Op, "&") && inEtc(r.Target) {
			return true
		}
	}
	_, long, ops := flags(c.Args)
	switch c.Name {
	case "tee":
		for _, o := range ops {
			if inEtc(o) {
				return true
			}
		}
	case "sed", "gsed", "perl":
		inPlace := long["in-place"]
		for _, a := range c.Args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a, 'i') {
				inPlace = true
			}
		}
		if inPlace {
			for _, o := range ops {
				if inEtc(o) {
					return true
				}
			}
		}
	}
	return false
}

// isCredentialPath reports whether a path names an SSH key directory entry,
// a .env file or a cloud provider's credential store.
func isCredentialPath(p string) bool {
	p = strings.TrimPrefix(p, "@") // curl -d @file
	if p == "" {
		return false
	}
	base := path.Base(p)
	if base == ".env" {
		return true
	}
	if suffix, ok := strings.CutPrefix(base, ".env."); ok {
		// .env.local and .env.production hold secrets; .env.example and
		// its kin are templates checked into the repository.
		switch suffix {
		case "example", "sample", "template", "dist", "defaults":
			return false
		}
		return true
	}
	if strings.HasSuffix(base, ".pub") {
		return false // a public key is meant to be shared
	}
	c := cleanPath(p) + "/"
	for _, marker := range []string{"/.ssh/", "/.aws/", "/.config/gcloud/", "/.azure/", "/.kube/"} {
		if strings.Contains(c, marker) || strings.HasPrefix(c, marker[1:]) {
			return true
		}
	}
	return false
}

var networkTools = map[string]bool{
	"curl": true, "wget": true, "nc": true, "ncat": true, "netcat": true, "socat": true,
}

// readsCredential reports whether a command names a credential file as an
// operand or an input.
func readsCredential(c Command) bool {
	for _, a := range c.Args {
		if isCredentialPath(a) {
			return true
		}
		if _, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "-") && isCredentialPath(v) {
			return true
		}
	}
	for _, r := range c.Redirects {
		if strings.HasPrefix(r.Op, "<") && isCredentialPath(r.Target) {
			return true
		}
	}
	return false
}

// credentialUpload: a network tool handed a credential file directly
// (curl -d @~/.ssh/id_rsa, curl -T .env, nc host < ~/.aws/credentials).
func credentialUpload(c Command) bool {
	return networkTools[c.Name] && readsCredential(c)
}

var envDumpers = map[string]bool{"env": true, "printenv": true, "set": true, "export": true}

// credentialPipedOut: a pipeline whose earlier stage reads a credential file
// and whose later stage is a network tool. When the command line loads a
// credential file into the environment (export $(cat .env), source .env), a
// stage that dumps the environment counts as reading it.
func credentialPipedOut(p Pipeline, script Script) bool {
	loaded := false
	for _, sp := range script.Pipelines {
		for _, c := range sp.Commands {
			if readsCredential(c) {
				loaded = true
			}
		}
	}
	read := false
	for _, c := range p.Commands {
		if read && networkTools[c.Name] {
			return true
		}
		if readsCredential(c) || (loaded && envDumpers[c.Name]) {
			read = true
		}
	}
	return false
}

func killAll(c Command) bool {
	switch c.Name {
	case "killall", "pkill":
		for i, a := range c.Args {
			if (a == "-u" || a == "-U" || a == "--user") && i+1 < len(c.Args) {
				if u := c.Args[i+1]; u == "root" || u == "0" {
					return true
				}
			}
			if a == "-uroot" || a == "--user=root" || a == "-u0" {
				return true
			}
		}
		if c.Name == "pkill" {
			_, _, ops := flags(c.Args)
			for _, o := range ops {
				if o == "." || o == ".*" || o == "^" || o == "" {
					return true
				}
			}
		}
	case "kill":
		// pid -1 addresses every process the caller may signal. The first
		// argument may be a signal ("kill -1 123" is SIGHUP to 123), so -1
		// is a target only after it or after "--".
		for i, a := range c.Args {
			if a == "-1" && i > 0 {
				return true
			}
		}
	}
	return false
}
