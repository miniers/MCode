package gate

import "testing"

func TestRemoteWrites(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"git push", true}, {"git -C /x push origin main", true}, {`bash -c "git push"`, true}, {`sh -lc 'cd x && git push'`, true}, {"echo $(git push)", true}, {"echo \"$(git push)\"", true}, {"echo `git push`", true}, {"env FOO=1 git push", true}, {"timeout 10 git push", true}, {"xargs git push", true}, {"eval git push", true},
		{"curl -X POST https://x", true}, {"curl -XPUT https://x", true}, {"curl --request=DELETE https://x", true}, {"curl -d a=b https://x", true}, {"curl -sd a https://x", true}, {"curl --data-binary @f https://x", true}, {"curl --json '{}' https://x", true}, {"curl -F f=@a https://x", true}, {"curl -T f https://x", true}, {"wget --post-data=a https://x", true},
		{"gh pr create", true}, {"gh issue comment 1 -b x", true}, {"gh api -X DELETE repos/a/b", true}, {"gh api repos/a/b/issues -f title=x", true}, {"gh repo delete a/b", true}, {"ssh host ls", true}, {"scp a host:b", true}, {"rsync -a a host:b", true}, {"npm publish", true}, {"docker push img", true}, {"echo 'unterminated", true},
		{"git log --oneline", false}, {"git status", false}, {"git fetch origin", false}, {"grep -rn 'git push' docs", false}, {`rg "curl -X POST" .`, false}, {"curl -fsSL https://x", false}, {"curl -I https://x", false}, {"curl -X GET https://x", false}, {"wget -qO- https://x", false}, {"gh pr view 1", false}, {"gh pr list", false}, {"gh run watch 1", false}, {"gh api repos/a/b", false}, {"gh api -X GET repos/a/b", false}, {"gh search code foo", false}, {"gh auth status", false}, {"python3 -c 'print(1)'", false}, {"awk '{print $1}' a > /tmp/x", false}, {"go test ./...", false},
		{"env -i -u X FOO=1 sudo nice -n 2 timeout -s TERM -k 2 10 xargs -n 1 git push", true}, {"echo $(echo $(git push))", true}, {"echo '$(git push)'", false}, {"curl -XHEAD https://x", false}, {"wget --method=POST https://x", true}, {"gh api --method=HEAD repos/a/b", true}, {"git status; git push", true}, {"echo \\", true},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			if got := RemoteWrites(tc.command); got != tc.want {
				t.Errorf("RemoteWrites(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
