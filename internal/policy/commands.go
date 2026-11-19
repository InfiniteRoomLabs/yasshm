package policy

import "regexp"

// commandRule is one entry of the built-in command denylist: a stable name
// (what Decision.Rule and the audit log carry) and an RE2 expression matched
// unanchored against the whole command.
type commandRule struct {
	Name    string
	Pattern string
	re      *regexp.Regexp
}

// Building blocks. Most rules match a command word only where a shell would
// run it as a command, so that `grep sudo /var/log/auth.log`, `man reboot`
// and `cat /etc/sudoers` stay allowed while `sudo -l`, `ls; sudo id`,
// `$(sudo id)`, `/usr/bin/sudo id`, `env X=1 nohup sudo id` and
// `sh -c 'sudo id'` are denied. That is a deliberate trade: a word that is
// only an argument is text, not an action.
const (
	// cmdStart: the start of the command, a shell separator or grouping
	// character, a command substitution, or the script argument of `sh -c`
	// (optionally quoted).
	cmdStart = `(?:(?:^|[;&|(){}!\x60\n\r]|\$\()\s*|\b(?:sh|bash|zsh|dash|ksh|mksh|ash|fish)\s+(?:-\w+\s+)*-\w*c\w*\s+['"]?\s*)`
	// wrappers: zero or more words that run the next word as a command
	// (with their options, or numeric arguments such as `nice -n 5` and
	// `timeout 5s`), shell keywords that precede a command, and VAR=value
	// assignments.
	wrappers = `(?:(?:[A-Za-z_][A-Za-z0-9_]*=\S*|sudo|doas|exec|nohup|time|nice|ionice|env|xargs|command|builtin|setsid|stdbuf|timeout|unbuffer|busybox|watch|strace|then|do|else|elif|if|while|until)(?:\s+(?:-\S+|\d\S*))*\s+)*`
	// pathPrefix: an optional directory, as in /usr/bin/sudo or ./sudo.
	pathPrefix = `(?:[^\s;&|()'"\x60]*/)?`
	// wordEnd: the command word ends here.
	wordEnd = `(?:$|[\s;&|()<>'"\x60}])`
	// blockDev: the names of block devices under /dev. /dev/null, /dev/zero,
	// /dev/stdout and the like are not among them.
	blockDev = `/dev/(?:sd|hd|vd|xvd|nvme|mmcblk|dm-|md|loop|mtd|sr|mapper/|disk/)`
	// recursive: an rm option that recurses.
	recursive = `(?:-[a-zA-Z]*[rR][a-zA-Z]*|--recursive)`
	// shells: interpreters a download is piped into.
	shells = `(?:(?:ba|z|da|k|mk|fi|a)?sh|python[0-9.]*|perl|ruby|node|php)`
)

// cmd matches word (an RE2 fragment) in command position.
func cmd(word string) string {
	return cmdStart + wrappers + pathPrefix + `(?:` + word + `)` + wordEnd
}

// cmdArgs matches word in command position followed by args (an RE2 fragment
// that starts right after the word).
func cmdArgs(word, args string) string {
	return cmdStart + wrappers + pathPrefix + `(?:` + word + `)` + args
}

