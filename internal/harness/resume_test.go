package harness

import "testing"

func TestFormatResumeCommand(t *testing.T) {
	cases := []struct{ provider, binary, session, want string }{
		{"omp", "/usr/local/bin/omp", "/tmp/a.jsonl", "/usr/local/bin/omp --resume /tmp/a.jsonl"},
		{"pi", "pi", "/tmp/b.json", "pi --session /tmp/b.json"},
		{"omp", "/usr/local/bin/omp", "a b.jsonl", "/usr/local/bin/omp --resume 'a b.jsonl'"},
		{"claude", "claude", "sess-1", "claude --resume sess-1"},
		{"commandcode", "cc", "sess-2", "cc --resume sess-2"},
		{"codex", "/usr/local/bin/codex", "01a1d5e6-8759-7cf3-a032-6ee06dfae989", "/usr/local/bin/codex resume 01a1d5e6-8759-7cf3-a032-6ee06dfae989"},
		{"droid", "droid", "sess-d", "droid --resume sess-d"},
		{"devin", "devin", "sess-v", "devin --resume sess-v"},
		{"OMP", "omp", "/tmp/a.jsonl", "omp --resume /tmp/a.jsonl"},
		{"copilot", "copilot", "sess-c", ""},
		{"cursor", "cursor-agent", "chat-1", "cursor-agent --resume chat-1"},
		{"cline", "cline", "sess-cl", "cline --id sess-cl"},
		{"opencode", "opencode", "ses_1", "opencode --session ses_1"},
		{"grok", "grok", "sess-g", "grok --resume sess-g"},
		{"agy", "agy", "conv-1", "agy --conversation conv-1"},
		{"acme", "acme", "abc", ""},
		{"omp", "", "/tmp/a.jsonl", ""},
		{"omp", "/usr/local/bin/omp", "", ""},
	}
	for _, c := range cases {
		if got := FormatResumeCommand(c.provider, c.binary, c.session); got != c.want {
			t.Errorf("FormatResumeCommand(%q, %q, %q) = %q, want %q", c.provider, c.binary, c.session, got, c.want)
		}
	}
}

func TestRegisterResumeTemplateOverridesDefault(t *testing.T) {
	RegisterResumeTemplate("acme", "--resume sessionid")
	RegisterResumeTemplate("claude", "--resume sessionid --fork")
	t.Cleanup(func() {
		RegisterResumeTemplate("acme", "")
		RegisterResumeTemplate("claude", "")
	})
	if got := FormatResumeCommand("acme", "acme", "abc"); got != "acme --resume abc" {
		t.Fatalf("override = %q", got)
	}
	if got := FormatResumeCommand("claude", "claude", "sess-1"); got != "claude --resume sess-1 --fork" {
		t.Fatalf("claude override = %q", got)
	}
	RegisterResumeTemplate("claude", "")
	if got := FormatResumeCommand("claude", "claude", "sess-1"); got != "claude --resume sess-1" {
		t.Fatalf("cleared override = %q", got)
	}
}

func TestFormatResumeCommandIn(t *testing.T) {
	RegisterResumeTemplate("acme", "--resume sessionid")
	t.Cleanup(func() {
		RegisterResumeTemplate("acme", "")
	})
	cases := []struct{ dir, provider, binary, session, want string }{
		{"/tmp/proj", "acme", "acme", "sess-1", "cd /tmp/proj && acme --resume sess-1"},
		{"/Users/me/My Project", "omp", "omp", "sess", "cd '/Users/me/My Project' && omp --resume sess"},
		{"/Users/admin/dev/MCode", "omp", "/Users/admin/.local/bin/omp", "/Users/admin/.omp/agent/sessions/-dev-MCode/2026-11-14T19-40-31-657Z_01a1db93-6229-72c9-859f-f853c3d99352.jsonl", "cd /Users/admin/dev/MCode && /Users/admin/.local/bin/omp --resume 01a1db93-6229-72c9-859f-f853c3d99352"},
		{"/tmp", "omp", "omp", "/tmp/a.jsonl", "cd /tmp && omp --resume /tmp/a.jsonl"},
		{"", "omp", "omp", "sess", "omp --resume sess"},
		{"/tmp", "copilot", "copilot", "sess", ""},
	}
	for _, c := range cases {
		if got := FormatResumeCommandIn(c.dir, c.provider, c.binary, c.session); got != c.want {
			t.Errorf("FormatResumeCommandIn(%q, %q) = %q, want %q", c.dir, c.provider, got, c.want)
		}
	}
}
