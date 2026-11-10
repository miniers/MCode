package gate

import "testing"

func TestRemoteWrites(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"git --git-dir /x push", true}, {"git --work-tree /w --git-dir /x push origin", true}, {"git --namespace n push", true},
		{"git --super-prefix x push", true}, {"git --config-env x push", true},
		{"git --git-dir", true}, {"git --work-tree", true}, {"git --namespace", true}, {"git --super-prefix", true}, {"git --config-env", true},
		{"git --git-dir /x log", false}, {"git --git-dir=/x status", false},
		{"git push", true}, {"git -C /x push origin main", true}, {`bash -c "git push"`, true}, {`sh -lc 'cd x && git push'`, true}, {"echo $(git push)", true}, {"echo \"$(git push)\"", true}, {"echo `git push`", true}, {"env FOO=1 git push", true}, {"timeout 10 git push", true}, {"xargs git push", true}, {"eval git push", true},
		{"curl -X POST https://x", true}, {"curl -XPUT https://x", true}, {"curl --request=DELETE https://x", true}, {"curl -d a=b https://x", true}, {"curl -sd a https://x", true}, {"curl --data-binary @f https://x", true}, {"curl --json '{}' https://x", true}, {"curl -F f=@a https://x", true}, {"curl -T f https://x", true}, {"wget --post-data=a https://x", true},
		{"gh pr create", true}, {"gh issue comment 1 -b x", true}, {"gh api -X DELETE repos/a/b", true}, {"gh api repos/a/b/issues -f title=x", true}, {"gh repo delete a/b", true}, {"ssh host ls", true}, {"scp a host:b", true}, {"rsync -a a host:b", true}, {"npm publish", true}, {"docker push img", true}, {"echo 'unterminated", true},
		{"git log --oneline", false}, {"git status", false}, {"git fetch origin", false}, {"grep -rn 'git push' docs", false}, {`rg "curl -X POST" .`, false}, {"curl -fsSL https://x", false}, {"curl -I https://x", false}, {"curl -X GET https://x", false}, {"wget -qO- https://x", false}, {"gh pr view 1", false}, {"gh pr list", false}, {"gh run watch 1", false}, {"gh api repos/a/b", false}, {"gh api -X GET repos/a/b", false}, {"gh search code foo", false}, {"gh auth status", false}, {"python3 -c 'print(1)'", false}, {"awk '{print $1}' a > /tmp/x", false}, {"go test ./...", false},
		{"env -i -u X FOO=1 sudo nice -n 2 timeout -s TERM -k 2 10 xargs -n 1 git push", true}, {"echo $(echo $(git push))", true}, {"echo '$(git push)'", false}, {"curl -XHEAD https://x", false}, {"wget --method=POST https://x", true}, {"gh api --method=HEAD repos/a/b", true}, {"git status; git push", true}, {"echo \\", true},
		{"adb devices -l", false}, {"adb -s 10.1.1.86:5555 logcat -d -t 100", false}, {"adb logcat -d -b crash", false}, {"adb exec-out logcat -d", false},
		{"adb shell getprop ro.product.model", false}, {`adb shell 'dumpsys battery | grep level'`, false}, {`adb shell "ps -A | grep com.example"`, false},
		{"adb shell pm list packages", false}, {"adb shell settings get global adb_enabled", false}, {"adb shell wm size", false},
		{`adb shell 'ls /sdcard 2>/dev/null'`, false}, {"adb shell ls /sdcard 2>/dev/null", false}, {"adb shell cat /sdcard/Download/a.txt", false}, {`adb shell 'find /sdcard -name "*.log" | head'`, false},
		{"adb shell md5sum /sdcard/a.bin", false}, {"adb shell run-as com.example cat files/config.json", false}, {"adb shell run-as com.example ls -la databases", false},
		{"adb shell run-as com.example --user 0 cat shared_prefs/a.xml", false}, {"adb exec-out run-as com.example cat databases/app.db", false},
		{"adb exec-out cat /sdcard/a.png", false}, {"adb shell content query --uri content://settings/system", false}, {"adb pull /sdcard/a.txt", false},
		{`adb pull /sdcard/a.txt "$TMPDIR/"`, false}, {"adb forward --list", false}, {"adb wait-for-device", false},
		{"adb shell", true}, {"adb shell touch /sdcard/x", true}, {"adb shell rm -rf /sdcard/x", true}, {`adb shell 'ls; rm /sdcard/x'`, true},
		{`adb shell 'cat a > /sdcard/b'`, true}, {"adb shell run-as com.example rm files/a.json", true}, {"adb shell run-as com.example", true},
		{`adb shell run-as com.example sqlite3 databases/app.db 'delete from t'`, true}, {`adb shell 'sort -o /sdcard/b /sdcard/a'`, true},
		{"adb shell settings put global x 1", true}, {"adb shell pm clear com.x", true}, {"adb shell wm size 1080x1920", true},
		{"adb shell am start -n a/b", true}, {"adb shell input tap 1 1", true}, {"adb shell content insert --uri content://x", true},
		{"adb logcat -c", true}, {"adb logcat -dc", true}, {"adb logcat -G 16M", true}, {"adb install a.apk", true}, {"adb uninstall com.x", true},
		{"adb push a /sdcard/", true}, {"adb reboot", true}, {"adb -s x reboot", true}, {"adb kill-server", true}, {"adb connect 1.2.3.4", true},
		{"adb forward tcp:1 tcp:2", true}, {`bash -c 'adb shell rm /sdcard/x'`, true},
		{`adb shell 'cat a 2>&1'`, false}, {`adb shell 'cat a >/dev/null'`, false}, {`adb shell 'cat a >>/dev/null'`, true},
		{`adb shell 'echo $(rm /sdcard/a)'`, true}, {"adb -s", true}, {"adb shell find /sdcard -delete", true},
		{"adb shell uniq a b", true}, {"adb shell uniq -f 1 a", false}, {"adb shell dmesg -c", true},
		{"adb shell ip addr", false}, {"adb shell ip -4 addr show wlan0", false}, {"adb shell ip route", false}, {"adb shell ip link show", false},
		{"adb shell ifconfig wlan0", false}, {"adb shell ifconfig", false}, {"adb shell xxd -l 64 /sdcard/a.bin", false},
		{"adb shell dumpsys battery", false}, {`adb shell 'dumpsys activity activities | grep mResumed'`, false}, {"adb shell dumpsys meminfo com.example", false},
		{"adb shell date +%s", false}, {"adb shell date", false},
		{"adb shell ip link set wlan0 down", true}, {"adb shell ip addr add 1.2.3.4/24 dev wlan0", true}, {"adb shell ip route del default", true},
		{"adb shell ifconfig wlan0 down", true}, {"adb shell xxd -r a.hex /sdcard/a.bin", true}, {"adb shell xxd /sdcard/a /sdcard/b", true},
		{"adb shell dumpsys battery set level 5", true}, {"adb shell dumpsys battery unplug", true}, {"adb shell dumpsys battery reset", true},
		{"adb shell dumpsys deviceidle force-idle", true}, {"adb shell date 010100002020", true},
		{"adb shell ip -netns test -family inet route list", false}, {"adb shell ip", false}, {"adb shell ip -n", true}, {"adb shell ip tunnel show", true},
		{"adb shell xxd -s 1 -c 16 -g 1 -o 2 /sdcard/a", false}, {"adb shell xxd -revert a", true}, {"adb shell xxd -l", true}, {"adb shell date +%s extra", true},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			if got := RemoteWrites(tc.command); got != tc.want {
				t.Errorf("RemoteWrites(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