// builtinCommands is the built-in command denylist (spec 5.3). The order
// decides which name a command that hits several rules is reported under.
// Every entry has must-match and must-not-match cases in commands_test.go,
// and that test fails for an entry without both.
var builtinCommands = compileCommands([]commandRule{
	// Privilege escalation. `visudo -c`, `sudoedit`, `which sudo`,
	// `cat /etc/sudoers` and `pseudocode` are not invocations of sudo.
	// `sudo -l` and `sudo -n true` are: the rule denies sudo in any form.
	{Name: "sudo", Pattern: cmd(`sudo`)},
	// `su` as a command; `sum`, `sudo` (its own rule) and `su` as an
	// argument (`id -nG su`) are not.
	{Name: "su", Pattern: cmd(`su`)},
	{Name: "doas", Pattern: cmd(`doas`)},
	{Name: "pkexec", Pattern: cmd(`pkexec`)},
	{Name: "run0", Pattern: cmd(`run0`)},
	// Power control. `last reboot` and `man shutdown` only name the word.
	{Name: "power-control", Pattern: cmd(`shutdown|reboot|poweroff|halt`)},
	// SysV init run levels: `init 0`, `telinit 6`. `init` with no level or
	// `systemd-analyze` is not matched.
	{Name: "init-runlevel", Pattern: cmdArgs(`(?:tel)?init`, `\s+(?:[0-6sSqQ]|single)`+wordEnd)},
	// systemd power and isolation verbs. `systemctl status`, `is-active`
	// and `list-units` are allowed.
	{Name: "systemctl-power", Pattern: cmdArgs(`systemctl`, `(?:\s+-\S+)*\s+(?:poweroff|reboot|soft-reboot|halt|kexec|suspend|hibernate|hybrid-sleep|suspend-then-hibernate|rescue|emergency|isolate|default)`+wordEnd)},
	// File system creation and wiping. `mkdir` and `lsblk -f` are allowed.
	{Name: "fs-create", Pattern: cmd(`mkfs(?:\.\w+)?|mke2fs|mkswap|mkdosfs|mkntfs|wipefs`)},
	// dd onto a block device. `dd if=/dev/zero of=/dev/null` (a benchmark)
	// and dd into a file are allowed.
	{Name: "dd-to-device", Pattern: cmdArgs(`dd`, `[^;&|\n]*\bof=`+blockDev)},
	// A redirect or tee onto a block device. `> /dev/null` is allowed.
	{Name: "write-to-device", Pattern: `>\|?\s*` + blockDev + `|` + cmdArgs(`tee`, `[^;&|\n]*\s`+blockDev)},
	// A recursive rm whose arguments include / or /* (the option before or
	// after it). `rm -rf /tmp/x` and `rm -r ./build/` are allowed.
	{Name: "rm-root", Pattern: cmdArgs(`rm`, `[^;&|\n]*\s`+recursive+`[^;&|\n]*\s/+\*?`+wordEnd) + `|` +
		cmdArgs(`rm`, `[^;&|\n]*\s/+\*?\s[^;&|\n]*`+recursive)},
	// The option exists only to delete /. Matched anywhere.
	{Name: "rm-no-preserve-root", Pattern: `--no-preserve-root\b`},
	// A download piped into a shell or interpreter. `curl ... | grep sh`
	// and `curl ... | shasum` are allowed.
	{Name: "pipe-to-shell", Pattern: `\b(?:curl|wget|fetch)\b[^\n;&]*\|\s*(?:(?:sudo|env)\s+)?` + pathPrefix + shells + wordEnd},
	// A download run through a substitution: `bash <(curl ...)`,
	// `sh -c "$(wget -O- ...)"`, `eval $(curl ...)`, `. <(curl ...)`.
	// `echo $(curl -s https://example.net/ip)` is allowed.
	{Name: "download-substitution", Pattern: `(?:\b(?:(?:ba|z|da|k|mk|fi|a)?sh|source|eval)|(?:^|[\s;&|(])\.)\s+[^\n]*?(?:<\(|\$\(|\x60)\s*(?:curl|wget|fetch)\b`},
	// A function that pipes into itself in the background: `:(){ :|:& };:`
	// and named variants. `f() { ls | wc -l; }` is allowed. Without
	// back-references RE2 cannot check that both sides are the same name.
	{Name: "fork-bomb", Pattern: `[\w:.]+\s*\(\)\s*\{[^}]*\|\s*[\w:.]+\s*&[^}]*\}`},
})

func compileCommands(rules []commandRule) []commandRule {
	for i := range rules {
		rules[i].re = regexp.MustCompile(rules[i].Pattern)
	}
	return rules
}
